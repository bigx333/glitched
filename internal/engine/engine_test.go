package engine_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
)

var now = time.Unix(1_700_000_000, 0).UTC()

type thread struct {
	t  *testing.T
	th engine.Thread
}

func (f *thread) fold(evs []engine.Event) []engine.Event {
	for _, e := range evs {
		f.th = engine.Apply(f.th, e)
	}
	return evs
}

// submit decides body as a client command; the command's ID is returned
// because a dispatched message and its run take it.
func (f *thread) submit(body api.Intent) (api.ID, []engine.Effect, error) {
	f.t.Helper()
	id := api.NewID()
	evs, effects, err := engine.Decide(f.th, api.Command{ID: id, Target: f.th.ID, Body: body}, now)
	f.fold(evs)
	return id, effects, err
}

func (f *thread) must(body api.Intent) api.ID {
	f.t.Helper()
	id, _, err := f.submit(body)
	if err != nil {
		f.t.Fatalf("%s: %v", api.IntentType(body), err)
	}
	return id
}

func (f *thread) observe(o engine.Observation) []engine.Event {
	f.t.Helper()
	evs, _, err := engine.Observe(f.th, o, now)
	if err != nil {
		f.t.Fatal(err)
	}
	return f.fold(evs)
}

func newThread(t *testing.T) *thread {
	t.Helper()
	id := api.NewID()
	evs, _, err := engine.Decide(engine.Thread{}, api.Command{ID: api.NewID(), Target: id, Body: api.ThreadCreate{Title: "t"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	f := &thread{t: t}
	f.fold(evs)
	return f
}

func dispatch(text string, mode api.DispatchMode) api.MessageDispatch {
	return api.MessageDispatch{Text: text, Mode: mode}
}

func running(t *testing.T) (*thread, api.ID) {
	f := newThread(t)
	run := f.must(dispatch("go", api.StartNow))
	f.observe(engine.Started{Run: run})
	return f, run
}

func waiting(t *testing.T) (*thread, api.ID, engine.Request) {
	f, run := running(t)
	f.observe(engine.Surfaced{Run: run, Request: api.NewID(), Dedup: "r1", Kind: "approval", Body: json.RawMessage(`{}`)})
	return f, run, f.th.Requests[0]
}

func answer(r engine.Request) api.RequestRespond {
	return api.RequestRespond{Request: r.ID, Generation: r.Generation, Answer: json.RawMessage(`{"allow":true}`)}
}

func TestRestoringARunningRunKeepsItsRevision(t *testing.T) {
	f, run := running(t)
	before := f.th.Revision
	if evs := f.observe(engine.Reconciled{Run: run, Restored: true}); len(evs) != 1 {
		t.Fatalf("restore recorded %d events, want turn.restored", len(evs))
	}
	if f.th.Revision != before {
		t.Fatalf("revision %d after turn.restored on a running run, want %d: a client's revision went stale for nothing", f.th.Revision, before)
	}
}

// A plan is the transcript's alone, as a work item is: the live run's
// reaches the log and leaves the thread's state, and its revision, as they
// were; one of a run that ended is dropped.
func TestAPlanIsTheLiveRunsAndChangesNoState(t *testing.T) {
	f, run := running(t)
	before := f.th
	plan := api.PlanUpdated{Run: run, Steps: []api.PlanStep{{Text: "Read the code", Status: api.PlanActive}, {Text: "Fix the bug"}}}
	evs := f.observe(engine.Plan{Plan: plan})
	if len(evs) != 1 || !reflect.DeepEqual(evs[0].Body, plan) {
		t.Fatalf("the live run's plan recorded %+v, want one plan.updated", evs)
	}
	if after := f.th; after.Revision != before.Revision || !reflect.DeepEqual(after.Runs, before.Runs) || !reflect.DeepEqual(after.Queue, before.Queue) {
		t.Fatalf("a plan changed the thread: revision %d, runs %+v; want revision %d, runs %+v", after.Revision, after.Runs, before.Revision, before.Runs)
	}
	f.observe(engine.Ended{Run: run, Outcome: api.OutcomeCompleted})
	if evs := f.observe(engine.Plan{Plan: plan}); len(evs) != 0 {
		t.Fatalf("a plan of a run that ended recorded %+v, want nothing", evs)
	}
}

func TestObserveReturnsWhatTheTablesRefuse(t *testing.T) {
	f, run := running(t)
	steer := f.must(dispatch("hurry", api.SteerActive))
	f.observe(engine.Ended{Run: run, Outcome: api.OutcomeCompleted})
	evs, _, err := engine.Observe(f.th, engine.Steered{Message: steer}, now)
	if len(evs) != 0 || !errors.Is(err, engine.ErrRefused) {
		t.Fatalf("a steer that landed after its run ended: %v, %v; want nothing recorded and engine.ErrRefused", evs, err)
	}
}

func refuses(t *testing.T, th engine.Thread, body api.EventBody) {
	t.Helper()
	defer func() {
		r := recover()
		err, _ := r.(error)
		if te := (*engine.TransitionError)(nil); !errors.As(err, &te) {
			t.Fatalf("Apply(%s) = %v, want a TransitionError", api.EventType(body), r)
		}
	}()
	engine.Apply(th, engine.Event{Thread: th.ID, At: now, Body: body})
}

func TestTablesRefuseUnlistedEdges(t *testing.T) {
	for name, tc := range map[string]func(t *testing.T) (engine.Thread, api.EventBody){
		"run starting to waiting": func(t *testing.T) (engine.Thread, api.EventBody) {
			f := newThread(t)
			run := f.must(dispatch("go", api.StartNow))
			return f.th, api.RequestOpened{Request: api.NewID(), Run: run, Dedup: "r1"}
		},
		"run running to starting": func(t *testing.T) (engine.Thread, api.EventBody) {
			f, _ := running(t)
			next := f.must(dispatch("next", api.AfterActive))
			return f.th, api.TurnAdmitted{Run: next, Generation: f.th.Generation + 1}
		},
		"run stopping to running": func(t *testing.T) (engine.Thread, api.EventBody) {
			f, run := running(t)
			f.must(api.TurnInterrupt{})
			return f.th, api.TurnStarted{Run: run}
		},
		"run stopping to waiting": func(t *testing.T) (engine.Thread, api.EventBody) {
			f, run := running(t)
			f.must(api.TurnInterrupt{})
			return f.th, api.RequestOpened{Request: api.NewID(), Run: run, Dedup: "r1"}
		},
		"run uncertain to stopping": func(t *testing.T) (engine.Thread, api.EventBody) {
			f, run := running(t)
			f.observe(engine.Reconciled{Run: run})
			return f.th, api.TurnStopping{Run: run}
		},
		"run uncertain to running": func(t *testing.T) (engine.Thread, api.EventBody) {
			f, run := running(t)
			f.observe(engine.Reconciled{Run: run})
			return f.th, api.TurnStarted{Run: run}
		},
		"run finished to stopping": func(t *testing.T) (engine.Thread, api.EventBody) {
			f, run := running(t)
			f.observe(engine.Ended{Run: run, Outcome: api.OutcomeCompleted})
			return f.th, api.TurnStopping{Run: run}
		},
		"message ready to dispatched": func(t *testing.T) (engine.Thread, api.EventBody) {
			f, _ := running(t)
			return f.th, api.MessageDispatched{Message: f.must(dispatch("next", api.AfterActive))}
		},
		"message held to dispatched": func(t *testing.T) (engine.Thread, api.EventBody) {
			f, _ := running(t)
			return f.th, api.MessageDispatched{Message: f.must(dispatch("later", api.Defer))}
		},
		"message cancelled to dispatched": func(t *testing.T) (engine.Thread, api.EventBody) {
			f, run := running(t)
			steer := f.must(dispatch("hurry", api.SteerActive))
			f.observe(engine.Ended{Run: run, Outcome: api.OutcomeCompleted})
			return f.th, api.MessageDispatched{Message: steer}
		},
		"message dispatched to cancelled": func(t *testing.T) (engine.Thread, api.EventBody) {
			f, _ := running(t)
			steer := f.must(dispatch("hurry", api.SteerActive))
			f.observe(engine.Steered{Message: steer})
			return f.th, api.MessageCancelled{Message: steer}
		},
		"message uncertain to dispatched": func(t *testing.T) (engine.Thread, api.EventBody) {
			f, run := running(t)
			steer := f.must(dispatch("hurry", api.SteerActive))
			f.observe(engine.Reconciled{Run: run})
			return f.th, api.MessageDispatched{Message: steer}
		},
		"request pending to delivered": func(t *testing.T) (engine.Thread, api.EventBody) {
			f, _, req := waiting(t)
			return f.th, api.RequestDelivery{Request: req.ID}
		},
		"request answered to answered": func(t *testing.T) (engine.Thread, api.EventBody) {
			f, _, req := waiting(t)
			f.must(answer(req))
			return f.th, api.RequestAnswer{Request: req.ID, Answer: json.RawMessage(`{}`)}
		},
		"request delivered to invalidated": func(t *testing.T) (engine.Thread, api.EventBody) {
			f, _, req := waiting(t)
			f.must(answer(req))
			f.observe(engine.Delivered{Request: req.ID})
			return f.th, api.RequestInvalidation{Request: req.ID}
		},
		"request invalidated to answered": func(t *testing.T) (engine.Thread, api.EventBody) {
			f, _, req := waiting(t)
			f.must(api.TurnInterrupt{})
			return f.th, api.RequestAnswer{Request: req.ID, Answer: json.RawMessage(`{}`)}
		},
	} {
		t.Run(name, func(t *testing.T) {
			th, body := tc(t)
			refuses(t, th, body)
		})
	}
}

func TestRefusedCommandsAreRejected(t *testing.T) {
	f, _, req := waiting(t)
	if _, _, err := f.submit(dispatch("now", api.StartNow)); !errors.As(err, new(*api.RejectedError)) {
		t.Fatalf("StartNow on a busy thread: %v, want rejected", err)
	}
	f.must(answer(req))
	if _, _, err := f.submit(answer(req)); !errors.Is(err, api.ErrAlreadyAnswered) {
		t.Fatalf("second answer: %v, want ErrAlreadyAnswered", err)
	}
}

func TestAnAnswerInFlightOutlivesItsRun(t *testing.T) {
	f, run, req := waiting(t)
	f.must(answer(req))
	f.must(api.TurnInterrupt{})
	f.observe(engine.Ended{Run: run, Outcome: api.OutcomeInterrupted})
	if r, _ := f.th.Request(req.ID); r.State != api.RequestAnswered {
		t.Fatalf("the run ended with its answer in flight and the request is %v, want answered", r.State)
	}
	f.observe(engine.Delivered{Request: req.ID})
	if r, _ := f.th.Request(req.ID); r.State != api.RequestDelivered {
		t.Fatalf("the agent took the answer and the request is %v, want delivered", r.State)
	}
}

// A request the agent withdrew, as when the user's own hook decided it, is
// void and lets the run go on; one already answered keeps its answer.
func TestAWithdrawnRequestIsVoid(t *testing.T) {
	f, run, req := waiting(t)
	f.observe(engine.Withdrawn{Run: run, Dedup: req.Dedup, Reason: "a hook decided"})
	if r, _ := f.th.Request(req.ID); r.State != api.RequestInvalidated {
		t.Fatalf("the withdrawn request is %v, want invalidated", r.State)
	}
	if live := f.th.Live(); live == nil || live.State != engine.RunRunning {
		t.Fatalf("run after its only request was withdrawn: %+v, want running", live)
	}
	if _, _, err := f.submit(answer(req)); err == nil {
		t.Error("answering a withdrawn request succeeded")
	}

	f, run, req = waiting(t)
	f.must(answer(req))
	f.observe(engine.Withdrawn{Run: run, Dedup: req.Dedup})
	if r, _ := f.th.Request(req.ID); r.State != api.RequestAnswered {
		t.Fatalf("a withdrawal after the answer left %v, want answered", r.State)
	}
}

// An answer that lost the race to the agent's withdrawal, so the agent
// never heard it, voids the request and leaves the run at work: unlike a
// failed delivery, it stops nothing.
func TestAnUnheardAnswerLeavesTheRunGoing(t *testing.T) {
	f, run, req := waiting(t)
	f.must(answer(req))
	f.observe(engine.Withdrawn{Run: run, Dedup: req.Dedup})
	f.observe(engine.Unheard{Request: req.ID, Reason: "the agent stopped waiting"})
	if r, _ := f.th.Request(req.ID); r.State != api.RequestInvalidated {
		t.Fatalf("the unheard answer left %v, want invalidated", r.State)
	}
	if live := f.th.Live(); live == nil || live.State != engine.RunRunning {
		t.Fatalf("run after an unheard answer: %+v, want running", live)
	}
}

func TestInterruptingAStoppingRunGivesUpOnIt(t *testing.T) {
	f, run := running(t)
	if _, effects, _ := f.submit(api.TurnInterrupt{}); len(effects) != 1 || f.th.Live().State != engine.RunStopping {
		t.Fatalf("first interrupt: %v with effects %v, want stopping and one interrupt", f.th.Live().State, effects)
	}
	if _, effects, _ := f.submit(api.TurnInterrupt{}); len(effects) != 0 || f.th.Live().State != engine.RunStopping {
		t.Fatalf("second interrupt before the first reached the agent: %v with effects %v, want still stopping and nothing new",
			f.th.Live().State, effects)
	}
	f.observe(engine.InterruptSent{Run: run})
	f.must(api.TurnInterrupt{})
	if s := f.th.Live().State; s != engine.RunUncertain {
		t.Fatalf("interrupt after the agent ignored one left the run %v, want uncertain", s)
	}
	if evs := f.observe(engine.Ended{Run: run, Outcome: api.OutcomeInterrupted}); len(evs) != 0 {
		t.Fatalf("an uncertain run was ended by an observation: %v", evs)
	}
	f.must(api.TurnInterrupt{})
	if f.th.Live() != nil || f.th.Runs[0].Outcome != api.OutcomeAbandoned {
		t.Fatalf("last interrupt: run %+v, want it abandoned", f.th.Runs[0])
	}
}

func TestDue(t *testing.T) {
	f, run, req := waiting(t)
	start := engine.StartTurn{Run: run}
	steer := engine.SteerTurn{Run: run, Message: f.must(dispatch("hurry", api.SteerActive))}
	deliver := engine.DeliverAnswer{Run: run, Request: req.ID}
	interrupt := engine.InterruptTurn{Run: run}
	due := func(e engine.Effect) bool {
		d, _ := engine.Due(f.th, e)
		return d
	}
	for _, e := range []engine.Effect{start, deliver, interrupt} {
		if due(e) {
			t.Errorf("%s is due on a waiting run with nothing answered", engine.EffectKind(e))
		}
	}
	if !due(steer) {
		t.Error("a claimed steer is not due")
	}
	f.must(answer(req))
	if !due(deliver) {
		t.Error("an answer is not due on the run that asked")
	}
	f.must(api.TurnInterrupt{})
	if !due(interrupt) {
		t.Error("an interrupt is not due on a stopping run")
	}
	for _, e := range []engine.Effect{steer, deliver} {
		d, skip := engine.Due(f.th, e)
		if d || skip == nil {
			t.Fatalf("%s on a stopping run: due %v, skip %v; want it skipped as a failure", engine.EffectKind(e), d, skip)
		}
		f.observe(skip)
	}
	m, _ := f.th.Message(steer.Message)
	r, _ := f.th.Request(req.ID)
	if m.State != engine.QueueCancelled || r.State != api.RequestInvalidated {
		t.Fatalf("skipped on a stopping run: steer %v, answer %v; want cancelled and invalidated", m.State, r.State)
	}
	f.observe(engine.Ended{Run: run, Outcome: api.OutcomeInterrupted})
	for _, e := range []engine.Effect{steer, interrupt} {
		if d, skip := engine.Due(f.th, e); d || skip != nil {
			t.Errorf("%s after its run ended: due %v, skip %v; want neither", engine.EffectKind(e), d, skip)
		}
	}
}

func TestAFailedDeliveryOnlyStopsARunStillAtWork(t *testing.T) {
	for name, tc := range map[string]struct {
		before func(f *thread, run api.ID)
		want   engine.RunState
	}{
		"running":   {func(*thread, api.ID) {}, engine.RunStopping},
		"stopping":  {func(f *thread, _ api.ID) { f.must(api.TurnInterrupt{}) }, engine.RunStopping},
		"uncertain": {func(f *thread, run api.ID) { f.observe(engine.Reconciled{Run: run}) }, engine.RunUncertain},
	} {
		t.Run(name, func(t *testing.T) {
			f, run, req := waiting(t)
			f.must(answer(req))
			tc.before(f, run)
			f.observe(engine.DeliveryFailed{Request: req.ID, Error: "gone"})
			if r, _ := f.th.Request(req.ID); r.State != api.RequestInvalidated {
				t.Fatalf("undeliverable answer left %v, want invalidated", r.State)
			}
			if live := f.th.Live(); live == nil || live.State != tc.want {
				t.Fatalf("run after a failed delivery: %+v, want %v", live, tc.want)
			}
		})
	}
}

// A rename trims the title and moves the revision, an unchanged one
// records nothing, and an empty one is refused.
func TestRename(t *testing.T) {
	f := newThread(t)
	rev := f.th.Revision
	f.must(api.ThreadRename{Title: "  Websocket loop "})
	if v := engine.View(f.th); v.Title != "Websocket loop" || v.Revision != rev+1 {
		t.Fatalf("after rename: title %q, revision %d, want %q at %d", v.Title, v.Revision, "Websocket loop", rev+1)
	}
	evs, _, err := engine.Decide(f.th, api.Command{ID: api.NewID(), Target: f.th.ID, Body: api.ThreadRename{Title: "Websocket loop"}}, now)
	if err != nil || len(evs) != 0 {
		t.Fatalf("the same title: %d events, %v; want none", len(evs), err)
	}
	if _, _, err := f.submit(api.ThreadRename{Title: "  "}); !errors.As(err, new(*api.RejectedError)) {
		t.Fatalf("an empty title: %v, want a rejection", err)
	}
}

// Open, Snoozed and Settled are the only inbox states. The archived state
// protocol 2 had is refused, and Open brings a settled thread back.
func TestInboxStates(t *testing.T) {
	f := newThread(t)
	f.must(api.InboxSet{Disposition: api.Disposition{State: api.Settled}})
	if _, _, err := f.submit(api.InboxSet{Disposition: api.Disposition{State: api.Settled, Until: now.Add(time.Hour)}}); !errors.As(err, new(*api.RejectedError)) {
		t.Fatalf("settled with an until: %v, want a rejection", err)
	}
	if _, _, err := f.submit(api.InboxSet{Disposition: api.Disposition{State: api.Settled + 1}}); !errors.As(err, new(*api.RejectedError)) {
		t.Fatalf("the archived state: %v, want a rejection", err)
	}
	f.must(api.InboxSet{Disposition: api.Disposition{}})
	if d := engine.View(f.th).Disposition; d.State != api.Open {
		t.Fatalf("disposition %v after reopening, want open", d.State)
	}
}

// A deleted thread refuses every command, a create under its ID too, and
// every observation.
func TestDelete(t *testing.T) {
	f, run := running(t)
	f.observe(engine.Ended{Run: run, Outcome: api.OutcomeCompleted})
	f.must(api.ThreadDelete{})
	if v := engine.View(f.th); !v.Deleted {
		t.Fatal("the view is no tombstone")
	}
	for _, body := range []api.Intent{dispatch("again", api.StartNow), api.ThreadRename{Title: "x"}, api.ThreadDelete{}, api.ThreadCreate{Title: "t"}} {
		if _, _, err := f.submit(body); !errors.Is(err, api.ErrNotFound) {
			t.Errorf("%s on a deleted thread: %v, want ErrNotFound", api.IntentType(body), err)
		}
	}
	if _, _, err := engine.Observe(f.th, engine.Started{Run: run}, now); !errors.Is(err, engine.ErrRefused) {
		t.Errorf("an observation on a deleted thread: %v, want ErrRefused", err)
	}
}

// A steer reaches the live run or starts a turn on an idle thread; one the
// run can't take, stopping or uncertain, is refused rather than queued.
func TestASteerIsNeverQueued(t *testing.T) {
	stopping, _ := running(t)
	stopping.must(api.TurnInterrupt{})
	uncertain, run := running(t)
	uncertain.observe(engine.Reconciled{Run: run})
	for name, f := range map[string]*thread{"stopping": stopping, "uncertain": uncertain} {
		queued := len(f.th.Queue)
		_, _, err := f.submit(dispatch("hurry", api.SteerActive))
		if rej := (*api.RejectedError)(nil); !errors.As(err, &rej) || len(f.th.Queue) != queued {
			t.Fatalf("a steer while the run is %s: %v, %d messages more; want rejected and none queued", name, err, len(f.th.Queue)-queued)
		}
	}

	idle := newThread(t)
	id, effects, err := idle.submit(dispatch("go", api.SteerActive))
	if err != nil || len(effects) != 1 || idle.th.Live() == nil || idle.th.Live().ID != id {
		t.Fatalf("a steer to an idle thread: %v, %v; want its turn started", effects, err)
	}
}

// A halt stops the run and cancels what waits its turn, a held message
// too, so no turn starts once the run ends, and the thread can be deleted.
// A plain interrupt leaves the queue to start once the run ends.
func TestAHaltStartsNothingAfter(t *testing.T) {
	f, run := running(t)
	next := f.must(dispatch("next", api.AfterActive))
	later := f.must(dispatch("later", api.Defer))
	if _, effects, err := f.submit(api.TurnInterrupt{Drop: true}); err != nil || len(effects) != 1 || f.th.Live().State != engine.RunStopping {
		t.Fatalf("halt: %v with effects %v, want stopping and one interrupt", err, effects)
	}
	for _, id := range []api.ID{next, later} {
		if m, _ := f.th.Message(id); m.State != engine.QueueCancelled {
			t.Fatalf("a queued message is %v after the halt, want cancelled", m.State)
		}
	}
	f.observe(engine.Ended{Run: run, Outcome: api.OutcomeInterrupted})
	if r := f.th.Live(); r != nil {
		t.Fatalf("a turn started after the halt: %+v", *r)
	}
	f.must(api.ThreadDelete{})

	g, run := running(t)
	next = g.must(dispatch("next", api.AfterActive))
	g.must(api.TurnInterrupt{})
	g.observe(engine.Ended{Run: run, Outcome: api.OutcomeInterrupted})
	if r := g.th.Live(); r == nil || r.ID != next {
		t.Fatalf("after a plain interrupt the live run is %+v, want the queued message's", r)
	}
}

// A halt on a run already stopping, its interrupt taken by the agent,
// cancels what waits its turn and asks nothing more of the run: the
// agent's end still ends it. Only a plain interrupt, asked again, gives up
// on the run.
func TestAHaltOnAStoppingRunLetsItEnd(t *testing.T) {
	f, run := running(t)
	f.must(api.TurnInterrupt{})
	f.observe(engine.InterruptSent{Run: run})
	next := f.must(dispatch("next", api.AfterActive))
	if _, effects, err := f.submit(api.TurnInterrupt{Drop: true}); err != nil || len(effects) != 0 {
		t.Fatalf("halt on a stopping run: %v with effects %v, want nothing new", err, effects)
	}
	if m, _ := f.th.Message(next); m.State != engine.QueueCancelled {
		t.Errorf("the follow-up is %v after the halt, want cancelled", m.State)
	}
	f.observe(engine.Ended{Run: run, Outcome: api.OutcomeInterrupted})
	if r := f.th.Live(); r != nil {
		t.Errorf("run is %v, want ended", r.State)
	}
}

// Settling a thread stops its run and cancels what waits its turn, in the
// settle's own commit. A settled thread starts no turn: a message sent to
// it waits until the thread is back in the inbox.
func TestASettledThreadStartsNothing(t *testing.T) {
	f, run := running(t)
	next := f.must(dispatch("next", api.AfterActive))
	if _, effects, err := f.submit(api.InboxSet{Disposition: api.Disposition{State: api.Settled}}); err != nil || len(effects) != 1 {
		t.Fatalf("settle: %v with effects %v, want one interrupt", err, effects)
	}
	if r := f.th.Live(); r == nil || r.State != engine.RunStopping {
		t.Fatalf("the run after the settle: %+v, want stopping", r)
	}
	if m, _ := f.th.Message(next); m.State != engine.QueueCancelled {
		t.Errorf("the follow-up is %v after the settle, want cancelled", m.State)
	}
	f.observe(engine.Ended{Run: run, Outcome: api.OutcomeInterrupted})
	if r := f.th.Live(); r != nil {
		t.Fatalf("a turn started on a settled thread: %+v", *r)
	}
	later := f.must(dispatch("later", api.StartNow))
	if r := f.th.Live(); r == nil || r.ID != later {
		t.Fatalf("the live run after a message to the settled thread is %+v, want the message's", r)
	}
	if m, _ := f.th.Message(next); m.State != engine.QueueCancelled {
		t.Errorf("the follow-up from before the settle is %v, want still cancelled", m.State)
	}

	g, run := running(t)
	g.must(api.TurnInterrupt{})
	g.observe(engine.InterruptSent{Run: run})
	if _, effects, err := g.submit(api.InboxSet{Disposition: api.Disposition{State: api.Settled}}); err != nil || len(effects) != 0 {
		t.Fatalf("settling a stopping run: %v with effects %v, want nothing new", err, effects)
	}
	g.observe(engine.Ended{Run: run, Outcome: api.OutcomeInterrupted})
	if r := g.th.Live(); r != nil {
		t.Errorf("a stopped run settled is %v, want ended", r.State)
	}
}

// Deleting a live thread ends its run in the delete's commit, abandoned,
// whatever state it was in, so no run outlives its thread.
func TestDeletingALiveThreadEndsItsRun(t *testing.T) {
	plain, planned := running(t)
	plain.must(dispatch("next", api.AfterActive))
	stopping, stopped := running(t)
	stopping.must(api.TurnInterrupt{})
	stopping.observe(engine.InterruptSent{Run: stopped})
	uncertain, lost := running(t)
	uncertain.observe(engine.Reconciled{Run: lost})
	asking, asked, _ := waiting(t)
	type live struct {
		f   *thread
		run api.ID
	}
	for name, l := range map[string]live{"running": {plain, planned}, "stopping": {stopping, stopped}, "uncertain": {uncertain, lost}, "waiting": {asking, asked}} {
		f := l.f
		want := []engine.Effect{engine.CloseAgent{Run: l.run, Generation: 1}}
		if _, effects, err := f.submit(api.ThreadDelete{}); err != nil || !reflect.DeepEqual(effects, want) {
			t.Fatalf("delete while %s: %v with effects %v, want deleted, closing its agent", name, err, effects)
		}
		if !f.th.Deleted || f.th.Live() != nil {
			t.Fatalf("delete while %s: deleted %v, live %+v; want deleted with no run", name, f.th.Deleted, f.th.Live())
		}
		if got := f.th.Runs[len(f.th.Runs)-1].Outcome; got != api.OutcomeAbandoned {
			t.Errorf("delete while %s ended the run %v, want abandoned", name, got)
		}
	}
}

// Deleting an idle thread closes the agent of its last run, which its
// provider may keep for the next one; a thread that never ran has none.
func TestDeletingAnIdleThreadClosesItsLastRunsAgent(t *testing.T) {
	f, run := running(t)
	f.observe(engine.Ended{Run: run, Outcome: api.OutcomeCompleted})
	want := []engine.Effect{engine.CloseAgent{Run: run, Generation: 1}}
	if _, effects, err := f.submit(api.ThreadDelete{}); err != nil || !reflect.DeepEqual(effects, want) {
		t.Errorf("delete of an idle thread: %v with effects %v, want %v", err, effects, want)
	}
	if _, effects, err := newThread(t).submit(api.ThreadDelete{}); err != nil || len(effects) != 0 {
		t.Errorf("delete of a thread that never ran: %v with effects %v, want none", err, effects)
	}
}

// A message sent to a settled thread, in any mode and from any client, is
// new work: in the same commit the thread comes back to the inbox and the
// message starts, or waits held if it was deferred.
func TestAMessageBringsASettledThreadBack(t *testing.T) {
	for _, mode := range []api.DispatchMode{api.AfterActive, api.SteerActive, api.StartNow, api.RestartActive, api.Defer} {
		f := newThread(t)
		f.must(api.InboxSet{Disposition: api.Disposition{State: api.Settled}})
		msg, effects, err := f.submit(dispatch("again", mode))
		if err != nil {
			t.Fatalf("mode %v: %v", mode, err)
		}
		if f.th.Disposition.State != api.Open {
			t.Errorf("mode %v: the thread is %v after the message, want back in the inbox", mode, f.th.Disposition.State)
		}
		r := f.th.Live()
		if mode == api.Defer {
			if m, _ := f.th.Message(msg); r != nil || m.State != engine.QueueHeld {
				t.Errorf("deferred: run %+v, message %v; want no run and the message held", r, m.State)
			}
			continue
		}
		if r == nil || r.ID != msg || len(effects) != 1 {
			t.Errorf("mode %v: run %+v with effects %v, want the message started", mode, r, effects)
		}
	}
}

// A message sent before the settle's stop lands brings the thread back in
// any mode. It waits for the stopped run to end and then starts, or stays
// held if deferred; what waited before the settle stays cancelled.
func TestAMessageAfterSettleWaitsForTheStoppedRun(t *testing.T) {
	for _, mode := range []api.DispatchMode{api.AfterActive, api.SteerActive, api.StartNow, api.RestartActive, api.Defer} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			f, run := running(t)
			old := f.must(dispatch("before settle", api.AfterActive))
			f.must(api.InboxSet{Disposition: api.Disposition{State: api.Settled}})
			f.observe(engine.InterruptSent{Run: run})
			msg, effects, err := f.submit(dispatch("after settle", mode))
			if err != nil {
				t.Fatal(err)
			}
			if f.th.Disposition.State != api.Open || len(effects) != 0 {
				t.Errorf("the thread is %v with effects %v, want back in the inbox and nothing sent to the stopping run", f.th.Disposition.State, effects)
			}
			evs, effects, err := engine.Observe(f.th, engine.Ended{Run: run, Outcome: api.OutcomeInterrupted}, now)
			if err != nil {
				t.Fatal(err)
			}
			f.fold(evs)
			if m, _ := f.th.Message(old); m.State != engine.QueueCancelled {
				t.Errorf("the message from before the settle is %v, want cancelled", m.State)
			}
			r := f.th.Live()
			if mode == api.Defer {
				if m, _ := f.th.Message(msg); r != nil || m.State != engine.QueueHeld {
					t.Errorf("deferred: run %+v, message %v; want no run and the message held", r, m.State)
				}
				return
			}
			want := []engine.Effect{engine.StartTurn{Run: msg, Generation: 2, Text: "after settle", Workspace: f.th.Workspace, Model: f.th.Model, Access: f.th.Access}}
			if r == nil || r.ID != msg || !reflect.DeepEqual(effects, want) {
				t.Errorf("once the stop landed, run %+v with effects %v; want the message started", r, effects)
			}
		})
	}
}
