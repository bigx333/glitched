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
	told  []api.Report
}

func newShelled(t *testing.T) *shelled {
	st, err := store.Open(filepath.Join(t.TempDir(), "host.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	sh := &shelled{t: t, st: st}
	sh.m = New(Config{Post: func(fn func()) { fn() }, Reported: func(_ *Env, _ api.ID, r api.Report) { sh.told = append(sh.told, r) }})
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

// report logs a Blocked report of a new child's to parent at at.
func (sh *shelled) report(parent api.ID, at time.Time) api.ID {
	sh.t.Helper()
	child := api.NewID()
	o := eng.Reported{Report: api.ChildReported{Child: child, Run: api.NewID(), Kind: api.ReportBlocked}}
	if _, err := sh.st.Observe(context.Background(), parent, o, at, 0); err != nil {
		sh.t.Fatal(err)
	}
	return child
}

func (sh *shelled) want(children ...api.ID) {
	sh.t.Helper()
	if len(sh.told) != len(children) {
		sh.t.Fatalf("told of %d reports, %+v; want %d", len(sh.told), sh.told, len(children))
	}
	for i, c := range children {
		if r := sh.told[i]; r.Child != c || r.Kind != api.ReportBlocked {
			sh.t.Fatalf("report %d told of %+v; want child %v blocked", i, r, c)
		}
	}
}

// The first snapshot tells of no report: they came before the app did.
func TestTheFirstSnapshotTellsOfNoReport(t *testing.T) {
	sh := newShelled(t)
	p := sh.create("Parent")
	sh.report(p, time.Unix(1001, 0))
	sh.connect()
	sh.want()
}

// Two reports at one time are each told of once, and a reconnect to the
// same log tells of neither again, but of one made while it was away.
func TestReportsAreToldOfOnceAcrossAReconnect(t *testing.T) {
	sh := newShelled(t)
	p := sh.create("Parent")
	sh.connect()
	at := time.Unix(1001, 0)
	a := sh.report(p, at)
	b := sh.report(p, at)
	sh.send()
	sh.want(a, b)
	sh.connect()
	sh.want(a, b)
	c := sh.report(p, at)
	sh.connect()
	sh.want(a, b, c)
}

// A store rewound for a restore keeps its views under a new log: the
// snapshot after tells of nothing, and the first report after is told of.
func TestTheFirstReportAfterARestoreIsToldOf(t *testing.T) {
	sh := newShelled(t)
	p := sh.create("Parent")
	sh.connect()
	a := sh.report(p, time.Unix(1001, 0))
	sh.send()
	if err := sh.st.Rewind(context.Background()); err != nil {
		t.Fatal(err)
	}
	sh.connect()
	sh.want(a)
	b := sh.report(p, time.Unix(1002, 0))
	sh.send()
	sh.want(a, b)
}
