package runtime_test

import (
	"context"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
	"glitched/internal/runtime"
)

func outcomes(t *testing.T, ep api.Endpoint, thread api.ID) []api.Outcome {
	t.Helper()
	var ends []api.Outcome
	for _, e := range events(t, ep, thread) {
		if e.Type == "turn.ended" {
			b, _ := e.Decode()
			ends = append(ends, b.(api.TurnEnded).Outcome)
		}
	}
	return ends
}

func TestHungStartBlocksNeitherOtherThreadsNorItsInterrupt(t *testing.T) {
	g := newGated(fake.Turn(false))
	n := start(t, dbPath(t), g)
	hung, other := newThread(t, n.h), newThread(t, n.h)
	entered, never := make(chan struct{}, 1), make(chan struct{})
	g.setGate(func(thread api.ID, a provider.Action) <-chan struct{} {
		if _, ok := a.(provider.Start); ok && thread == hung {
			entered <- struct{}{}
			return never
		}
		return nil
	})
	submit(t, n.h, hung, dispatch("hang", api.StartNow))
	<-entered
	submit(t, n.h, other, dispatch("unrelated", api.StartNow))
	until(t, n.h, other, "the other thread's turn", func(th api.Thread) bool { return idle(th) && len(outcomes(t, n.h, other)) == 1 })

	submit(t, n.h, hung, api.TurnInterrupt{})
	until(t, n.h, hung, "the interrupt ends the hung start", idle)
	if got := outcomes(t, n.h, hung); len(got) != 1 || got[0] != api.OutcomeInterrupted {
		t.Fatalf("hung run ended %v, want interrupted", got)
	}
	assertReplay(t, n.st)
}

// deaf is an adapter whose agents take an interrupt and carry on.
type deaf struct{ *fake.Adapter }

func (d deaf) Open(ctx context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	ag, err := d.Adapter.Open(ctx, spec)
	return deafAgent{ag}, err
}

type deafAgent struct{ provider.Agent }

func (a deafAgent) Do(ctx context.Context, act provider.Action) error {
	if _, ok := act.(provider.Interrupt); ok {
		return nil
	}
	return a.Agent.Do(ctx, act)
}

func TestSecondInterruptGivesUpOnARunThatIgnoresTheFirst(t *testing.T) {
	n := start(t, dbPath(t), deaf{fake.New(fake.Turn(true))})
	thread := newThread(t, n.h)
	submit(t, n.h, thread, dispatch("go", api.StartNow))
	first := until(t, n.h, thread, "approval", waiting).Requests[0].Run
	submit(t, n.h, thread, api.TurnInterrupt{})
	until(t, n.h, thread, "the agent took the interrupt", func(api.Thread) bool {
		return count(events(t, n.h, thread), "turn.signalled") == 1
	})
	submit(t, n.h, thread, api.TurnInterrupt{})
	until(t, n.h, thread, "uncertain", func(th api.Thread) bool { return th.Run == api.RunUncertain })

	submit(t, n.h, thread, dispatch("instead", api.RestartActive))
	until(t, n.h, thread, "the next run asks", func(th api.Thread) bool { return waiting(th) && th.Requests[0].Run != first })
	if got := outcomes(t, n.h, thread); len(got) != 1 || got[0] != api.OutcomeAbandoned {
		t.Fatalf("the ignored run ended %v, want abandoned", got)
	}
	assertReplay(t, n.st)
}

func TestASecondInterruptWaitsForTheFirstToReachTheAgent(t *testing.T) {
	g := newGated(fake.Turn(true))
	n := start(t, dbPath(t), g)
	thread := newThread(t, n.h)
	submit(t, n.h, thread, dispatch("go", api.StartNow))
	until(t, n.h, thread, "approval", waiting)
	entered, release := make(chan struct{}, 1), make(chan struct{})
	g.setGate(func(_ api.ID, a provider.Action) <-chan struct{} {
		if _, ok := a.(provider.Interrupt); ok {
			entered <- struct{}{}
			return release
		}
		return nil
	})
	submit(t, n.h, thread, api.TurnInterrupt{})
	<-entered
	submit(t, n.h, thread, api.TurnInterrupt{})
	if th := until(t, n.h, thread, "read", func(api.Thread) bool { return true }); th.Run != api.RunStopping {
		t.Fatalf("a second interrupt while the first was still on its way left the run %v, want stopping", th.Run)
	}
	close(release)
	until(t, n.h, thread, "turn end", idle)
	if got := outcomes(t, n.h, thread); len(got) != 1 || got[0] != api.OutcomeInterrupted {
		t.Fatalf("run ended %v, want interrupted", got)
	}
	assertReplay(t, n.st)
}

func TestProviderCallsHaveADeadline(t *testing.T) {
	t.Cleanup(runtime.SetCallTimeout(100 * time.Millisecond))
	ad := fake.New(fake.Turn(false))
	ad.HoldStart = true
	n := start(t, dbPath(t), ad)
	thread := newThread(t, n.h)
	submit(t, n.h, thread, dispatch("go", api.StartNow))
	until(t, n.h, thread, "the hung start gives up", idle)
	if got := outcomes(t, n.h, thread); len(got) != 1 || got[0] != api.OutcomeFailed {
		t.Fatalf("hung start ended %v, want failed", got)
	}
}

// The user steers, approves, then stops while the steer hangs. The stop
// must not wait behind either, and the approval must not reach the run.
func TestStopCancelsSteersAndAnswersInFlight(t *testing.T) {
	g := newGated(fake.Turn(true))
	n := start(t, dbPath(t), g)
	thread := newThread(t, n.h)
	submit(t, n.h, thread, dispatch("go", api.StartNow))
	req := until(t, n.h, thread, "approval", waiting).Requests[0]
	entered, never := make(chan struct{}, 1), make(chan struct{})
	g.setGate(func(_ api.ID, a provider.Action) <-chan struct{} {
		if _, ok := a.(provider.Steer); ok {
			entered <- struct{}{}
			return never
		}
		return nil
	})
	steer := api.NewID()
	if _, err := n.h.Submit(ctx, api.Command{ID: steer, Target: thread, Body: dispatch("hurry", api.SteerActive)}); err != nil {
		t.Fatal(err)
	}
	<-entered
	respond(t, n.h, thread, req)
	submit(t, n.h, thread, api.TurnInterrupt{})
	until(t, n.h, thread, "the stop ends the run while the steer still hangs", idle)

	stored, err := n.st.Thread(ctx, thread)
	if err != nil {
		t.Fatal(err)
	}
	m, _ := stored.Message(steer)
	r, _ := stored.Request(req.ID)
	if g.count(provider.Respond{}) != 0 || r.State != api.RequestInvalidated || m.State != engine.QueueUncertain {
		t.Fatalf("after the stop: %d answers reached the agent, request %v, steer %v; want none, invalidated and uncertain",
			g.count(provider.Respond{}), r.State, m.State)
	}
	if got := outcomes(t, n.h, thread); len(got) != 1 || got[0] != api.OutcomeInterrupted {
		t.Fatalf("run ended %v, want interrupted", got)
	}
	assertReplay(t, n.st)
}
