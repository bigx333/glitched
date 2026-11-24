package store_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
	"glitched/internal/store"
)

type taskTree struct {
	t          *testing.T
	s          *store.Store
	caller     api.Caller
	child, run api.ID
}

func newTaskTree(t *testing.T) taskTree {
	t.Helper()
	f := taskTree{t: t, s: open(t, filepath.Join(t.TempDir(), "host.db"))}
	f.caller.Thread, f.caller.Run = working(t, f.s)
	var err error
	f.child, f.run, err = spawn(f.s, f.caller.Thread, f.caller.Run)
	if err != nil {
		t.Fatal(err)
	}
	f.observe(engine.Started{Run: f.run})
	return f
}

func (f taskTree) observe(o engine.Observation) {
	f.t.Helper()
	if _, err := f.s.Observe(context.Background(), f.child, o, time.Now(), 0); err != nil {
		f.t.Fatal(err)
	}
}

func (f taskTree) submit(caller *api.Caller, body api.Intent) error {
	_, err := f.s.Submit(context.Background(), api.Command{ID: api.NewID(), Target: f.child, Caller: caller, Body: body}, time.Now())
	return err
}

func (f taskTree) must(caller *api.Caller, body api.Intent) {
	f.t.Helper()
	if err := f.submit(caller, body); err != nil {
		f.t.Fatal(err)
	}
}

func (f taskTree) view() api.Thread {
	f.t.Helper()
	s, err := f.s.Read(context.Background(), api.Query{Thread: f.child})
	if err != nil {
		f.t.Fatal(err)
	}
	return s.Threads[0]
}

func TestTheUsersStopTakesAChildOverWhateverItsRunIsDoing(t *testing.T) {
	for _, state := range []string{"stopping", "signalled", "uncertain"} {
		t.Run(state, func(t *testing.T) {
			f := newTaskTree(t)
			switch state {
			case "stopping", "signalled":
				f.must(&f.caller, api.TurnInterrupt{})
				if state == "signalled" {
					f.observe(engine.InterruptSent{Run: f.run})
				}
			case "uncertain":
				f.observe(engine.Reconciled{Run: f.run})
			}
			f.must(nil, api.TurnInterrupt{})
			switch state {
			case "stopping":
				f.observe(engine.Ended{Run: f.run, Outcome: api.OutcomeInterrupted})
			case "signalled":
				f.must(nil, api.TurnInterrupt{})
			}
			if th := f.view(); th.Run != api.RunIdle {
				t.Fatalf("user's stop did not lead to idle: %+v", th)
			}
			err := f.submit(&f.caller, api.MessageDispatch{Text: "start again", Mode: api.AfterActive})
			if err == nil || !strings.Contains(err.Error(), "user took this thread over") {
				t.Errorf("parent's send after the user's %s stop: %v; want takeover refusal", state, err)
			}
		})
	}
}

func TestAnAgentNeverEndsAnUncertainRun(t *testing.T) {
	f := newTaskTree(t)
	f.observe(engine.Reconciled{Run: f.run})
	if err := f.submit(&f.caller, api.TurnInterrupt{}); err == nil {
		t.Fatal("ordinary agent interrupt unexpectedly ended uncertain run")
	}
	err := f.submit(&f.caller, api.MessageDispatch{Mode: api.RestartActive, Text: "replacement work"})
	th := f.view()
	if err == nil || th.Run != api.RunUncertain || th.Turn.Run != f.run {
		t.Errorf("agent restart dispatch: %v, prior run %s, current %+v; want rejection with original run uncertain", err, f.run, th.Turn)
	}
}

func TestAnAgentsSecondStopLeavesTheRunStopping(t *testing.T) {
	f := newTaskTree(t)
	f.must(&f.caller, api.TurnInterrupt{})
	f.observe(engine.InterruptSent{Run: f.run})
	err := f.submit(&f.caller, api.TurnInterrupt{})
	if th := f.view(); th.Run != api.RunStopping {
		t.Errorf("second agent stop: %v; run state %v, want it to stay stopping", err, th.Run)
	}
}

func TestTheEnginesOwnStopDoesNotTakeAChildOver(t *testing.T) {
	f := newTaskTree(t)
	request := api.NewID()
	f.observe(engine.Surfaced{Run: f.run, Request: request, Dedup: "prompt", Kind: api.KindApproval, Body: []byte(`{}`)})
	f.must(nil, api.RequestRespond{Request: request, Generation: f.view().Requests[0].Generation, Answer: []byte(`{"allow":true}`)})
	f.observe(engine.DeliveryFailed{Request: request, Error: "provider did not receive the answer"})
	f.observe(engine.Ended{Run: f.run, Outcome: api.OutcomeInterrupted})
	if err := f.submit(&f.caller, api.MessageDispatch{Mode: api.AfterActive, Text: "try again"}); err != nil {
		t.Errorf("a stop issued by the engine, not by the user, blocked a parent send: %v", err)
	}
}

func TestStopHoldResumeAndReportsKeepWhoSteered(t *testing.T) {
	f := newTaskTree(t)
	queued := api.Command{ID: api.NewID(), Target: f.child, Caller: &f.caller, Body: api.MessageDispatch{Mode: api.AfterActive, Text: "queued by parent"}}
	submitTo(t, f.s, queued)
	f.must(&f.caller, api.TurnInterrupt{})
	f.observe(engine.Reported{Report: api.ChildReported{Child: api.NewID(), Run: api.NewID(), Kind: api.ReportDone, Title: "Grandchild"}})
	f.observe(engine.Ended{Run: f.run, Outcome: api.OutcomeInterrupted})
	th := f.view()
	if th.Run != api.RunIdle || !th.Resumable || th.Queued != 2 {
		t.Fatalf("after parent stop: state %v, resumable %v, queued %d", th.Run, th.Resumable, th.Queued)
	}
	f.must(nil, api.QueueResume{})
	th = f.view()
	if th.Turn == nil || th.Turn.Run != queued.ID {
		t.Fatalf("Resume should start the held parent message, got %+v", th.Turn)
	}
	f.must(&f.caller, api.MessageDispatch{Mode: api.AfterActive, Text: "parent can still send"})
	f.must(nil, api.MessageDispatch{Mode: api.Defer, Text: "user takes over while queued"})
	f.observe(engine.Reported{Report: api.ChildReported{Child: api.NewID(), Run: api.NewID(), Kind: api.ReportDone, Title: "Another grandchild"}})
	if err := f.submit(&f.caller, api.MessageDispatch{Mode: api.AfterActive, Text: "parent is now refused"}); err == nil || !strings.Contains(err.Error(), "user took this thread over") {
		t.Errorf("a report wake must not erase user ownership; got %v", err)
	}
}

func TestASettledChildTakesAnAgentsMessageAndADeletedOneDoesNot(t *testing.T) {
	f := newTaskTree(t)
	f.observe(engine.Ended{Run: f.run, Outcome: api.OutcomeCompleted})
	f.must(nil, settle)
	f.must(&f.caller, api.MessageDispatch{Mode: api.AfterActive, Text: "new work"})
	th := f.view()
	if th.Disposition.State == api.Settled || th.Turn == nil {
		t.Fatalf("new work is documented to reopen an idle settled child: %+v", th)
	}
	f.must(nil, del)
	if err := f.submit(&f.caller, api.MessageDispatch{Mode: api.AfterActive, Text: "never resurrect deleted work"}); !errors.Is(err, api.ErrNotFound) {
		t.Fatalf("new work for a deleted child: %v; want not found", err)
	}
}

func TestTheCommitRefusesACallerNotAboveOrNotLive(t *testing.T) {
	f := newTaskTree(t)
	unrelatedThread, unrelatedRun := working(t, f.s)
	unrelated := api.Caller{Thread: unrelatedThread, Run: unrelatedRun}
	if err := f.submit(&unrelated, api.TurnInterrupt{}); err == nil || !strings.Contains(err.Error(), "under its own") {
		t.Errorf("unrelated caller: %v", err)
	}
	submitTo(t, f.s, api.Command{ID: api.NewID(), Target: f.caller.Thread, Body: api.TurnInterrupt{}})
	if err := f.submit(&f.caller, api.TurnInterrupt{}); err == nil || !strings.Contains(err.Error(), "only the live run") {
		t.Errorf("stopping caller: %v", err)
	}
}

func TestASilentRunCannotAct(t *testing.T) {
	ctx := context.Background()
	s := open(t, filepath.Join(t.TempDir(), "compact.db"))
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	parent, run, compact := api.NewID(), api.NewID(), api.NewID()
	submit := func(c api.Command, at time.Time) error {
		_, err := s.Submit(ctx, c, at)
		return err
	}
	observe := func(o engine.Observation, at time.Time) {
		t.Helper()
		if _, err := s.Observe(ctx, parent, o, at, 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := submit(api.Command{ID: api.NewID(), Target: parent, Body: api.ThreadCreate{
		Workspace: "w", Title: "parent", Model: api.ModelRef{Provider: "claude", Model: "claude-opus-4-6"},
	}}, at); err != nil {
		t.Fatal(err)
	}
	if err := submit(api.Command{ID: run, Target: parent, Body: api.MessageDispatch{Mode: api.StartNow, Text: "go"}}, at); err != nil {
		t.Fatal(err)
	}
	observe(engine.Started{Run: run}, at)
	child, _, err := spawn(s, parent, run)
	if err != nil {
		t.Fatal(err)
	}
	observe(engine.Context{Run: run, Used: 150000, Window: 200000, CacheExpires: at.Add(time.Hour)}, at)
	observe(engine.Ended{Run: run, Outcome: api.OutcomeCompleted}, at)
	due := at.Add(54 * time.Minute)
	observe(engine.IdleCompact{AfterRun: run, Run: compact}, due)
	observe(engine.Started{Run: compact}, due)
	snap, err := s.Read(ctx, api.Query{Thread: parent})
	if err != nil || snap.Threads[0].Turn == nil || !snap.Threads[0].Turn.Silent {
		t.Fatalf("no silent parent turn: %+v %v", snap.Threads, err)
	}
	err = submit(api.Command{ID: api.NewID(), Target: child, Caller: &api.Caller{Thread: parent, Run: compact}, Body: api.MessageDispatch{Mode: api.AfterActive, Text: "from a silent run"}}, due)
	if err == nil {
		t.Fatal("silent maintenance run could send a child a message; expected a non-steerable caller refusal")
	}
}

func TestAnAgentsSecondStopHoldsWhatQueuedSinceItsFirst(t *testing.T) {
	f := newTaskTree(t)
	f.must(&f.caller, api.TurnInterrupt{})
	queued := api.NewID()
	submitTo(t, f.s, api.Command{ID: queued, Target: f.child, Caller: &f.caller, Body: api.MessageDispatch{Mode: api.AfterActive, Text: "queued after the first cancel"}})
	f.must(&f.caller, api.TurnInterrupt{})
	if th := f.view(); th.Run != api.RunStopping || th.Queued != 1 {
		t.Fatalf("before provider ended: got %v with %d queued, want stopping with one queued", th.Run, th.Queued)
	}
	f.observe(engine.Ended{Run: f.run, Outcome: api.OutcomeInterrupted})
	th := f.view()
	if th.Run != api.RunIdle || !th.Resumable || th.Queued != 1 {
		t.Fatalf("second cancel didn't hold the new message %s: got run=%v (%+v), resumable=%v, queued=%d; want idle with one held message", queued, th.Run, th.Turn, th.Resumable, th.Queued)
	}
}
