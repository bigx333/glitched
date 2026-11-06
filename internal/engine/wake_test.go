package engine_test

import (
	"errors"
	"strings"
	"testing"

	"glitched/internal/api"
	"glitched/internal/engine"
)

// see observes o on f and returns the effects it asked for.
func (f *thread) see(o engine.Observation) []engine.Effect {
	f.t.Helper()
	evs, effects, err := engine.Observe(f.th, o, now)
	if err != nil {
		f.t.Fatal(err)
	}
	f.fold(evs)
	return effects
}

// started is the turn effects start, if they start one.
func started(effects []engine.Effect) (engine.StartTurn, bool) {
	for _, e := range effects {
		if s, ok := e.(engine.StartTurn); ok {
			return s, true
		}
	}
	return engine.StartTurn{}, false
}

func report(kind api.ReportKind, title, outcome string) engine.Reported {
	return engine.Reported{Report: api.ChildReported{Child: api.NewID(), Run: api.NewID(), Kind: kind, Title: title, Outcome: outcome}}
}

// A child's done or failed report starts its parent's next turn, with the
// child's result in the turn's text: at once on an idle parent, after the
// turn at work on a busy one. A blocked report wakes nothing.
func TestAReportWakesTheParent(t *testing.T) {
	idle := newThread(t)
	s, ok := started(idle.see(report(api.ReportDone, "Write the tests", "Added 9 table tests.")))
	if !ok || !strings.Contains(s.Text, "Write the tests") || !strings.Contains(s.Text, "Added 9 table tests.") || idle.th.Live() == nil {
		t.Errorf("a done report to an idle parent: %+v, %v; want a turn that names the child and says what it did", s, ok)
	}

	failed := newThread(t)
	if s, ok := started(failed.see(report(api.ReportFailed, "Write the tests", "Claude Code exited: usage limit"))); !ok ||
		!strings.Contains(s.Text, "failed") || !strings.Contains(s.Text, "usage limit") {
		t.Errorf("a failed report: %+v, %v; want a turn that says the child failed and why", s, ok)
	}

	blocked := newThread(t)
	if s, ok := started(blocked.see(report(api.ReportBlocked, "Write the tests", "Wants to run go test ./..."))); ok {
		t.Errorf("a blocked report started %+v; the child waits on the user, not the parent", s)
	}

	busy, run := running(t)
	if s, ok := started(busy.see(report(api.ReportDone, "Write the tests", "Added 9 table tests."))); ok {
		t.Errorf("a report to a busy parent started %+v at once; want it after the turn at work", s)
	}
	s, ok = started(busy.see(engine.Ended{Run: run, Outcome: api.OutcomeCompleted}))
	if !ok || !strings.Contains(s.Text, "Added 9 table tests.") {
		t.Errorf("the busy parent's turn ended: %+v, %v; want the report's turn next", s, ok)
	}
	busy.see(engine.Started{Run: s.Run})
	for _, m := range busy.th.Queue {
		if m.State != engine.QueueDispatched {
			t.Errorf("message %v is %v once the report's turn started; want every one dispatched", m.ID, m.State)
		}
	}
}

// A settled parent takes the report and no turn, and nothing it held
// starts once it is back in the inbox; a deleted one takes nothing. An
// open one, the same report, is woken.
func TestASettledOrDeletedParentIsNotWoken(t *testing.T) {
	r := report(api.ReportDone, "Write the tests", "Added 9 table tests.")
	open := newThread(t)
	if _, ok := started(open.see(r)); !ok {
		t.Error("an open parent was not woken")
	}

	settled := newThread(t)
	settled.must(api.InboxSet{Disposition: api.Disposition{State: api.Settled}})
	if s, ok := started(settled.see(r)); ok || settled.th.Live() != nil {
		t.Errorf("a settled parent started %+v", s)
	}

	held, run := running(t)
	held.see(report(api.ReportDone, "Write the tests", "Added 9 table tests."))
	held.must(api.InboxSet{Disposition: api.Disposition{State: api.Settled}})
	// It completes as the stop lands, so only the settle holds the report.
	if s, ok := started(held.see(engine.Ended{Run: run, Outcome: api.OutcomeCompleted})); ok {
		t.Errorf("a parent settled with a report waiting started %+v", s)
	}
	_, effects, err := held.submit(api.InboxSet{Disposition: api.Disposition{}})
	if s, ok := started(effects); err != nil || ok {
		t.Errorf("back in the inbox (%v): started %+v; want nothing", err, s)
	}
	_, effects, _ = held.submit(dispatch("carry on", api.StartNow))
	if s, ok := started(effects); !ok || s.Text != "carry on" {
		t.Errorf("a message after the settle: %+v, %v; want the user's words alone", s, ok)
	}

	deleted := newThread(t)
	deleted.must(api.ThreadDelete{})
	if _, _, err := engine.Observe(deleted.th, r, now); !errors.Is(err, engine.ErrRefused) {
		t.Errorf("a report to a deleted parent: %v, want it refused", err)
	}
}

// A parent the user stopped holds its children's reports, whether they
// came while it stopped or after: a stop stays a stop. They go in with the
// next message the user sends, before the user's words, in that one turn.
func TestAStoppedParentHoldsItsReports(t *testing.T) {
	p, run := running(t)
	p.must(api.TurnInterrupt{})
	if s, ok := started(p.see(report(api.ReportDone, "Write the tests", "Added 9 table tests."))); ok {
		t.Errorf("a report to a stopping parent started %+v", s)
	}
	if s, ok := started(p.see(engine.Ended{Run: run, Outcome: api.OutcomeInterrupted})); ok {
		t.Errorf("the stopped run's end started %+v", s)
	}
	if s, ok := started(p.see(report(api.ReportFailed, "Check the callers", "Claude Code exited: usage limit"))); ok {
		t.Errorf("a report to a stopped parent started %+v", s)
	}
	if p.th.Live() != nil {
		t.Fatal("the stopped parent is at work")
	}

	_, effects, err := p.submit(dispatch("carry on", api.StartNow))
	s, ok := started(effects)
	if err != nil || !ok {
		t.Fatalf("the user's next message: %v, %v; want it to start", err, ok)
	}
	tests, callers, words := strings.Index(s.Text, "Added 9 table tests."), strings.Index(s.Text, "usage limit"), strings.Index(s.Text, "carry on")
	if tests < 0 || callers < 0 || words < max(tests, callers) {
		t.Errorf("the turn's text is %q; want both reports, then the user's words", s.Text)
	}
	p.see(engine.Started{Run: s.Run})
	if s, ok := started(p.see(engine.Ended{Run: s.Run, Outcome: api.OutcomeCompleted})); ok {
		t.Errorf("the reports went in with the user's turn, and its end started another: %+v", s)
	}
}

// A report that comes again, as a replayed effect's does, wakes the parent
// once and logs once.
func TestAReplayedReportWakesOnce(t *testing.T) {
	p := newThread(t)
	r := report(api.ReportDone, "Write the tests", "Added 9 table tests.")
	s, ok := started(p.see(r))
	if !ok {
		t.Fatal("the report did not wake the parent")
	}
	evs, effects, err := engine.Observe(p.th, r, now)
	if err != nil || len(evs) != 0 || len(effects) != 0 {
		t.Errorf("the report again: %d events, %d effects, %v; want nothing", len(evs), len(effects), err)
	}
	p.fold(evs)
	p.see(engine.Started{Run: s.Run})
	if s, ok := started(p.see(engine.Ended{Run: s.Run, Outcome: api.OutcomeCompleted})); ok {
		t.Errorf("the wake's turn ended and started %+v", s)
	}
}
