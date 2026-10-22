package engine_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
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

// running returns a thread whose run has started.
func running(t *testing.T) (*thread, api.ID) {
	f := newThread(t)
	run := f.must(dispatch("go", api.StartNow))
	f.observe(engine.Started{Run: run})
	return f, run
}

// waiting returns a running thread whose run asked for approval.
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

func TestObserveLogsWhatTheTablesRefuse(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	f, run := running(t)
	steer := f.must(dispatch("hurry", api.SteerActive))
	f.observe(engine.Ended{Run: run, Outcome: api.OutcomeCompleted})
	if evs := f.observe(engine.Steered{Message: steer}); len(evs) != 0 {
		t.Fatalf("a steer that landed after its run ended recorded %v", evs)
	}
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "engine.Steered") {
		t.Fatalf("the refused steer left no warning, log: %q", logs.String())
	}
}

// refuses expects Apply to reject body as an edge its tables don't list.
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

func TestInterruptingAStoppingRunGivesUpOnIt(t *testing.T) {
	f, run := running(t)
	if _, effects, _ := f.submit(api.TurnInterrupt{}); len(effects) != 1 || f.th.Live().State != engine.RunStopping {
		t.Fatalf("first interrupt: %v with effects %v, want stopping and one interrupt", f.th.Live().State, effects)
	}
	f.must(api.TurnInterrupt{})
	if s := f.th.Live().State; s != engine.RunUncertain {
		t.Fatalf("second interrupt left the run %v, want uncertain", s)
	}
	if evs := f.observe(engine.Ended{Run: run, Outcome: api.OutcomeInterrupted}); len(evs) != 0 {
		t.Fatalf("an uncertain run was ended by an observation: %v", evs)
	}
	f.must(api.TurnInterrupt{})
	if f.th.Live() != nil || f.th.Runs[0].Outcome != api.OutcomeAbandoned {
		t.Fatalf("third interrupt: run %+v, want it abandoned", f.th.Runs[0])
	}
}

func TestDue(t *testing.T) {
	f, run, req := waiting(t)
	start := engine.StartTurn{Run: run}
	steer := engine.SteerTurn{Run: run, Message: f.must(dispatch("hurry", api.SteerActive))}
	deliver := engine.DeliverAnswer{Run: run, Request: req.ID}
	interrupt := engine.InterruptTurn{Run: run}
	for _, e := range []engine.Effect{start, deliver, interrupt} {
		if engine.Due(f.th, e) {
			t.Errorf("%s is due on a waiting run with nothing answered", engine.EffectKind(e))
		}
	}
	if !engine.Due(f.th, steer) {
		t.Error("a claimed steer is not due")
	}
	f.must(answer(req))
	f.must(api.TurnInterrupt{})
	for _, e := range []engine.Effect{deliver, interrupt} {
		if !engine.Due(f.th, e) {
			t.Errorf("%s is not due on a stopping run with an answer in flight", engine.EffectKind(e))
		}
	}
	f.observe(engine.Ended{Run: run, Outcome: api.OutcomeInterrupted})
	for _, e := range []engine.Effect{steer, interrupt} {
		if engine.Due(f.th, e) {
			t.Errorf("%s is due after its run ended", engine.EffectKind(e))
		}
	}
}
