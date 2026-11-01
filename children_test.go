package main

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/daemon"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
)

// seedChild creates a child of parent on the engine, at Ask, and sends it
// messages, returning once every turn they started has ended.
func seedChild(t *testing.T, d *daemon.Daemon, parent api.ID, title string, messages ...string) api.ID {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ep, id := d.Endpoint(), api.NewID()
	bodies := []api.Intent{api.ThreadCreate{Workspace: "/work/t3code", Title: title, Model: fakeModel, Parent: parent}}
	for _, m := range messages {
		bodies = append(bodies, api.MessageDispatch{Text: m, Mode: api.AfterActive})
	}
	for _, b := range bodies {
		if _, err := ep.Submit(ctx, api.Command{ID: api.NewID(), Target: id, Body: b}); err != nil {
			t.Fatal(err)
		}
	}
	for len(messages) > 0 {
		snap, err := ep.Read(ctx, api.Query{Thread: id})
		if err != nil {
			t.Fatal(err)
		}
		if v := snap.Threads[0]; v.LastTurn != nil && v.Run == api.RunIdle && v.Queued == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return id
}

// tree3 is the app on an engine holding a thread, Ship the sidebar sort,
// with a child, Write the tests, which has a child of its own, Run them,
// each with a finished turn.
func tree3(t *testing.T) *harness {
	t.Helper()
	dir := tempDir(t)
	ad := fake.New([]provider.Event{provider.TextDelta{Text: "Looked into it.\n\nAll done: the sort is tested."}})
	ad.Tokens = 1200
	d := engine(t, daemon.Config{Dir: dir}, ad)
	top := seed(t, d, "/work/t3code", "Ship the sidebar sort", "Ship it.")
	kid := seedChild(t, d, top, "Write the tests", "Write them.")
	seedChild(t, d, kid, "Run them", "Run them.")
	h := start(t, dir)
	h.thread("Run them")
	h.tt.Advance(time.Second)
	return h
}

// A child shows under its parent, not on its own: the parent's row counts
// what's under it and opens it, each level indented under the last.
func TestChildrenNestInTheSidebar(t *testing.T) {
	h := tree3(t)
	for _, it := range h.a.itemsIn(catInbox) {
		if it.t.View.Title != "Ship the sidebar sort" {
			t.Errorf("%q is in the inbox on its own", it.t.View.Title)
		}
	}
	if h.tt.HasText("Write the tests") {
		t.Fatal("a child shows before its parent is opened")
	}
	h.click("2 child threads")
	h.tt.Advance(time.Second)
	top, okTop := h.tt.Find("Ship the sidebar sort")
	kid, okKid := h.tt.Find("Write the tests")
	if !okTop || !okKid || kid.X <= top.X {
		t.Fatalf("the child at %v, its parent at %v; want it under, indented", kid, top)
	}
	h.click("1 child thread")
	h.tt.Advance(time.Second)
	grand, ok := h.tt.Find("Run them")
	if !ok || grand.X <= kid.X {
		t.Fatalf("the grandchild at %v, its parent at %v; want it under, indented", grand, kid)
	}
}

// A child's finished turn shows in its parent's timeline as a report: done,
// with the last of what it said, and a way into it. The parent's header
// adds up its children's tokens.
func TestAChildReportsInItsParent(t *testing.T) {
	h := tree3(t)
	h.a.select_(h.thread("Ship the sidebar sort"))
	h.until("the report", func() bool { return h.tt.HasText("Write the tests") && h.tt.HasText("Done") })
	h.tt.Advance(time.Second)
	if !h.tt.HasText("All done: the sort is tested.") {
		t.Errorf("the report doesn't say what came of it: %q", h.tt.Texts())
	}
	if !h.tt.HasText("1.2k · 3.6k with children") {
		t.Errorf("the header's usage: %q", h.tt.Texts())
	}
	h.click("Write the tests")
	h.tt.Advance(time.Second)
	if h.a.current().View.Title != "Write the tests" {
		t.Fatalf("the report opened %q", h.a.current().View.Title)
	}
	// The child's header leads back up.
	if _, ok := h.tt.Find("Back to Ship the sidebar sort"); !ok {
		t.Errorf("no way back to the parent: %q", h.tt.Texts())
	}
}

// New child thread opens a draft under the thread, which sends as its
// child, no wider than it: the composer's Full access runs at the parent's
// Ask.
func TestNewChildThread(t *testing.T) {
	h := tree3(t)
	h.a.access = accessFull
	if err := h.tt.RightClick("Ship the sidebar sort"); err != nil {
		t.Fatal(err)
	}
	if m := h.tt.Menu(); len(m) < 2 || m[1] != "New child thread" {
		t.Fatalf("menu %q", m)
	}
	if err := h.tt.ChooseMenuItem("New child thread"); err != nil {
		t.Fatal(err)
	}
	h.tt.Advance(time.Second)
	if !h.tt.HasText("New child thread") || !h.tt.HasText("Runs at “Ask for approval”, as its parent does") {
		t.Fatalf("the child's draft: %q", h.tt.Texts())
	}
	h.a.draft = "Check the docs."
	h.tt.Frame()
	h.click("Send")
	h.until("the child", func() bool {
		v := h.a.current().View
		return !v.ID.IsZero() && v.Revision > 0
	})
	v := h.a.current().View
	if top := h.thread("Ship the sidebar sort"); v.Parent != top.id() || v.Access != api.AccessAsk {
		t.Errorf("the new thread: parent %v, access %v; want the parent's child at Ask", v.Parent, v.Access)
	}
}

// A thread at the deepest level starts no more: the menu's item is off.
func TestNoChildBelowTheDeepest(t *testing.T) {
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(false)))
	at := seed(t, d, "/work/t3code", "Level 0")
	for i := 1; i <= maxDepth; i++ {
		at = seedChild(t, d, at, fmt.Sprintf("Level %d", i))
	}
	h := start(t, dir)
	for i := range maxDepth {
		h.a.childrenOpen[h.thread(fmt.Sprintf("Level %d", i)).id()] = true
	}
	h.thread(fmt.Sprintf("Level %d", maxDepth))
	h.tt.Advance(time.Second)
	for _, tc := range []struct {
		row string
		ok  bool
	}{{fmt.Sprintf("Level %d", maxDepth-1), true}, {fmt.Sprintf("Level %d", maxDepth), false}} {
		if err := h.tt.RightClick(tc.row); err != nil {
			t.Fatal(err)
		}
		err := h.tt.ChooseMenuItem("New child thread")
		if (err == nil) != tc.ok {
			t.Errorf("New child thread on %s: %v; want it on %v", tc.row, err, tc.ok)
		}
		h.tt.CloseMenu()
	}
}

// Deleting a parent deletes what's under it too, after a confirm that
// counts them: here a child and its own child, two threads under it.
func TestDeleteTakesTheTree(t *testing.T) {
	h := tree3(t)
	if err := h.tt.RightClick("Ship the sidebar sort"); err != nil {
		t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem("Delete…"); err != nil {
		t.Fatal(err)
	}
	h.tt.Advance(time.Second)
	if !h.tt.HasText("Delete “Ship the sidebar sort” and the 2 threads under it?") {
		t.Fatalf("no confirm: %q", h.tt.Texts())
	}
	h.click("Delete")
	h.until("the tree deleted", func() bool {
		for _, title := range []string{"Ship the sidebar sort", "Write the tests", "Run them"} {
			for _, e := range h.a.backend.Envs() {
				for _, th := range e.Threads {
					if th.View.Title == title {
						return false
					}
				}
			}
		}
		return true
	})
}

// Stopping a thread stops only its own agent; Stop all in the Agents panel
// stops what works under it, and says so.
func TestStopAllInTheAgentsPanel(t *testing.T) {
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, stalled{fake.New(midTurn), 1})
	top := seed(t, d, "/work/t3code", "Ship the sidebar sort")
	kid := seedChild(t, d, top, "Write the tests")
	for _, id := range []api.ID{top, kid} {
		if _, err := d.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: id, Body: api.MessageDispatch{Text: "go"}}); err != nil {
			t.Fatal(err)
		}
	}
	h := start(t, dir)
	parent, child := h.thread("Ship the sidebar sort"), h.thread("Write the tests")
	h.until("both working", func() bool { return parent.View.Working() && child.View.Working() })
	h.a.select_(parent)
	h.tt.Advance(time.Second)
	h.tt.Key(ui.Cmd, ui.KeyEnter)
	h.until("the parent stopped", func() bool { return !parent.View.Live() })
	if !child.View.Live() {
		t.Fatal("stopping the parent stopped its child")
	}
	h.click("Agents")
	h.tt.Advance(time.Second)
	h.click("Stop all agents")
	h.until("the child stopped", func() bool { return !child.View.Live() })
	if !h.tt.HasText("1 child thread") {
		t.Errorf("no word of the child stopped: %q", strings.Join(h.tt.Texts(), " | "))
	}
}

// Deleting a parent whose child works stops the child, then deletes it once
// its agent has stopped, as the engine deletes no thread at work.
func TestDeleteWaitsForAWorkingChild(t *testing.T) {
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, stalled{fake.New(midTurn), 1})
	top := seed(t, d, "/work/t3code", "Ship the sidebar sort")
	kid := seedChild(t, d, top, "Write the tests")
	if _, err := d.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: kid, Body: api.MessageDispatch{Text: "go"}}); err != nil {
		t.Fatal(err)
	}
	h := start(t, dir)
	child := h.thread("Write the tests")
	h.until("the child working", func() bool { return child.View.Working() })
	h.a.settings.confirmDelete = false
	h.a.askDelete(h.thread("Ship the sidebar sort"))
	h.until("both deleted", func() bool {
		_, p := h.a.find(top)
		_, k := h.a.find(kid)
		return !p && !k
	})
}

// counting counts the agents its adapter opens, one a turn: it assumes the
// runtime reuses no agent across turns.
type counting struct {
	provider.Adapter
	opens *atomic.Int32
}

func (c counting) Open(ctx context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	c.opens.Add(1)
	return c.Adapter.Open(ctx, spec)
}

// Deleting a tree whose child works with a follow-up queued halts the
// child: the follow-up is cancelled, not started as the stop lands, and
// both threads go.
func TestDeleteTreeWithAQueuedFollowUp(t *testing.T) {
	dir := tempDir(t)
	var opens atomic.Int32
	d := engine(t, daemon.Config{Dir: dir}, counting{stalled{fake.New(midTurn), 1}, &opens})
	top := seed(t, d, "/work/t3code", "Ship the sidebar sort")
	kid := seedChild(t, d, top, "Write the tests")
	for _, m := range []string{"go", "then this"} {
		if _, err := d.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: kid, Body: api.MessageDispatch{Text: m, Mode: api.AfterActive}}); err != nil {
			t.Fatal(err)
		}
	}
	h := start(t, dir)
	child := h.thread("Write the tests")
	h.until("the child working, its follow-up queued", func() bool { return child.View.Working() && child.View.Queued == 1 })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st, err := d.Endpoint().Open(ctx, api.Subscription{Thread: kid})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cancelled := make(chan string, 8)
	go func() {
		for {
			it, err := st.Next(ctx)
			if err != nil {
				return
			}
			if it.Event == nil {
				continue
			}
			if b, err := it.Event.Decode(); err == nil {
				if c, ok := b.(api.MessageCancelled); ok {
					cancelled <- c.Reason
				}
			}
		}
	}()
	before := opens.Load()
	h.a.settings.confirmDelete = false
	h.a.askDelete(h.thread("Ship the sidebar sort"))
	h.until("both deleted", func() bool {
		_, p := h.a.find(top)
		_, k := h.a.find(kid)
		return !p && !k
	})
	select {
	case r := <-cancelled:
		if r != "the thread is being deleted" {
			t.Errorf("the follow-up was cancelled for %q, want the delete", r)
		}
	case <-time.After(5 * time.Second):
		t.Error("the follow-up was never cancelled")
	}
	if n := opens.Load() - before; n != 0 {
		t.Errorf("%d turns started after the delete, want none", n)
	}
	if len(h.a.afterStop) != 0 {
		t.Errorf("deletions left waiting: %v", h.a.afterStop)
	}
}

// A cascade gives up on a line whose child's stop didn't land in time:
// the parent waiting for it isn't deleted on its own later.
func TestACascadeGivesUpOnALineTogether(t *testing.T) {
	h := tree3(t)
	top, kid := h.thread("Ship the sidebar sort"), h.thread("Write the tests")
	// deleteTree stamps a cascade once, but settleStopped reads the clock
	// per entry, so a child can pass the grace a moment before its parent:
	// here, by a lot, to make that moment.
	old := time.Now().Add(-2 * stopGrace)
	h.a.afterStop[kid.id()] = stopped{at: old}
	h.a.afterStop[top.id()] = stopped{after: []api.ID{kid.id()}, at: time.Now()}
	h.a.settleStopped()
	if len(h.a.afterStop) != 0 {
		t.Fatalf("left waiting: %v", h.a.afterStop)
	}
	h.tt.Advance(time.Second)
	if _, ok := h.a.find(top.id()); !ok || deleting(top) {
		t.Error("the parent was deleted though its child's stop never landed")
	}
}

// Scrolled up, the composer compact, ⌘⏎ still stops the working agent, as
// the full composer's Send does.
func TestCmdEnterStopsFromTheCompactComposer(t *testing.T) {
	var script []provider.Event
	for i := range 30 {
		script = append(script, provider.Work{Item: fmt.Sprint("r", i), Kind: "read", Title: fmt.Sprintf("Read file%d.go", i), Status: api.WorkCompleted})
	}
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, stalled{fake.New(script), len(script)})
	id := seed(t, d, "/work/t3code", "Read everything")
	if _, err := d.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: id, Body: api.MessageDispatch{Text: "Read them all."}}); err != nil {
		t.Fatal(err)
	}
	h := start(t, dir)
	th := h.thread("Read everything")
	h.a.select_(th)
	h.until("the reads", func() bool { return h.tt.HasText("Read file29.go") && th.View.Working() })
	h.tt.Advance(time.Second)
	h.a.listFor(th.id()).ScrollTo(0, ui.Start)
	h.tt.Frame()
	h.tt.Advance(time.Second)
	if h.a.composerK != 0 {
		t.Fatalf("scrolled up: composer at %v, want compact", h.a.composerK)
	}
	h.tt.Key(ui.Cmd, ui.KeyEnter)
	h.until("the stop", func() bool { return !th.View.Live() })
}

// Deleting a tree waits only where it must: a parent waits on its working
// child, and an idle branch beside it, its own child with it, goes at once,
// whichever of the two branches comes first. Should the working child's
// stop never land, the parent is given up on with it, and the idle branch
// is gone all the same.
func TestDeleteWaitsOnlyOnItsOwnLine(t *testing.T) {
	for _, idleFirst := range []bool{false, true} {
		t.Run(fmt.Sprint("idle branch first: ", idleFirst), func(t *testing.T) {
			dir := tempDir(t)
			d := engine(t, daemon.Config{Dir: dir}, stalled{fake.New(midTurn), 1})
			top := seed(t, d, "/work/t3code", "Ship the sidebar sort")
			live := seedChild(t, d, top, "Write the tests")
			idle := seedChild(t, d, top, "Update the docs")
			leaf := seedChild(t, d, idle, "Fix the typo")
			submit := func(id api.ID, b api.Intent) {
				if _, err := d.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: id, Body: b}); err != nil {
					t.Fatal(err)
				}
			}
			submit(live, api.MessageDispatch{Text: "go"})
			h := start(t, dir)
			h.until("the child working", func() bool { th, _ := h.a.find(live); return th.View.Working() })
			// The newest child comes first in the walk.
			if idleFirst {
				submit(idle, api.ThreadRename{Title: "Update the docs again"})
				h.until("the rename", func() bool { th, _ := h.a.find(idle); return th.View.Title == "Update the docs again" })
			}
			h.tt.Frame()
			kids := h.a.kids[top]
			if first := kids[0].id() == idle; first != idleFirst {
				t.Fatalf("the idle branch first: %v, want %v", first, idleFirst)
			}
			th, _ := h.a.find(top)
			h.a.deleteTree(th)
			for _, id := range []api.ID{idle, leaf} {
				if x, _ := h.a.find(id); !deleting(x) {
					t.Errorf("%q waits, though nothing under it works", x.View.Title)
				}
			}
			if len(h.a.afterStop) != 2 || !slices.Equal(h.a.afterStop[top].after, []api.ID{live}) {
				t.Fatalf("held: %v; want the working child, and its parent waiting on it alone", h.a.afterStop)
			}
			// Its stop never lands: the line is given up on together.
			s := h.a.afterStop[live]
			s.at = time.Now().Add(-2 * stopGrace)
			h.a.afterStop[live] = s
			h.a.settleStopped()
			if len(h.a.afterStop) != 0 {
				t.Errorf("still held: %v", h.a.afterStop)
			}
			if x, _ := h.a.find(top); deleting(x) {
				t.Error("the parent was deleted though its child's stop never landed")
			}
		})
	}
}

// Giving up on a thread gives up on every thread over it, up the line, not
// only its parent.
func TestGivingUpGoesUpTheLine(t *testing.T) {
	h := tree3(t)
	top, mid, leaf := h.thread("Ship the sidebar sort"), h.thread("Write the tests"), h.thread("Run them")
	h.a.afterStop[leaf.id()] = stopped{at: time.Now().Add(-2 * stopGrace)}
	h.a.afterStop[mid.id()] = stopped{after: []api.ID{leaf.id()}, at: time.Now()}
	h.a.afterStop[top.id()] = stopped{after: []api.ID{mid.id()}, at: time.Now()}
	h.a.settleStopped()
	if len(h.a.afterStop) != 0 {
		t.Fatalf("still held: %v", h.a.afterStop)
	}
	for _, x := range []thread{top, mid} {
		if deleting(x) {
			t.Errorf("%q was deleted though the line under it was given up on", x.View.Title)
		}
	}
}

// A blocked child's report says Needs you only while the child still waits
// on its request: once its run is stopped, the same card says it was
// waiting, with what it waited on.
func TestABlockedReportStopsNeedingYou(t *testing.T) {
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(true)))
	top := seed(t, d, "/work/t3code", "Ship the sidebar sort")
	kid := seedChild(t, d, top, "Write the tests")
	if _, err := d.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: kid, Body: api.MessageDispatch{Text: "go"}}); err != nil {
		t.Fatal(err)
	}
	h := start(t, dir)
	h.a.select_(h.thread("Ship the sidebar sort"))
	h.until("the blocked report", func() bool { return h.tt.HasText("Needs you") && h.tt.HasText("Wants to edit main.go") })
	child := h.thread("Write the tests")
	h.a.backend.Interrupt(child.env, child.id())
	h.until("the stop", func() bool { return !child.View.Live() })
	h.tt.Advance(time.Second)
	if h.tt.HasText("Needs you") || !h.tt.HasText("Was waiting") || !h.tt.HasText("Wants to edit main.go") {
		t.Errorf("after the stop, the card shows %q", h.tt.Texts())
	}
}

// A run that asked twice reported twice, the parent's text between: two
// cards, each keyed apart, and the same way each frame.
func TestTwoReportsOfARunAreKeyedApart(t *testing.T) {
	run, kid := api.NewID(), api.NewID()
	at := time.Now()
	first := client.ReportEntry{ChildReported: api.ChildReported{Child: kid, Run: run, Kind: api.ReportBlocked}, At: at}
	second := first
	second.At = at.Add(time.Second)
	parent := thread{Thread: &client.Thread{Transcript: &client.Transcript{Entries: []client.Entry{&first, &client.AssistantEntry{Run: api.NewID(), Text: "ok"}, &second}}}}
	cards := func() []string {
		var keys []string
		for _, e := range entriesOf(parent) {
			if e.kind == entryReport {
				keys = append(keys, e.key)
			}
		}
		return keys
	}
	keys := cards()
	if len(keys) != 2 || keys[0] == keys[1] {
		t.Fatalf("report cards keyed %q, want two keys apart", keys)
	}
	if again := cards(); !slices.Equal(again, keys) {
		t.Errorf("keyed %q, then %q: want the same each frame", keys, again)
	}
}

// A run that asked twice reported twice: only its last report, of the
// request still waiting, needs the user; the first was answered.
func TestOnlyTheLastBlockedReportNeedsYou(t *testing.T) {
	run, kid := api.NewID(), api.NewID()
	at := time.Now()
	first := client.ReportEntry{ChildReported: api.ChildReported{Child: kid, Run: run, Kind: api.ReportBlocked}, At: at}
	second := first
	second.At = at.Add(time.Second)
	parent := thread{Thread: &client.Thread{Transcript: &client.Transcript{Entries: []client.Entry{&first, &client.AssistantEntry{Text: "ok"}, &second}}}}
	child := thread{Thread: &client.Thread{View: api.Thread{ID: kid, Requests: []api.Request{{ID: api.NewID(), Run: run}}}}}
	if stillBlocked(parent, child, first) || !stillBlocked(parent, child, second) {
		t.Errorf("first %v, second %v; want only the second to stand", stillBlocked(parent, child, first), stillBlocked(parent, child, second))
	}
	child.View.Requests = nil
	if stillBlocked(parent, child, second) {
		t.Error("a report stands with nothing waiting")
	}
}

// A child's draft is left behind when another thread is selected: should
// that thread go, the draft shown in its place is a thread of the user's,
// and sends as one, not under the parent it was once started under.
func TestSelectingAnotherThreadDropsTheChildDraft(t *testing.T) {
	h := tree3(t)
	top := h.thread("Ship the sidebar sort")
	h.a.startChild(top)
	if v := h.a.current().View; v.Parent != top.id() {
		t.Fatalf("the child's draft is under %v, want %v", v.Parent, top.id())
	}
	other := h.thread("Run them")
	h.a.select_(other)
	h.a.backend.Delete(other.env, other.id()) // as another GUI would
	h.until("the other thread gone", func() bool { _, ok := h.a.find(other.id()); return !ok })
	if v := h.a.current().View; !v.ID.IsZero() || !v.Parent.IsZero() {
		t.Fatalf("with the selected thread gone the chat shows %q under %v; want a draft of the user's", v.Title, v.Parent)
	}
	h.tt.Frame()
	h.a.draft = "Check the docs."
	h.tt.Frame()
	h.click("Send")
	h.until("the new thread", func() bool {
		v := h.a.current().View
		return !v.ID.IsZero() && v.Revision > 0
	})
	if v := h.a.current().View; !v.Parent.IsZero() {
		t.Errorf("the new thread is under %v, want a thread of the user's", v.Parent)
	}
}

// A child whose agent works shows in Working, the way to its parent under
// its title, while its parent is snoozed or settled, out of sight; it nests
// again once its parent is back in the inbox, or it stops.
func TestAWorkingChildOfASettledThreadShowsInWorking(t *testing.T) {
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, stalled{fake.New(midTurn), 1})
	top := seed(t, d, "/work/t3code", "Ship the sidebar sort")
	kid := seedChild(t, d, top, "Write the tests")
	submit := func(id api.ID, b api.Intent) {
		t.Helper()
		if _, err := d.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: id, Body: b}); err != nil {
			t.Fatal(err)
		}
	}
	submit(top, api.InboxSet{Disposition: api.Disposition{State: api.Settled}})
	submit(kid, api.MessageDispatch{Text: "go"})
	h := start(t, dir)
	parent, child := h.thread("Ship the sidebar sort"), h.thread("Write the tests")
	h.until("the child working under its settled parent", func() bool {
		return child.View.Working() && parent.View.Disposition.State == api.Settled
	})
	inWorking := func() bool {
		h.tt.Frame()
		return slices.ContainsFunc(h.a.itemsIn(catWorking), func(it inboxItem) bool { return it.t.id() == kid })
	}
	if !inWorking() {
		t.Fatal("a working child of a settled thread isn't in Working")
	}
	h.click("Working")
	h.tt.Advance(time.Second)
	if _, ok := h.tt.Find("under Ship the sidebar sort"); !ok {
		t.Errorf("the child's row doesn't lead to its parent: %q", strings.Join(h.tt.Texts(), " | "))
	}
	h.a.backend.SetDisposition(parent.env, top, api.Disposition{})
	h.until("the parent back in the inbox", func() bool { return parent.View.Disposition.State == api.Open })
	if inWorking() {
		t.Error("the child stays in Working with its parent back in the inbox")
	}
	h.a.backend.SetDisposition(parent.env, top, api.Disposition{State: api.Snoozed, Until: time.Now().Add(time.Hour)})
	h.until("the parent snoozed", func() bool { return parent.View.Disposition.State == api.Snoozed })
	if !inWorking() {
		t.Fatal("a working child of a snoozed thread isn't in Working")
	}
	h.a.backend.Interrupt(child.env, kid)
	h.until("the child stopped", func() bool { return !child.View.Live() })
	if inWorking() {
		t.Error("a stopped child stays in Working")
	}
}

// The delete confirm counts the threads under the one deleted, not it:
// one child is the thread under it.
func TestTheDeleteConfirmCountsOnlyWhatsUnder(t *testing.T) {
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(false)))
	top := seed(t, d, "/work/t3code", "Ship the sidebar sort")
	seedChild(t, d, top, "Write the tests")
	h := start(t, dir)
	h.thread("Write the tests")
	h.a.askDelete(h.thread("Ship the sidebar sort"))
	h.tt.Frame()
	h.tt.Advance(time.Second)
	want := "Delete “Ship the sidebar sort” and the thread under it?"
	if !h.tt.HasText(want) {
		t.Fatalf("the confirm: %q; want %q", strings.Join(h.tt.Texts(), " | "), want)
	}
}

// A child's report is told of, with the window in the background, once:
// on its parent, saying how the child went.
// Under a settled thread, a working child's working child shows in that
// child's rows, not in Working a second time on its own.
func TestALiftedChildsChildIsntLiftedToo(t *testing.T) {
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, stalled{fake.New(midTurn), 1})
	top := seed(t, d, "/work/t3code", "Ship the sidebar sort")
	kid := seedChild(t, d, top, "Write the tests")
	leaf := seedChild(t, d, kid, "Run them")
	for _, c := range []struct {
		id api.ID
		b  api.Intent
	}{{top, api.InboxSet{Disposition: api.Disposition{State: api.Settled}}}, {kid, api.MessageDispatch{Text: "go"}}, {leaf, api.MessageDispatch{Text: "go"}}} {
		if _, err := d.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: c.id, Body: c.b}); err != nil {
			t.Fatal(err)
		}
	}
	h := start(t, dir)
	child, grand := h.thread("Write the tests"), h.thread("Run them")
	h.until("both working", func() bool { return child.View.Working() && grand.View.Working() })
	h.tt.Frame()
	var ids []api.ID
	for _, it := range h.a.itemsIn(catWorking) {
		ids = append(ids, it.t.id())
	}
	if !slices.Contains(ids, kid) || slices.Contains(ids, leaf) {
		t.Fatalf("Working lists %v; want the child %v and not its child %v", ids, kid, leaf)
	}
}

func TestAChildsReportNotifies(t *testing.T) {
	for _, tc := range []struct {
		name   string
		ask    bool
		opened bool // the parent's transcript open, the chat showing it
		want   string
	}{
		{"blocked", true, false, "Write the tests: Needs you"},
		{"done", false, false, ""}, // its turn's end tells of it
		{"blocked, the parent shown", true, true, "Write the tests: Needs you"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := tempDir(t)
			d := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(tc.ask)))
			top := seed(t, d, "/work/t3code", "Ship the sidebar sort")
			kid := seedChild(t, d, top, "Write the tests")
			h := start(t, dir)
			var got []mygo.NotificationOptions
			h.a.notify = func(o mygo.NotificationOptions) { got = append(got, o) }
			parent := h.thread("Ship the sidebar sort")
			h.thread("Write the tests")
			if tc.opened {
				h.a.select_(parent)
				h.until("the parent's transcript", func() bool { return parent.Transcript != nil })
			}
			h.tt.Frame()
			if _, err := d.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: kid, Body: api.MessageDispatch{Text: "go"}}); err != nil {
				t.Fatal(err)
			}
			reports := func() []mygo.NotificationOptions {
				var rs []mygo.NotificationOptions
				for _, o := range got {
					if o.ID == "thread:"+top.String() {
						rs = append(rs, o)
					}
				}
				return rs
			}
			if tc.want == "" {
				h.until("the child's turn's end", func() bool {
					return slices.ContainsFunc(got, func(o mygo.NotificationOptions) bool { return o.ID == "thread:"+kid.String() })
				})
			} else {
				h.until("the report's notification", func() bool { return len(reports()) > 0 })
			}
			for range 5 {
				h.p.run()
				h.tt.Advance(100 * time.Millisecond)
			}
			rs := reports()
			if tc.want == "" {
				if len(rs) != 0 {
					t.Fatalf("notifications on the parent: %+v; want none", rs)
				}
				return
			}
			if len(rs) != 1 || rs[0].Title != "Ship the sidebar sort" || rs[0].Body != tc.want {
				t.Fatalf("notifications on the parent: %+v; want one, %q", rs, tc.want)
			}
		})
	}
}
