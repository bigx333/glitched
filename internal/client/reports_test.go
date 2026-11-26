package client

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"glitched/internal/api"
	eng "glitched/internal/engine"
	"glitched/internal/store"
)

// shelled is a manager fed a real store's shell: its snapshots and the
// views each commit moved, as the engine's shell stream sends them.
type shelled struct {
	t     *testing.T
	st    *store.Store
	m     *Manager
	e     *Env
	views []api.Thread // committed, not yet sent
	told  []api.ID     // the children Asked named
}

func newShelled(t *testing.T) *shelled {
	st, err := store.Open(filepath.Join(t.TempDir(), "host.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	sh := &shelled{t: t, st: st}
	sh.m = New(Config{Post: func(fn func()) { fn() }, Asked: func(_ *Env, child api.ID) { sh.told = append(sh.told, child) }})
	t.Cleanup(func() { sh.m.Close() })
	sh.e = &Env{ctx: sh.m.ctx, Threads: map[api.ID]*Thread{}}
	st.OnCommit(func(c store.Commit) { sh.views = append(sh.views, c.Threads...) })
	return sh
}

// connect sends a snapshot, as a new shell stream does.
func (sh *shelled) connect() {
	sh.t.Helper()
	snap, err := sh.st.Read(context.Background(), api.Query{})
	if err != nil {
		sh.t.Fatal(err)
	}
	sh.views = nil
	s := &session{ctx: sh.m.ctx}
	sh.m.shell(sh.e, s, api.Environment{}, api.StreamItem{Kind: api.StreamSnapshot, Snapshot: &snap})
}

// send sends the views committed since the last send or connect.
func (sh *shelled) send() {
	for _, v := range sh.views {
		sh.m.shell(sh.e, sh.e.session, api.Environment{}, api.StreamItem{Kind: api.StreamThread, Thread: &v})
	}
	sh.views = nil
}

func (sh *shelled) create(title string) api.ID {
	sh.t.Helper()
	id := api.NewID()
	if _, err := sh.st.Submit(context.Background(), api.Command{ID: api.NewID(), Target: id, Body: api.ThreadCreate{Title: title}}, time.Unix(1000, 0)); err != nil {
		sh.t.Fatal(err)
	}
	return id
}

// ask starts a turn on a new child of parent's that asks the user, and
// logs its Blocked report to parent, as the runtime does.
func (sh *shelled) ask(parent api.ID, at time.Time) api.ID {
	sh.t.Helper()
	child, report := sh.asking(parent, at)
	report()
	return child
}

// asking starts a turn on a new child of parent's that asks the user, a
// turn the user started, which takes the child over. report logs its
// Blocked report to parent, which the runtime does in a later commit.
func (sh *shelled) asking(parent api.ID, at time.Time) (child api.ID, report func()) {
	return sh.askingAs(parent, at, true)
}

// askingAs is asking, with the child's turn the user's, which takes it
// over, or its parent's, its create's task.
func (sh *shelled) askingAs(parent api.ID, at time.Time, user bool) (child api.ID, report func()) {
	sh.t.Helper()
	ctx := context.Background()
	child = api.NewID()
	create := api.Command{ID: api.NewID(), Target: child, Body: api.ThreadCreate{Workspace: "w", Title: "child", Model: api.ModelRef{Provider: "fake"}, Parent: parent}}
	run := create.ID
	if user {
		sh.spawn(parent, at, create)
		run = api.NewID()
		if _, err := sh.st.Submit(ctx, api.Command{ID: run, Target: child, Body: api.MessageDispatch{Text: "go", Mode: api.StartNow}}, at); err != nil {
			sh.t.Fatal(err)
		}
	} else {
		b := create.Body.(api.ThreadCreate)
		b.Text = "go"
		create.Body = b
		sh.spawn(parent, at, create)
	}
	e, ok, err := sh.st.Lease(ctx, func() []api.ID { return nil })
	for err == nil && ok && e.Thread != child { // another child's report
		e, ok, err = sh.st.Lease(ctx, func() []api.ID { return nil })
	}
	if err != nil || !ok {
		sh.t.Fatalf("lease: %+v %v %v", e, ok, err)
	}
	for _, o := range []eng.Observation{
		eng.Started{Run: run},
		eng.Surfaced{Run: run, Request: api.NewID(), Dedup: "ask", Kind: "approval", Body: []byte(`{}`)},
	} {
		if _, err := sh.st.Observe(ctx, child, o, at, e.ID); err != nil {
			sh.t.Fatal(err)
		}
		e.ID = 0
	}
	return child, func() {
		sh.t.Helper()
		sh.reported(parent, child, run, api.ReportBlocked, at)
	}
}

// finish creates a child of parent's that logs a Done report to it.
func (sh *shelled) finish(parent api.ID, at time.Time) {
	sh.t.Helper()
	child := api.NewID()
	sh.spawn(parent, at, api.Command{ID: api.NewID(), Target: child, Body: api.ThreadCreate{Workspace: "w", Title: "sibling", Model: api.ModelRef{Provider: "fake"}, Parent: parent}})
	sh.reported(parent, child, api.NewID(), api.ReportDone, at)
}

// spawn submits c, a child's create, from a turn of parent's that ends
// once it did, as only a parent's live run starts a child.
func (sh *shelled) spawn(parent api.ID, at time.Time, c api.Command) {
	sh.t.Helper()
	ctx := context.Background()
	run := api.NewID()
	if _, err := sh.st.Submit(ctx, api.Command{ID: run, Target: parent, Body: api.MessageDispatch{Text: "split it", Mode: api.StartNow}}, at); err != nil {
		sh.t.Fatal(err)
	}
	b := c.Body.(api.ThreadCreate)
	b.Run = run
	c.Body = b
	if _, err := sh.st.Submit(ctx, c, at); err != nil {
		sh.t.Fatal(err)
	}
	if _, err := sh.st.Observe(ctx, parent, eng.Ended{Run: run, Outcome: api.OutcomeCompleted}, at, 0); err != nil {
		sh.t.Fatal(err)
	}
}

func (sh *shelled) reported(parent, child, run api.ID, kind api.ReportKind, at time.Time) {
	sh.t.Helper()
	o := eng.Reported{Report: api.ChildReported{Child: child, Run: run, Kind: kind}}
	if _, err := sh.st.Observe(context.Background(), parent, o, at, 0); err != nil {
		sh.t.Fatal(err)
	}
}

func (sh *shelled) want(children ...api.ID) {
	sh.t.Helper()
	if len(sh.told) != len(children) {
		sh.t.Fatalf("told of %d children, %v; want %d", len(sh.told), sh.told, len(children))
	}
	for i, c := range children {
		if sh.told[i] != c {
			sh.t.Fatalf("ask %d told of %v; want %v", i, sh.told[i], c)
		}
	}
}

// The first snapshot tells of no ask, nor a reconnect after it: their
// reports came before the app did.
func TestTheFirstSnapshotTellsOfNoAsk(t *testing.T) {
	sh := newShelled(t)
	p := sh.create("Parent")
	sh.ask(p, time.Unix(1001, 0))
	sh.connect()
	sh.connect()
	sh.want()
}

// Two reports at one time are each told of once, and a reconnect to the
// same log tells of neither again, but of a child that asked while it was
// away.
func TestReportsAreToldOfOnceAcrossAReconnect(t *testing.T) {
	sh := newShelled(t)
	p := sh.create("Parent")
	sh.connect()
	at := time.Unix(1001, 0)
	a := sh.ask(p, at)
	b := sh.ask(p, at)
	sh.send()
	sh.want(a, b)
	sh.connect()
	sh.want(a, b)
	c := sh.ask(p, at)
	sh.connect()
	sh.want(a, b, c)
	sh.connect()
	sh.want(a, b, c)
}

// A store rewound for a restore keeps its views under a new log: the
// snapshot after tells of nothing, and the first report after is told of.
func TestTheFirstReportAfterARestoreIsToldOf(t *testing.T) {
	sh := newShelled(t)
	p := sh.create("Parent")
	sh.connect()
	a := sh.ask(p, time.Unix(1001, 0))
	sh.send()
	if err := sh.st.Rewind(context.Background()); err != nil {
		t.Fatal(err)
	}
	sh.connect()
	sh.want(a)
	b := sh.ask(p, time.Unix(1002, 0))
	sh.send()
	sh.want(a, b)
}

// A child that asks before the first snapshot and reports after it, while
// the shell is cut, is told of on the reconnect, though a sibling's Done
// report came after its own.
func TestAnAskFromBeforeTheFirstSnapshotIsToldOfThoughASiblingReportedAfter(t *testing.T) {
	sh := newShelled(t)
	p := sh.create("Parent")
	at := time.Unix(1001, 0)
	a, report := sh.asking(p, at)
	sh.connect()
	report()
	sh.finish(p, at)
	sh.connect()
	sh.want(a)
	sh.connect()
	sh.want(a)
}

// A child the user took over is told of as it asks. A duplicate that comes
// back starts over as on a first snapshot, and is told of the ask again by
// its report, which comes with a sibling's Done after.
func TestAnAskSeenByAReturningDuplicateIsToldOfThoughASiblingReportedAfter(t *testing.T) {
	sh := newShelled(t)
	p := sh.create("Parent")
	sh.connect()
	at := time.Unix(1001, 0)
	a, report := sh.asking(p, at)
	sh.send()
	sh.want(a)
	sh.m.down(sh.e, Duplicate{Of: "other"})
	sh.connect()
	report()
	sh.finish(p, at)
	sh.connect()
	sh.want(a, a)
	sh.connect()
	sh.want(a, a)
}

// A child that answers to its parent asks the parent, not the user: its
// ask is never told of, live or across a reconnect, while one the user
// took over is.
func TestAChildThatAnswersToItsParentIsNeverToldOf(t *testing.T) {
	sh := newShelled(t)
	p := sh.create("Parent")
	sh.connect()
	at := time.Unix(1001, 0)
	quiet, report := sh.askingAs(p, at, false)
	sh.send()
	report()
	sh.send()
	sh.want()
	if v := sh.e.Threads[quiet].View; v.TakenOver || v.UserOwned() || len(v.Requests) != 1 {
		t.Fatalf("the parent's child: TakenOver %v, UserOwned %v, requests %v; want its own, asking", v.TakenOver, v.UserOwned(), v.Requests)
	}
	taken := sh.ask(p, at)
	sh.send()
	sh.want(taken)
	sh.connect()
	sh.want(taken)
}
