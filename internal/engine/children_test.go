package engine_test

import (
	"encoding/json"
	"errors"
	"testing"

	"glitched/internal/api"
	"glitched/internal/engine"
)

// child is a thread created under parent.
func child(t *testing.T, parent engine.Thread) *thread {
	t.Helper()
	evs, _, err := engine.Decide(engine.Thread{}, api.Command{ID: api.NewID(), Target: api.NewID(),
		Body: api.ThreadCreate{Title: "child", Access: parent.Access, Parent: parent.ID}}, now)
	if err != nil {
		t.Fatal(err)
	}
	f := &thread{t: t}
	f.fold(evs)
	return f
}

// A child is created under a parent that's there, no deeper than MaxDepth
// below a thread of the user's, and no wider than its parent.
func TestCheckChild(t *testing.T) {
	top := newThread(t).th
	top.Access = api.AccessAutoEdits
	line := []engine.Thread{top}
	if err := engine.CheckChild(line, api.ThreadCreate{Parent: top.ID, Access: api.AccessAsk}); err != nil {
		t.Errorf("a narrower child: %v", err)
	}
	refused := func(what string, line []engine.Thread, c api.ThreadCreate) {
		t.Helper()
		var rej *api.RejectedError
		if err := engine.CheckChild(line, c); !errors.As(err, &rej) {
			t.Errorf("%s: %v, want a rejection", what, err)
		}
	}
	refused("a wider child", line, api.ThreadCreate{Parent: top.ID, Access: api.AccessFull})
	refused("no parent", nil, api.ThreadCreate{Parent: api.NewID()})
	gone := top
	gone.Deleted = true
	refused("a deleted parent", []engine.Thread{gone}, api.ThreadCreate{Parent: top.ID})
	deep := make([]engine.Thread, api.MaxDepth+1)
	for i := range deep {
		deep[i] = top
	}
	if err := engine.CheckChild(deep[:api.MaxDepth], api.ThreadCreate{Parent: top.ID}); err != nil {
		t.Errorf("a child at depth %d: %v", api.MaxDepth, err)
	}
	refused("a child below MaxDepth", deep, api.ThreadCreate{Parent: top.ID})
}

// A child's run reports to its parent as it completes, fails or blocks on
// the user, with the child's title; one the user stopped reports nothing,
// and a thread of the user's never reports.
func TestAChildReportsToItsParent(t *testing.T) {
	parent := newThread(t)
	reportOf := func(effects []engine.Effect) (engine.ReportToParent, bool) {
		for _, e := range effects {
			if r, ok := e.(engine.ReportToParent); ok {
				return r, true
			}
		}
		return engine.ReportToParent{}, false
	}
	start := func(f *thread) api.ID {
		run := f.must(dispatch("go", api.StartNow))
		f.observe(engine.Started{Run: run})
		return run
	}
	end := func(f *thread, o engine.Observation) []engine.Effect {
		t.Helper()
		evs, effects, err := engine.Observe(f.th, o, now)
		if err != nil {
			t.Fatal(err)
		}
		f.fold(evs)
		return effects
	}

	c := child(t, parent.th)
	run := start(c)
	r, ok := reportOf(end(c, engine.Ended{Run: run, Outcome: api.OutcomeCompleted}))
	if !ok || r.Kind != api.ReportDone || r.Parent != parent.th.ID || r.Child != c.th.ID || r.Run != run || r.Title != "child" {
		t.Errorf("completed: %+v, %v; want a done report to the parent", r, ok)
	}

	run = start(c)
	if r, ok := reportOf(end(c, engine.Ended{Run: run, Outcome: api.OutcomeFailed, Error: "boom"})); !ok || r.Kind != api.ReportFailed || r.Error != "boom" {
		t.Errorf("failed: %+v, %v; want a failed report saying why", r, ok)
	}

	run = start(c)
	ask := json.RawMessage(`{"operation":"edit","path":"main.go"}`)
	r, ok = reportOf(end(c, engine.Surfaced{Run: run, Request: api.NewID(), Dedup: "r", Kind: "approval", Body: ask}))
	if !ok || r.Kind != api.ReportBlocked || string(r.Ask) != string(ask) {
		t.Errorf("blocked: %+v, %v; want a blocked report with the ask", r, ok)
	}
	c.must(api.TurnInterrupt{})
	if _, ok := reportOf(end(c, engine.Ended{Run: run, Outcome: api.OutcomeInterrupted})); ok {
		t.Error("a run the user stopped reported")
	}

	run = start(parent)
	if _, ok := reportOf(end(parent, engine.Ended{Run: run, Outcome: api.OutcomeCompleted})); ok {
		t.Error("a thread of the user's reported")
	}
}

// A report lands in the parent's log, marks when, and brings a settled or
// snoozed parent back to the inbox.
func TestAReportWakesItsParent(t *testing.T) {
	for _, state := range []api.DispositionState{api.Settled, api.Snoozed} {
		p := newThread(t)
		d := api.Disposition{State: state}
		if state == api.Snoozed {
			d.Until = now.Add(24 * 3600e9)
		}
		p.must(api.InboxSet{Disposition: d})
		rev := p.th.Revision
		p.observe(engine.Reported{Report: api.ChildReported{Child: api.NewID(), Run: api.NewID(), Kind: api.ReportDone, Title: "c"}})
		want := api.Open
		if got := p.th.Disposition.State; got != want || !p.th.Reported.Equal(now) || p.th.Revision == rev {
			t.Errorf("a report to a parent %v: disposition %v, reported %v, revision %d→%d; want %v, now, moved",
				state, got, p.th.Reported, rev, p.th.Revision, want)
		}
	}
}

// A thread's tokens are its runs', added up as they end.
func TestTokensAddUp(t *testing.T) {
	f := newThread(t)
	for range 2 {
		run := f.must(dispatch("go", api.StartNow))
		f.observe(engine.Started{Run: run})
		f.observe(engine.Ended{Run: run, Outcome: api.OutcomeCompleted, Tokens: 1200})
	}
	if v := engine.View(f.th); v.Tokens != 2400 {
		t.Errorf("tokens %d after two runs of 1200, want 2400", v.Tokens)
	}
}

// A run being stopped reports nothing, even one that completes as the stop
// reaches it.
func TestAStoppedRunReportsNothing(t *testing.T) {
	c := child(t, newThread(t).th)
	run := c.must(dispatch("go", api.StartNow))
	c.observe(engine.Started{Run: run})
	c.must(api.TurnInterrupt{})
	_, effects, err := engine.Observe(c.th, engine.Ended{Run: run, Outcome: api.OutcomeCompleted}, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range effects {
		if _, ok := e.(engine.ReportToParent); ok {
			t.Error("a run being stopped reported")
		}
	}
}
