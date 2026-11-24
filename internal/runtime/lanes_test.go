package runtime_test

import (
	"context"
	"errors"
	"slices"
	"sync"
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

// slowCloser records opens and closes in order; its agents take a while to
// close, like a CLI that is slow to exit.
type slowCloser struct {
	*gated
	mu  sync.Mutex
	log []string
}

func (a *slowCloser) note(s string) {
	a.mu.Lock()
	a.log = append(a.log, s)
	a.mu.Unlock()
}

func (a *slowCloser) at(s string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Index(a.log, s)
}

func (a *slowCloser) Open(ctx context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	ag, err := a.gated.Open(ctx, spec)
	a.note("open " + spec.Run.String())
	return &slowClose{ag, a, spec.Run}, err
}

type slowClose struct {
	provider.Agent
	a   *slowCloser
	run api.ID
}

func (s *slowClose) Close(ctx context.Context) error {
	time.Sleep(200 * time.Millisecond)
	s.a.note("closed " + s.run.String())
	return s.Agent.Close(ctx)
}

func TestAnInterruptThatTimesOutLeavesTheRunUncertain(t *testing.T) {
	t.Cleanup(runtime.SetCallTimeout(100 * time.Millisecond))
	ad := &slowCloser{gated: newGated(fake.Turn(true))}
	n := start(t, dbPath(t), ad)
	thread := newThread(t, n.h)
	first, second := api.NewID(), api.NewID()
	if _, err := n.h.Submit(ctx, api.Command{ID: first, Target: thread, Body: dispatch("first", api.StartNow)}); err != nil {
		t.Fatal(err)
	}
	until(t, n.h, thread, "approval", waiting)
	never := make(chan struct{})
	ad.setGate(func(_ api.ID, a provider.Action) <-chan struct{} {
		if _, ok := a.(provider.Interrupt); ok {
			return never
		}
		return nil
	})
	if _, err := n.h.Submit(ctx, api.Command{ID: second, Target: thread, Body: dispatch("second", api.AfterActive)}); err != nil {
		t.Fatal(err)
	}
	submit(t, n.h, thread, api.TurnInterrupt{})
	until(t, n.h, thread, "the unanswered interrupt leaves the run uncertain", uncertain)
	if got := outcomes(t, n.h, thread); len(got) != 0 {
		t.Fatalf("an interrupt that timed out ended the run %v, but nobody knows the agent stopped", got)
	}

	ad.setGate(nil)
	submit(t, n.h, thread, api.TurnInterrupt{})
	submit(t, n.h, thread, api.QueueResume{})
	until(t, n.h, thread, "the next run asks", func(th api.Thread) bool { return waiting(th) && th.Requests[0].Run == second })
	if closed, opened := ad.at("closed "+first.String()), ad.at("open "+second.String()); closed < 0 || opened < closed {
		t.Fatalf("the next agent opened before the last one closed: %v", ad.log)
	}
}

type stuckCloser struct {
	*gated
	mu    sync.Mutex
	stuck bool
}

func (a *stuckCloser) Open(ctx context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	ag, err := a.gated.Open(ctx, spec)
	return &stuckClose{ag, a}, err
}

func (a *stuckCloser) unstick() {
	a.mu.Lock()
	a.stuck = false
	a.mu.Unlock()
}

type stuckClose struct {
	provider.Agent
	a *stuckCloser
}

func (s *stuckClose) Close(ctx context.Context) error {
	s.a.mu.Lock()
	stuck := s.a.stuck
	s.a.mu.Unlock()
	if stuck {
		return errors.New("close failed")
	}
	return s.Agent.Close(ctx)
}

// abandonForNext leaves first uncertain behind an interrupt it never takes,
// then abandons it and resumes the queue the stop held, which admits second.
func abandonForNext(t *testing.T, n node, ad *stuckCloser, thread, first, second api.ID) {
	t.Helper()
	if _, err := n.h.Submit(ctx, api.Command{ID: first, Target: thread, Body: dispatch("first", api.StartNow)}); err != nil {
		t.Fatal(err)
	}
	until(t, n.h, thread, "approval", waiting)
	never := make(chan struct{})
	ad.setGate(func(_ api.ID, a provider.Action) <-chan struct{} {
		if _, ok := a.(provider.Interrupt); ok {
			return never
		}
		return nil
	})
	if _, err := n.h.Submit(ctx, api.Command{ID: second, Target: thread, Body: dispatch("second", api.AfterActive)}); err != nil {
		t.Fatal(err)
	}
	submit(t, n.h, thread, api.TurnInterrupt{})
	until(t, n.h, thread, "uncertain", uncertain)
	ad.setGate(nil)
	submit(t, n.h, thread, api.TurnInterrupt{})
	submit(t, n.h, thread, api.QueueResume{})
	until(t, n.h, thread, "the next run is admitted", func(th api.Thread) bool { return th.Run == api.RunStarting })
}

func TestAFailedCloseKeepsTheNextAgentFromStarting(t *testing.T) {
	t.Cleanup(runtime.SetCallTimeout(100 * time.Millisecond))
	ad := &stuckCloser{gated: newGated(fake.Turn(true)), stuck: true}
	n := start(t, dbPath(t), ad)
	thread := newThread(t, n.h)
	first, second := api.NewID(), api.NewID()
	abandonForNext(t, n, ad, thread, first, second)

	time.Sleep(300 * time.Millisecond)
	if got := ad.Starts(); got != 1 {
		t.Fatalf("%d agents started while the first one failed to close, want 1", got)
	}
	ad.unstick()
	until(t, n.h, thread, "the next run asks", func(th api.Thread) bool { return waiting(th) && th.Requests[0].Run == second })
	if rec, err := ad.Adapter.Reconcile(ctx, provider.RuntimeIdentity{Thread: thread, Run: first}); err != nil {
		t.Fatal(err)
	} else if _, alive := rec.(provider.Restored); alive {
		t.Fatal("the first agent is still open after the next one started")
	}
}

// A start that waits for the thread's last agent to close has not reached the
// provider, so a restart meanwhile leaves it to start, not uncertain.
func TestAStartWaitingOnTheLastAgentSurvivesARestart(t *testing.T) {
	t.Cleanup(runtime.SetCallTimeout(100 * time.Millisecond))
	path := dbPath(t)
	ad := &stuckCloser{gated: newGated(fake.Turn(true)), stuck: true}
	n := start(t, path, ad)
	thread := newThread(t, n.h)
	first, second := api.NewID(), api.NewID()
	abandonForNext(t, n, ad, thread, first, second)
	time.Sleep(50 * time.Millisecond)
	n.kill()

	fresh := fake.New(fake.Turn(true))
	n = start(t, path, fresh)
	th := until(t, n.h, thread, "the next run settles", func(th api.Thread) bool { return uncertain(th) || waiting(th) })
	if uncertain(th) {
		t.Fatal("a start that never reached the provider is uncertain after a restart")
	}
	if th.Requests[0].Run != second || fresh.Starts() != 1 {
		t.Fatalf("the new host asks for run %s after %d starts, want %s after 1", th.Requests[0].Run, fresh.Starts(), second)
	}
}

// A steer sent while the stop is still on its way to the agent is refused,
// so no second turn starts once the run ends.
func TestASteerDuringAStopIsRefused(t *testing.T) {
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
	_, err := n.h.Submit(ctx, api.Command{ID: api.NewID(), Target: thread, Body: dispatch("hurry", api.SteerActive)})
	if rej := (*api.RejectedError)(nil); !errors.As(err, &rej) {
		t.Fatalf("a steer while the stop is in flight: %v, want rejected", err)
	}
	close(release)
	th := until(t, n.h, thread, "the stop ends the run", idle)
	if got := outcomes(t, n.h, thread); th.Queued != 0 || len(got) != 1 {
		t.Fatalf("after the stop: %d queued, runs ended %v; want none queued and one run", th.Queued, got)
	}
	assertReplay(t, n.st)
}

// lateStart opens the fake's agents to take their Start only once the
// start's context ends, as a CLI that already read the prompt does.
type lateStart struct {
	*fake.Adapter
	entered    chan struct{}
	once       sync.Once
	mu         sync.Mutex
	interrupts int
}

func (s *lateStart) Open(ctx context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	g, err := s.Adapter.Open(ctx, spec)
	return &lateStartAgent{Agent: g, s: s}, err
}

func (s *lateStart) interrupted() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.interrupts
}

type lateStartAgent struct {
	provider.Agent
	s *lateStart
}

func (g *lateStartAgent) Do(ctx context.Context, act provider.Action) error {
	switch act.(type) {
	case provider.Start:
		g.s.once.Do(func() { close(g.s.entered) })
		<-ctx.Done()
		return g.Agent.Do(context.Background(), act)
	case provider.Interrupt:
		g.s.mu.Lock()
		g.s.interrupts++
		g.s.mu.Unlock()
	}
	return g.Agent.Do(ctx, act)
}

// A stop cancels a start in flight, but a start can take anyway: the stop
// then goes to the agent it gave, and the run ends on that agent's word.
func TestAStopReachesAStartThatTookAfterIt(t *testing.T) {
	h := &heard{}
	ad := &lateStart{Adapter: fake.New(fake.Turn(true)), entered: make(chan struct{})}
	n := awakeHost(t, dbPath(t), ad, h)
	thread := newThread(t, n.h)
	submit(t, n.h, thread, dispatch("go", api.StartNow))
	<-ad.entered
	submit(t, n.h, thread, api.TurnInterrupt{})
	h.until(t, thread, "the stopped run's end", idle)
	evs := events(t, n.h, thread)
	if got := outcomes(t, n.h, thread); !slices.Equal(got, []api.Outcome{api.OutcomeInterrupted}) || ad.interrupted() != 1 || count(evs, "turn.signalled") != 1 {
		t.Fatalf("outcomes %v after %d interrupts reached the agent, %d turn.signalled; want interrupted after 1, signalled once", got, ad.interrupted(), count(evs, "turn.signalled"))
	}
	if got := h.all(); !slices.Equal(got, []bool{true, false}) {
		t.Fatalf("told %v once the run ended, want [true false]", got)
	}
	assertReplay(t, n.st)
}
