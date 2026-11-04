package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
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

// Deleting a parent whose child works deletes both: the engine ends the
// child's run as it deletes it.
func TestDeleteTakesAWorkingChild(t *testing.T) {
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

// Deleting a tree whose child works with a follow-up queued starts no
// turn of the follow-up's, and both threads go.
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
	before := opens.Load()
	h.a.settings.confirmDelete = false
	h.a.askDelete(h.thread("Ship the sidebar sort"))
	h.until("both deleted", func() bool {
		_, p := h.a.find(top)
		_, k := h.a.find(kid)
		return !p && !k
	})
	h.settle(func() bool { return false })
	if n := opens.Load() - before; n != 0 {
		t.Errorf("%d turns started after the delete, want none", n)
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

// A child whose agent works shows in Working, the way to its parent under
// its title, while its parent is snoozed, out of sight; it nests again once
// its parent is back in the inbox, or it stops. Under a settled parent it
// isn't lifted: settling from here halts it.
func TestAWorkingChildOfASnoozedThreadShowsInWorking(t *testing.T) {
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
	submit(top, api.InboxSet{Disposition: api.Disposition{State: api.Snoozed, Until: time.Now().Add(time.Hour)}})
	submit(kid, api.MessageDispatch{Text: "go"})
	h := start(t, dir)
	parent, child := h.thread("Ship the sidebar sort"), h.thread("Write the tests")
	h.until("the child working under its snoozed parent", func() bool {
		return child.View.Working() && parent.View.Disposition.State == api.Snoozed
	})
	inWorking := func() bool {
		h.tt.Frame()
		return slices.ContainsFunc(h.a.itemsIn(catWorking), func(it inboxItem) bool { return it.t.id() == kid })
	}
	if !inWorking() {
		t.Fatal("a working child of a snoozed thread isn't in Working")
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
	// As another GUI might leave it: settled, the child still working.
	h.a.backend.SetDisposition(parent.env, top, api.Disposition{State: api.Settled})
	h.until("the parent settled", func() bool { return parent.View.Disposition.State == api.Settled })
	if inWorking() {
		t.Error("a working child of a settled thread is lifted into Working")
	}
	h.a.backend.SetDisposition(parent.env, top, api.Disposition{State: api.Snoozed, Until: time.Now().Add(time.Hour)})
	h.until("the parent snoozed", func() bool { return parent.View.Disposition.State == api.Snoozed })
	h.a.backend.Interrupt(child.env, kid)
	h.until("the child stopped", func() bool { return !child.View.Live() })
	if inWorking() {
		t.Error("a stopped child stays in Working")
	}
}

// Settling a thread settles every thread under it and halts those
// working, its children's children too: nothing under a settled thread
// works.
func TestSettlingStopsTheTree(t *testing.T) {
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, stalled{fake.New(midTurn), 1})
	top := seed(t, d, "/work/t3code", "Ship the sidebar sort")
	kid := seedChild(t, d, top, "Write the tests")
	leaf := seedChild(t, d, kid, "Run them")
	for _, id := range []api.ID{kid, leaf} {
		if _, err := d.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: id, Body: api.MessageDispatch{Text: "go"}}); err != nil {
			t.Fatal(err)
		}
	}
	h := start(t, dir)
	child, grand := h.thread("Write the tests"), h.thread("Run them")
	h.until("both working", func() bool { return child.View.Working() && grand.View.Working() })
	h.tt.Advance(time.Second)
	if err := h.tt.RightClick("Ship the sidebar sort"); err != nil {
		t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem("Mark settled"); err != nil {
		t.Fatal(err)
	}
	h.until("the tree stopped and settled", func() bool {
		for _, title := range []string{"Ship the sidebar sort", "Write the tests", "Run them"} {
			if v := h.thread(title).View; v.Live() || v.Disposition.State != api.Settled {
				return false
			}
		}
		return true
	})
	h.tt.Frame()
	if n := len(h.a.itemsIn(catWorking)); n != 0 {
		t.Errorf("Working lists %d threads under a settled tree", n)
	}
}

// No user path starts a child: an agent does. The thread menu and the
// agents panel offer none.
func TestNoUserPathStartsAChild(t *testing.T) {
	h := tree3(t)
	if err := h.tt.RightClick("Ship the sidebar sort"); err != nil {
		t.Fatal(err)
	}
	if m := h.tt.Menu(); slices.Contains(m, "New child thread") {
		t.Errorf("the thread menu offers a child: %q", m)
	}
	h.tt.CloseMenu()
	h.a.select_(h.thread("Ship the sidebar sort"))
	h.until("its report", func() bool { return h.tt.HasText("Done") })
	h.a.agentsOpen[h.a.current().id()] = true
	clear(h.a.childrenOpen)
	h.tt.Advance(time.Second)
	r, ok := h.tt.Find("Run them")
	if !ok {
		t.Fatalf("no agents panel: %q", h.tt.Texts())
	}
	h.tt.Move(r.X+r.W/2, r.Y+r.H/2)
	h.tt.Advance(time.Second)
	if _, ok := h.tt.Find("Thread actions"); !ok {
		t.Fatalf("the panel's row isn't pointed at: %q", h.tt.Texts())
	}
	if _, ok := h.tt.Find("New child thread"); ok {
		t.Error("the agents panel offers a child")
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
// Under a snoozed thread, a working child's working child shows in that
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
	}{{top, api.InboxSet{Disposition: api.Disposition{State: api.Snoozed, Until: time.Now().Add(time.Hour)}}}, {kid, api.MessageDispatch{Text: "go"}}, {leaf, api.MessageDispatch{Text: "go"}}} {
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

// slowStop opens the fake's agents, which play the first event of a turn
// and work on until interrupted; the interrupted turn ends only once
// release closes, so a stop lands late.
type slowStop struct {
	*fake.Adapter
	release chan struct{}
}

func (s slowStop) Open(ctx context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	g, err := s.Adapter.Open(ctx, spec)
	return &slowAgent{Agent: g, release: s.release, stop: make(chan struct{}), left: 1}, err
}

type slowAgent struct {
	provider.Agent
	release chan struct{}
	left    int
	once    sync.Once
	stop    chan struct{}
}

func (g *slowAgent) Next(ctx context.Context) (provider.Event, error) {
	if g.left == 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-g.stop:
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-g.release:
		}
	} else {
		g.left--
	}
	return g.Agent.Next(ctx)
}

func (g *slowAgent) Do(ctx context.Context, act provider.Action) error {
	if _, ok := act.(provider.Interrupt); ok {
		g.once.Do(func() { close(g.stop) })
	}
	return g.Agent.Do(ctx, act)
}

// settle runs what the manager posts, a frame after each batch, until ok
// holds or 5 s pass, then for a moment longer, so a turn started after
// would show.
func (h *harness) settle(ok func() bool) {
	for end := time.Now().Add(5 * time.Second); !ok() && time.Now().Before(end); {
		if h.p.run() {
			h.tt.Frame()
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	if h.p.run() {
		h.tt.Frame()
	}
}

// viewOn is the engine's view of a thread, and whether it's still there.
func viewOn(t *testing.T, d *daemon.Daemon, id api.ID) (api.Thread, bool) {
	t.Helper()
	snap, err := d.Endpoint().Read(context.Background(), api.Query{Thread: id})
	if errors.Is(err, api.ErrNotFound) {
		return api.Thread{}, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return snap.Threads[0], true
}

// Stop all, then Settle before the stops land: each child ends as its
// agent does, and the settled tree has nothing live.
func TestStopAllThenSettleLeavesNothingLive(t *testing.T) {
	dir := tempDir(t)
	release := make(chan struct{})
	d := engine(t, daemon.Config{Dir: dir}, slowStop{fake.New(midTurn), release})
	top := seed(t, d, "/work/t3code", "Ship the sidebar sort")
	kid := seedChild(t, d, top, "Write the tests")
	if _, err := d.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: kid, Body: api.MessageDispatch{Text: "go"}}); err != nil {
		t.Fatal(err)
	}
	h := start(t, dir)
	parent, child := h.thread("Ship the sidebar sort"), h.thread("Write the tests")
	h.until("the child working", func() bool { return child.View.Working() })
	h.tt.Frame()
	h.a.stopAll(h.a.shown(parent))
	h.until("the child stopping", func() bool { return child.View.Run == api.RunStopping })
	time.Sleep(300 * time.Millisecond)
	h.a.settleTree(parent)
	h.until("the child settled", func() bool { return child.View.Disposition.State == api.Settled })
	close(release)
	h.settle(func() bool { v, _ := viewOn(t, d, kid); return !v.Live() })
	if v, _ := viewOn(t, d, kid); v.Live() || v.Disposition.State != api.Settled {
		t.Errorf("the child in a settled tree: run %v, settled %v; want idle and settled", v.Run, v.Disposition.State == api.Settled)
	}
}

// Stop a lone thread, then delete it before the stop lands: it goes once
// its agent has stopped.
func TestStopThenDeleteALoneThread(t *testing.T) {
	dir := tempDir(t)
	release := make(chan struct{})
	d := engine(t, daemon.Config{Dir: dir}, slowStop{fake.New(midTurn), release})
	id := seed(t, d, "/work/t3code", "Ship the sidebar sort")
	if _, err := d.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: id, Body: api.MessageDispatch{Text: "go"}}); err != nil {
		t.Fatal(err)
	}
	h := start(t, dir)
	th := h.thread("Ship the sidebar sort")
	h.until("working", func() bool { return th.View.Working() })
	h.a.backend.Interrupt(th.env, th.id())
	h.until("stopping", func() bool { return th.View.Run == api.RunStopping })
	time.Sleep(300 * time.Millisecond)
	h.a.settings.confirmDelete = false
	h.a.askDelete(th)
	h.settle(func() bool { _, there := viewOn(t, d, id); return !there })
	close(release)
	h.settle(func() bool { _, there := viewOn(t, d, id); return !there })
	if v, there := viewOn(t, d, id); there {
		t.Errorf("the thread is still there: run %v", v.Run)
	}
}

// Deleting a tree deletes a child another client set to work after this
// GUI last heard of it: the GUI's view of the child is idle, the engine's
// is not, and neither thread is left, nor anything running.
func TestDeleteAChildStartedBeforeTheGUIHeard(t *testing.T) {
	dir := tempDir(t)
	var opens atomic.Int32
	ad := fake.New(midTurn)
	d := engine(t, daemon.Config{Dir: dir}, counting{stalled{ad, 1}, &opens})
	top := seed(t, d, "/work/t3code", "Ship the sidebar sort")
	kid := seedChild(t, d, top, "Write the tests")
	h := start(t, dir)
	parent, child := h.thread("Ship the sidebar sort"), h.thread("Write the tests")
	h.tt.Frame()
	if child.View.Live() {
		t.Fatal("the child works already")
	}
	run, err := d.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: kid, Body: api.MessageDispatch{Text: "go"}})
	if err != nil {
		t.Fatal(err)
	}
	for v, _ := viewOn(t, d, kid); v.Run != api.RunRunning; v, _ = viewOn(t, d, kid) {
		time.Sleep(5 * time.Millisecond)
	}
	h.a.settings.confirmDelete = false
	h.a.askDelete(parent)
	h.settle(func() bool { _, p := viewOn(t, d, top); _, k := viewOn(t, d, kid); return !p && !k })
	_, p := viewOn(t, d, top)
	v, k := viewOn(t, d, kid)
	if p || k {
		t.Errorf("parent there=%v; child there=%v, live=%v", p, k, v.Live())
	}
	rec, err := ad.Reconcile(context.Background(), provider.RuntimeIdentity{Thread: kid, Run: run.Command})
	if _, lost := rec.(provider.Lost); !lost || err != nil {
		t.Errorf("Delete left the child agent running: %T %v", rec, err)
	}
}

// The delete wins over another client's follow-ups: one waiting its turn
// behind the child's live run when the delete lands never starts, and one
// sent after is refused. The tree goes, and nothing of it runs on.
func TestADeleteWinsOverAnotherClientsFollowUps(t *testing.T) {
	dir := tempDir(t)
	var opens atomic.Int32
	ad := fake.New(midTurn)
	d := engine(t, daemon.Config{Dir: dir}, counting{stalled{ad, 1}, &opens})
	top := seed(t, d, "/work/t3code", "Ship the sidebar sort")
	kid := seedChild(t, d, top, "Write the tests")
	send := func(text string, mode api.DispatchMode) (api.Receipt, error) {
		return d.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: kid, Body: api.MessageDispatch{Text: text, Mode: mode}})
	}
	run, err := send("go", api.StartNow)
	if err != nil {
		t.Fatal(err)
	}
	h := start(t, dir)
	child := h.thread("Write the tests")
	h.until("the child working", func() bool { return child.View.Working() })
	if _, err := send("then this", api.AfterActive); err != nil {
		t.Fatal(err)
	}
	before := opens.Load()
	h.a.settings.confirmDelete = false
	h.a.askDelete(h.thread("Ship the sidebar sort"))
	h.settle(func() bool { _, p := viewOn(t, d, top); _, k := viewOn(t, d, kid); return !p && !k })
	if _, err := send("too late", api.StartNow); !errors.Is(err, api.ErrNotFound) {
		t.Errorf("a follow-up to the deleted child: %v, want %v", err, api.ErrNotFound)
	}
	_, p := viewOn(t, d, top)
	_, k := viewOn(t, d, kid)
	if n := opens.Load() - before; p || k || n != 0 {
		t.Errorf("starts=%d parent_exists=%v child_exists=%v", n, p, k)
	}
	rec, err := ad.Reconcile(context.Background(), provider.RuntimeIdentity{Thread: kid, Run: run.Command})
	if _, lost := rec.(provider.Lost); !lost || err != nil {
		t.Errorf("Delete left the child agent running: %T %v", rec, err)
	}
}

// A follow-up sent from the composer to a thread another client settled
// brings it back to the inbox and runs, whether this GUI had heard of the
// settle or not.
func TestAFollowUpBringsBackASettledThread(t *testing.T) {
	for _, heard := range []bool{false, true} {
		t.Run(fmt.Sprintf("heard %v", heard), func(t *testing.T) {
			dir := tempDir(t)
			ad := fake.New(fake.Turn(false))
			d := engine(t, daemon.Config{Dir: dir}, ad)
			id := seed(t, d, "/work/t3code", "Review follow-up")
			h := start(t, dir)
			th := h.thread("Review follow-up")
			h.a.select_(th)
			if _, err := d.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: id, Body: api.InboxSet{Disposition: api.Disposition{State: api.Settled}}}); err != nil {
				t.Fatal(err)
			}
			if heard {
				h.until("the settle", func() bool { return th.View.Disposition.State == api.Settled })
			}
			h.a.draft = "Please continue."
			h.a.send(th)
			h.until("the follow-up's turn over", func() bool { return ad.Starts() == 1 && len(th.Pending) == 0 && !th.View.Live() })
			if v, _ := viewOn(t, d, id); v.Disposition.State != api.Open {
				t.Errorf("the thread is %v after the follow-up, want back in the inbox", v.Disposition.State)
			}
		})
	}
}
