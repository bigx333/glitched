package engine_test

import (
	"errors"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
)

func (f *thread) state(id api.ID) engine.QueueState {
	f.t.Helper()
	m, ok := f.th.Message(id)
	if !ok {
		f.t.Fatalf("no message %v", id)
	}
	return m.State
}

// A stop goes quiet: what waits its turn is held when the stop is asked,
// and nothing starts once the run ends, until the user resumes the queue,
// which starts its messages in the order they were sent.
func TestAStopHoldsTheQueueUntilResumed(t *testing.T) {
	f, run := running(t)
	first := f.must(dispatch("first", api.AfterActive))
	second := f.must(dispatch("second", api.AfterActive))
	f.must(api.TurnInterrupt{})
	if f.state(first) != engine.QueueHeld || f.state(second) != engine.QueueHeld {
		t.Fatalf("after the stop the queue is %v, %v; want both held", f.state(first), f.state(second))
	}
	if s, ok := started(f.see(engine.Ended{Run: run, Outcome: api.OutcomeInterrupted})); ok || f.th.Live() != nil {
		t.Fatalf("the stopped run ended and started %+v; want the thread quiet", s)
	}
	if v := engine.View(f.th); v.Queued != 2 {
		t.Fatalf("view shows %d queued, want the 2 held", v.Queued)
	}

	_, effects, err := f.submit(api.QueueResume{})
	if s, ok := started(effects); err != nil || !ok || s.Run != first || s.Text != "first" {
		t.Fatalf("resume: %+v, %v, %v; want the first message's turn", s, ok, err)
	}
	if f.state(second) != engine.QueueReady {
		t.Fatalf("after the resume the second message is %v, want ready behind the first", f.state(second))
	}
	if s, ok := started(f.see(engine.Ended{Run: first, Outcome: api.OutcomeCompleted})); !ok || s.Run != second {
		t.Fatalf("the first resumed turn ended and started %+v, %v; want the second's", s, ok)
	}
}

// Sending again is the user's go-ahead: what the stop held starts before
// the new message, even sent while the run still stops. A Defer is not
// one, and stays held with the rest.
func TestASendAfterAStopResumesTheQueue(t *testing.T) {
	f, run := running(t)
	held := f.must(dispatch("held", api.AfterActive))
	f.must(api.TurnInterrupt{})
	deferred := f.must(dispatch("later", api.Defer))
	if f.state(held) != engine.QueueHeld || f.state(deferred) != engine.QueueHeld {
		t.Fatalf("a Defer after the stop left the queue %v, %v; want both held", f.state(held), f.state(deferred))
	}
	sent := f.must(dispatch("then this", api.AfterActive))
	if f.state(held) != engine.QueueReady || f.state(deferred) != engine.QueueReady {
		t.Fatalf("a send while the run stops left the queue %v, %v; want both released", f.state(held), f.state(deferred))
	}
	s, ok := started(f.see(engine.Ended{Run: run, Outcome: api.OutcomeInterrupted}))
	if !ok || s.Run != held {
		t.Fatalf("the stopped run ended and started %+v, %v; want the held message first", s, ok)
	}
	f.see(engine.Ended{Run: held, Outcome: api.OutcomeCompleted})
	if s, ok := started(f.see(engine.Ended{Run: deferred, Outcome: api.OutcomeCompleted})); !ok || s.Run != sent {
		t.Fatalf("after the released messages the next turn is %+v, %v; want the one sent", s, ok)
	}
}

// Ending an uncertain run is a stop too: the message queued behind it is
// held, not started.
func TestEndingAnUncertainRunHoldsTheQueue(t *testing.T) {
	f, run := running(t)
	f.observe(engine.Reconciled{Run: run})
	retry := f.must(dispatch("retry", api.AfterActive))
	_, effects, err := f.submit(api.TurnInterrupt{})
	if s, ok := started(effects); err != nil || ok || f.th.Live() != nil {
		t.Fatalf("stop on an uncertain run: %+v, %v, %v; want it ended and nothing started", s, ok, err)
	}
	if f.state(retry) != engine.QueueHeld {
		t.Fatalf("the retry is %v, want held", f.state(retry))
	}
	if _, effects, _ := f.submit(api.QueueResume{}); len(effects) != 1 {
		t.Fatalf("resume: %v; want the retry's turn", effects)
	}
}

// A wake that came during a stop waits with the queue, and resuming
// starts it when no message waits. A second resume, its turn at work, is
// refused and changes nothing.
func TestAResumeStartsTheWakesAStopHeld(t *testing.T) {
	f, run := running(t)
	f.must(api.TurnInterrupt{})
	f.see(report(api.ReportDone, "Write the tests", "Added 9 table tests."))
	if s, ok := started(f.see(engine.Ended{Run: run, Outcome: api.OutcomeInterrupted})); ok {
		t.Fatalf("the stopped run ended and the wake started %+v; want it held", s)
	}
	_, effects, err := f.submit(api.QueueResume{})
	if s, ok := started(effects); err != nil || !ok || f.th.Live() == nil {
		t.Fatalf("resume with a wake waiting: %+v, %v, %v; want the wake's turn", s, ok, err)
	}
	before := f.th.Revision
	evs, effects, err := engine.Decide(f.th, api.Command{ID: api.NewID(), Target: f.th.ID, Body: api.QueueResume{}}, now)
	if rej := (*api.RejectedError)(nil); !errors.As(err, &rej) || len(evs) != 0 || len(effects) != 0 || f.th.Revision != before {
		t.Fatalf("a second resume: %v, %v, %v; want it refused with nothing recorded", evs, effects, err)
	}
}

// A start now on a busy thread stops nothing: it goes first in the
// queue, ahead of what was queued before it, and starts when the run ends.
func TestStartNowOnABusyThreadGoesFirst(t *testing.T) {
	f, run := running(t)
	queued := f.must(dispatch("queued", api.AfterActive))
	_, effects, err := f.submit(dispatch("now", api.StartNow))
	if err != nil || len(effects) != 0 || f.th.Live().ID != run {
		t.Fatalf("start now on a busy thread: %v, %v; want it queued and the run left alone", effects, err)
	}
	s, ok := started(f.see(engine.Ended{Run: run, Outcome: api.OutcomeCompleted}))
	if !ok || s.Text != "now" || f.state(queued) != engine.QueueReady {
		t.Fatalf("the run ended and started %+v, %v; want the start now first, the queued message after", s, ok)
	}
}

// A run that ends while it stops was stopped, even when its end beats
// word that its interrupt reached it: the wake a child sent waits.
func TestARunThatEndsWhileStoppingIsStopped(t *testing.T) {
	f, run := running(t)
	f.see(report(api.ReportDone, "child", "result"))
	f.must(api.TurnInterrupt{})
	if s, ok := started(f.see(engine.Ended{Run: run, Outcome: api.OutcomeCompleted})); ok {
		t.Fatalf("a run that completed before its interrupt landed started the wake %q; want it held", s.Text)
	}
	if !engine.View(f.th).Resumable {
		t.Fatal("the held wake is not resumable")
	}
	if late := f.see(engine.InterruptSent{Run: run}); len(late) != 0 {
		t.Fatalf("the interrupt's late word did something: %v", late)
	}
}

// Resume is offered only when it starts something: on an idle thread
// holding what a stop held, or only a wake. While the run still stops, a
// resume with only a wake waiting is refused, not dropped, so the user
// resumes once it ends.
func TestResumeIsOfferedWhenItStartsSomething(t *testing.T) {
	f, run := running(t)
	if engine.View(f.th).Resumable {
		t.Fatal("a running thread is resumable")
	}
	f.must(api.TurnInterrupt{})
	f.see(report(api.ReportDone, "Write the tests", "Added 9 table tests."))
	if _, _, err := f.submit(api.QueueResume{}); err == nil {
		t.Fatal("a resume while the run stops, with only a wake waiting, was taken; want it refused")
	}
	if engine.View(f.th).Resumable {
		t.Fatal("a stopping thread is resumable")
	}
	f.see(engine.Ended{Run: run, Outcome: api.OutcomeInterrupted})
	if !engine.View(f.th).Resumable {
		t.Fatal("a stopped thread with a wake waiting is not resumable")
	}
	f.must(api.QueueResume{})
	if engine.View(f.th).Resumable {
		t.Fatal("a thread whose wake started is still resumable")
	}

	g := newThread(t)
	g.must(dispatch("later", api.Defer))
	if !engine.View(g.th).Resumable {
		t.Fatal("an idle thread holding a Defer is not resumable")
	}
	g.must(api.ThreadDelete{})
	if engine.View(g.th).Resumable {
		t.Fatal("a deleted thread is resumable")
	}
}

// A restore cancels what a stop held on an idle thread too: it may have
// run, after a resume, in the history the restore lost.
func TestARewindCancelsWhatAStopHeld(t *testing.T) {
	f, run := running(t)
	held := f.must(dispatch("held", api.AfterActive))
	f.must(api.TurnInterrupt{})
	f.see(engine.Ended{Run: run, Outcome: api.OutcomeInterrupted})
	f.see(engine.Rewound{})
	if f.state(held) != engine.QueueCancelled || engine.View(f.th).Resumable {
		t.Fatalf("after the rewind the held message is %v, want cancelled and nothing to resume", f.state(held))
	}
}

// A stop of a silent run, as a CLI update does to a compaction, holds
// nothing: the user's message and a wake waited behind it, not on it,
// and start once it ends.
func TestStoppingACompactionHoldsNothing(t *testing.T) {
	base := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	th, last := makeIdleThread("claude", "claude-opus-4-6", "", 150_000, base, base.Add(time.Hour))
	compacting := func() (*thread, api.ID) {
		f, run := &thread{t: t, th: th}, api.NewID()
		evs, _, err := engine.Observe(f.th, engine.IdleCompact{Run: run, AfterRun: last}, base.Add(54*time.Minute+30*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		f.fold(evs)
		f.see(engine.Started{Run: run})
		f.see(report(api.ReportDone, "Write the tests", "Added 9 table tests."))
		return f, run
	}

	f, run := compacting()
	msg := f.must(dispatch("next", api.StartNow))
	f.must(api.TurnInterrupt{})
	if f.state(msg) != engine.QueueReady {
		t.Fatalf("stopping the compaction left the message %v, want ready", f.state(msg))
	}
	if s, ok := started(f.see(engine.Ended{Run: run, Outcome: api.OutcomeInterrupted})); !ok || s.Run != msg {
		t.Fatalf("the stopped compaction ended and started %+v, %v; want the message's turn", s, ok)
	}

	g, run := compacting()
	g.must(api.TurnInterrupt{})
	if _, ok := started(g.see(engine.Ended{Run: run, Outcome: api.OutcomeInterrupted})); !ok {
		t.Fatal("the stopped compaction ended and the wake waited; want the wake's turn")
	}
}

// steering is a running turn the user steered with "hurry", which the
// agent was sent, and then stopped.
func steering(t *testing.T) (*thread, api.ID, api.ID) {
	f, run := running(t)
	steer := f.must(dispatch("hurry", api.SteerActive))
	f.observe(engine.Steered{Message: steer})
	f.must(api.TurnInterrupt{})
	return f, run, steer
}

func cancelled(evs []engine.Event) []api.MessageCancelled {
	var out []api.MessageCancelled
	for _, e := range evs {
		if c, ok := e.Body.(api.MessageCancelled); ok {
			out = append(out, c)
		}
	}
	return out
}

func endedAt(evs []engine.Event) (int, api.TurnEnded) {
	for i, e := range evs {
		if b, ok := e.Body.(api.TurnEnded); ok {
			return i, b
		}
	}
	return -1, api.TurnEnded{}
}

// A steer the agent was sent but never read, as the stop came first, is
// cancelled with the turn's end, in the same decision, and says why.
func TestAStopCancelsTheSteerTheAgentNeverTook(t *testing.T) {
	f, run, steer := steering(t)
	evs := f.observe(engine.Ended{Run: run, Outcome: api.OutcomeInterrupted, Untaken: []api.ID{steer}})
	if f.state(steer) != engine.QueueCancelled {
		t.Fatalf("the untaken steer is %v, want cancelled", f.state(steer))
	}
	got := cancelled(evs)
	if len(got) != 1 || got[0].Message != steer || got[0].Reason != "the stop came before the agent read it" {
		t.Fatalf("message.cancelled %+v; want the steer's, saying the stop came first", got)
	}
	i, ended := endedAt(evs)
	if i < 0 || ended.Outcome != api.OutcomeInterrupted {
		t.Fatalf("the turn ended %+v at %d, want interrupted in the same decision", ended, i)
	}
}

// Only the steers the agent never took are cancelled, each once.
func TestAStopCancelsOnlyTheSteersNamedUntaken(t *testing.T) {
	f, run, taken := steering(t)
	if evs := f.observe(engine.Ended{Run: run, Outcome: api.OutcomeInterrupted}); len(cancelled(evs)) != 0 || f.state(taken) != engine.QueueDispatched {
		t.Fatalf("a stop with no untaken steers cancelled %+v and left the steer %v; want none, dispatched", cancelled(evs), f.state(taken))
	}

	f, run = running(t)
	taken = f.must(dispatch("one", api.SteerActive))
	f.observe(engine.Steered{Message: taken})
	untaken := f.must(dispatch("two", api.SteerActive))
	f.observe(engine.Steered{Message: untaken})
	f.must(api.TurnInterrupt{})
	evs := f.observe(engine.Ended{Run: run, Outcome: api.OutcomeInterrupted, Untaken: []api.ID{untaken, untaken}})
	if got := cancelled(evs); len(got) != 1 || got[0].Message != untaken {
		t.Fatalf("message.cancelled %+v; want the second steer's once", got)
	}
	if f.state(taken) != engine.QueueDispatched || f.state(untaken) != engine.QueueCancelled {
		t.Fatalf("steers %v and %v; want the taken one dispatched, the untaken cancelled", f.state(taken), f.state(untaken))
	}
}

// An untaken id that isn't a steer this run was sent is no one's to
// cancel: unknown, another run's, the run's own prompt, or one not sent.
func TestUntakenNamesOnlyTheRunsSentSteers(t *testing.T) {
	f, first := running(t)
	older := f.must(dispatch("older", api.SteerActive))
	f.observe(engine.Steered{Message: older})
	lost := f.must(dispatch("lost", api.SteerActive))
	f.observe(engine.Ended{Run: first, Outcome: api.OutcomeCompleted})
	run := f.must(dispatch("again", api.StartNow))
	f.observe(engine.Started{Run: run})
	claimed := f.must(dispatch("claimed", api.SteerActive))
	f.must(api.TurnInterrupt{})

	evs := f.observe(engine.Ended{Run: run, Outcome: api.OutcomeInterrupted, Untaken: []api.ID{api.NewID(), older, run, claimed, lost}})
	if got := cancelled(evs); len(got) != 0 {
		t.Fatalf("message.cancelled %+v; want none", got)
	}
	if i, ended := endedAt(evs); i < 0 || ended.Outcome != api.OutcomeInterrupted {
		t.Fatalf("the turn ended %+v at %d, want interrupted", ended, i)
	}
	if f.state(older) != engine.QueueDispatched || f.state(run) != engine.QueueDispatched {
		t.Fatalf("the other run's steer is %v and the prompt %v; want both dispatched", f.state(older), f.state(run))
	}
}

// A message that went in with the run's prompt is no steer, so an
// adapter that names it untaken cancels nothing.
func TestUntakenNamesNoMessageThatWentInWithThePrompt(t *testing.T) {
	f, first := running(t)
	f.observe(report(api.ReportDone, "c", ""))
	prompt := f.must(dispatch("next", api.AfterActive))
	f.observe(engine.Ended{Run: first, Outcome: api.OutcomeCompleted})
	f.observe(engine.Started{Run: prompt})
	var along api.ID
	for _, m := range f.th.Queue {
		if m.Run == prompt && m.ID != prompt {
			along = m.ID
		}
	}
	if along.IsZero() || f.state(along) != engine.QueueDispatched {
		t.Fatalf("the wake that went in with the prompt is %v; want dispatched", f.state(along))
	}
	f.must(api.TurnInterrupt{})
	evs := f.observe(engine.Ended{Run: prompt, Outcome: api.OutcomeInterrupted, Untaken: []api.ID{along}})
	if got := cancelled(evs); len(got) != 0 || f.state(along) != engine.QueueDispatched {
		t.Fatalf("message.cancelled %+v and the wake %v; want none, dispatched", got, f.state(along))
	}
}

// Only the user ends an uncertain run, so its untaken steers stay as
// they are.
func TestAnUncertainRunCancelsNoUntakenSteer(t *testing.T) {
	f, run := running(t)
	steer := f.must(dispatch("hurry", api.SteerActive))
	f.observe(engine.Steered{Message: steer})
	f.observe(engine.Reconciled{Run: run})
	if evs := f.observe(engine.Ended{Run: run, Outcome: api.OutcomeInterrupted, Untaken: []api.ID{steer}}); len(evs) != 0 || f.state(steer) != engine.QueueDispatched {
		t.Fatalf("an uncertain run's end recorded %v and left the steer %v; want nothing, dispatched", evs, f.state(steer))
	}
}

// A steer's failure that comes after the agent was sent it cancels
// nothing: the agent has it.
func TestALateSteerFailureLeavesTheSentSteer(t *testing.T) {
	f, _ := running(t)
	steer := f.must(dispatch("hurry", api.SteerActive))
	f.observe(engine.Steered{Message: steer})
	evs, _, err := engine.Observe(f.th, engine.SteerFailed{Message: steer, Error: "boom"}, now)
	if len(evs) != 0 || !errors.Is(err, engine.ErrRefused) || f.state(steer) != engine.QueueDispatched {
		t.Fatalf("a late steer failure: %v, %v, steer %v; want nothing recorded, engine.ErrRefused, dispatched", evs, err, f.state(steer))
	}
}

// A run that ends on its own, not stopped, cancels no steer its adapter
// names untaken: the agent still holds it and runs it next.
func TestAnUnstoppedEndCancelsNoUntakenSteer(t *testing.T) {
	for name, outcome := range map[string]api.Outcome{"completed": api.OutcomeCompleted, "failed": api.OutcomeFailed} {
		t.Run(name, func(t *testing.T) {
			f, run := running(t)
			steer := f.must(dispatch("hurry", api.SteerActive))
			f.observe(engine.Steered{Message: steer})
			evs := f.observe(engine.Ended{Run: run, Outcome: outcome, Untaken: []api.ID{steer}})
			if got := cancelled(evs); len(got) != 0 || f.state(steer) != engine.QueueDispatched {
				t.Fatalf("the end cancelled %+v and left the steer %v; want none, dispatched", got, f.state(steer))
			}
			if i, ended := endedAt(evs); i < 0 || ended.Outcome != outcome {
				t.Fatalf("the turn ended %+v at %d, want %v", ended, i, outcome)
			}
		})
	}
}
