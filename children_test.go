package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
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
// counts them.
func TestDeleteTakesTheTree(t *testing.T) {
	h := tree3(t)
	if err := h.tt.RightClick("Ship the sidebar sort"); err != nil {
		t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem("Delete…"); err != nil {
		t.Fatal(err)
	}
	h.tt.Advance(time.Second)
	if !h.tt.HasText("Delete “Ship the sidebar sort” and its 2 child threads?") {
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
