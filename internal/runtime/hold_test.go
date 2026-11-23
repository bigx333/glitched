package runtime_test

import (
	"context"
	"errors"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
	"glitched/internal/runtime"
)

// A Hold, as an update of the provider's CLI takes, is refused while a
// turn of the provider runs, or waits for it to end; and a turn asked for
// while held starts once the hold is released.
func TestHoldWaitsForTheProvidersTurns(t *testing.T) {
	ad := fake.New(fake.Turn(true))
	n := start(t, dbPath(t), ad)
	thread := newThread(t, n.h)
	submit(t, n.h, thread, dispatch("look", api.StartNow))
	th := until(t, n.h, thread, "the approval", waiting)

	if _, err := n.h.Hold(ctx, "fake", false); !errors.Is(err, runtime.ErrBusy) {
		t.Fatalf("Hold while a turn runs: %v, want ErrBusy", err)
	}
	release, err := n.h.Hold(ctx, "other", false)
	if err != nil {
		t.Fatalf("Hold of a provider with no turn: %v", err)
	}
	release()

	held := make(chan func(), 1)
	go func() {
		release, err := n.h.Hold(ctx, "fake", true)
		if err != nil {
			t.Error(err)
		}
		held <- release
	}()
	select {
	case <-held:
		t.Fatal("a Hold that waits returned while the turn runs")
	case <-time.After(300 * time.Millisecond):
	}
	respond(t, n.h, thread, th.Requests[0])
	until(t, n.h, thread, "the turn's end", idle)
	release = <-held

	starts := ad.Starts()
	submit(t, n.h, thread, dispatch("again", api.StartNow))
	time.Sleep(300 * time.Millisecond)
	if ad.Starts() != starts {
		t.Fatal("a turn started while its provider was held")
	}
	if _, err := n.h.Hold(ctx, "fake", false); !errors.Is(err, runtime.ErrBusy) {
		t.Errorf("a second Hold while a turn waits on the first: %v, want ErrBusy", err)
	}
	release()
	until(t, n.h, thread, "the turn held back", waiting)
	if ad.Starts() != starts+1 {
		t.Errorf("%d starts after the release, want %d", ad.Starts(), starts+1)
	}
}

// A run whose reconcile failed at boot is uncertain: this host doesn't
// drive it, but its process may still run, so Hold counts it.
func TestHoldCountsARunAFailedReconcileLeft(t *testing.T) {
	n, _, _, _ := crashWithAStuckRun(t, dbPath(t))
	if release, err := n.h.Hold(ctx, "b", false); !errors.Is(err, runtime.ErrBusy) {
		if release != nil {
			release()
		}
		t.Fatalf("Hold with an uncertain run of the provider: %v, want ErrBusy", err)
	}
}

// A run stopped while uncertain leaves a close that retries the failed
// reconcile, and the provider's process may run till it succeeds.
func TestHoldCountsTheCloseOfAStoppedUncertainRun(t *testing.T) {
	n, b, thread, run := crashWithAStuckRun(t, dbPath(t))
	submit(t, n.h, thread, api.TurnInterrupt{})
	until(t, n.h, thread, "idle", idle)
	retried(t, b, run, 2)
	if release, err := n.h.Hold(ctx, "b", false); !errors.Is(err, runtime.ErrBusy) {
		if release != nil {
			release()
		}
		t.Fatalf("Hold while the stopped run's close retries: %v, want ErrBusy", err)
	}
	b.fail.Store(false)
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	release, err := n.h.Hold(c, "b", true)
	if err != nil {
		t.Fatalf("Hold once the close is done: %v", err)
	}
	release()
}

// An agent still closing after its run ended may still run its process.
func TestHoldCountsAnAgentStillClosing(t *testing.T) {
	ad := &stuckCloser{gated: newGated(fake.Turn(false)), stuck: true}
	n := start(t, dbPath(t), ad)
	thread := newThread(t, n.h)
	submit(t, n.h, thread, dispatch("look", api.StartNow))
	until(t, n.h, thread, "the turn's end", idle)
	if release, err := n.h.Hold(ctx, "fake", false); !errors.Is(err, runtime.ErrBusy) {
		if release != nil {
			release()
		}
		t.Fatalf("Hold while the turn's agent is still closing: %v, want ErrBusy", err)
	}
	ad.unstick()
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	release, err := n.h.Hold(c, "fake", true)
	if err != nil {
		t.Fatalf("Hold once the agent closed: %v", err)
	}
	release()
}

func TestAHoldAskedForByAGoneCallerHoldsNothing(t *testing.T) {
	n := start(t, dbPath(t), fake.New(nil))
	c, cancel := context.WithCancel(ctx)
	cancel()
	if release, err := n.h.Hold(c, "fake", false); !errors.Is(err, context.Canceled) {
		if release != nil {
			release()
		}
		t.Fatalf("Hold on a cancelled context: %v", err)
	}
}

func TestAHoldThatWaitsEndsWithItsContext(t *testing.T) {
	n := start(t, dbPath(t), fake.New(fake.Turn(true)))
	thread := newThread(t, n.h)
	submit(t, n.h, thread, dispatch("look", api.StartNow))
	until(t, n.h, thread, "the approval", waiting)
	c, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	if _, err := n.h.Hold(c, "fake", true); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Hold past its deadline: %v", err)
	}
}

// A run whose reconcile found its process gone is uncertain only in its
// transcript, and has no close to wait for: Hold doesn't wait on it.
func TestHoldDoesntWaitOnARunWhoseProcessIsGone(t *testing.T) {
	path := dbPath(t)
	ad := fake.New(fake.Turn(true))
	n := start(t, path, ad)
	thread := newThread(t, n.h)
	run := submit(t, n.h, thread, dispatch("work", api.StartNow)).Command
	until(t, n.h, thread, "approval", waiting)
	n.kill()
	id := provider.RuntimeIdentity{Thread: thread, Run: run}
	r, err := ad.Reconcile(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.(provider.Restored).Agent.Close(ctx); err != nil {
		t.Fatal(err)
	}
	n = start(t, path, ad)
	until(t, n.h, thread, "uncertain", uncertain)
	c, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	release, err := n.h.Hold(c, "fake", true)
	if err != nil {
		t.Fatalf("Hold with the run's process gone and no close: %v", err)
	}
	release()
}

// pauseAtThread is a ctx that stops the first store.Thread read made with
// it until resume closes: a Hold paused there has read the store, and not
// yet the host's agents.
type pauseAtThread struct {
	context.Context
	once            sync.Once
	reached, resume chan struct{}
}

func (c *pauseAtThread) Done() <-chan struct{} {
	var pcs [32]uintptr
	frames := goruntime.CallersFrames(pcs[:goruntime.Callers(2, pcs[:])])
	driver, thread := false, false
	for more := true; more; {
		var f goruntime.Frame
		f, more = frames.Next()
		driver = driver || strings.Contains(f.Function, "modernc.org/sqlite.(*stmt).query")
		thread = thread || f.Function == "glitched/internal/store.(*Store).Thread"
	}
	if driver && thread {
		c.once.Do(func() {
			close(c.reached)
			select {
			case <-c.resume:
			case <-c.Context.Done():
			}
		})
	}
	return c.Context.Done()
}

// startsSlowly is the fake, whose Start returns only once resume closes.
type startsSlowly struct {
	*fake.Adapter
	sent, resume chan struct{}
}

func (a startsSlowly) Open(ctx context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	ag, err := a.Adapter.Open(ctx, spec)
	if err != nil {
		return nil, err
	}
	return slowStart{ag, a}, nil
}

type slowStart struct {
	provider.Agent
	a startsSlowly
}

func (g slowStart) Do(ctx context.Context, a provider.Action) error {
	err := g.Agent.Do(ctx, a)
	if _, ok := a.(provider.Start); ok {
		close(g.a.sent)
		<-g.a.resume
	}
	return err
}

// A run deleted while its agent starts is forgotten at once, but its agent
// runs until the start returns and it is closed: a Hold that read the
// store before the run began still finds it among the host's agents.
func TestHoldFindsAnAgentStillStartingAfterItsRunIsDeleted(t *testing.T) {
	p := startsSlowly{fake.New(fake.Turn(true)), make(chan struct{}), make(chan struct{})}
	n := serve(t, dbPath(t), provider.Mux{"target": p, "other": fake.New(fake.Turn(true))})
	defer close(p.resume)
	other := threadOn(t, n.h, "other")
	submit(t, n.h, other, dispatch("other turn", api.StartNow))
	until(t, n.h, other, "the other provider's approval", waiting)
	target := threadOn(t, n.h, "target")

	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	pause := &pauseAtThread{Context: c, reached: make(chan struct{}), resume: make(chan struct{})}
	got := make(chan error, 1)
	go func() {
		release, err := n.h.Hold(pause, "target", false)
		if release != nil {
			release()
		}
		got <- err
	}()
	select {
	case <-pause.reached:
	case <-c.Done():
		t.Fatal("the Hold never read the store")
	}
	submit(t, n.h, target, dispatch("target turn", api.StartNow))
	select {
	case <-p.sent:
	case <-c.Done():
		t.Fatal("the agent never got its start")
	}
	submit(t, n.h, target, api.ThreadDelete{})
	close(pause.resume)
	select {
	case err := <-got:
		if !errors.Is(err, runtime.ErrBusy) {
			t.Fatalf("Hold with the deleted run's agent still starting: %v, want ErrBusy", err)
		}
	case <-c.Done():
		t.Fatal("Hold never returned")
	}
}

// interruptFails is the fake, whose agents take no interrupt and never
// close, as a CLI that hangs.
type interruptFails struct {
	*fake.Adapter
	closing chan struct{}
	once    sync.Once
}

func (a *interruptFails) Open(ctx context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	ag, err := a.Adapter.Open(ctx, spec)
	if err != nil {
		return nil, err
	}
	return unclosed{ag, a}, nil
}

type unclosed struct {
	provider.Agent
	a *interruptFails
}

func (g unclosed) Do(ctx context.Context, action provider.Action) error {
	if _, ok := action.(provider.Interrupt); ok {
		return errors.New("the agent took no interrupt")
	}
	return g.Agent.Do(ctx, action)
}

func (g unclosed) Close(context.Context) error {
	g.a.once.Do(func() { close(g.a.closing) })
	return errors.New("the agent's process still runs")
}

// A run left uncertain by an interrupt that failed has no close in the
// store: after a restart, which doesn't reconcile it, Hold asks its
// adapter whether its agent still runs.
func TestHoldAsksAfterARestartWhetherAnUncertainRunsAgentRuns(t *testing.T) {
	path := dbPath(t)
	ad := &interruptFails{Adapter: fake.New(fake.Turn(true)), closing: make(chan struct{})}
	n := start(t, path, ad)
	thread := newThread(t, n.h)
	submit(t, n.h, thread, dispatch("work", api.StartNow))
	until(t, n.h, thread, "approval", waiting)
	submit(t, n.h, thread, api.TurnInterrupt{})
	until(t, n.h, thread, "uncertain", uncertain)
	select {
	case <-ad.closing:
	case <-time.After(2 * time.Second):
		t.Fatal("the agent's close was never tried")
	}
	n.kill()

	n = start(t, path, ad)
	if release, err := n.h.Hold(ctx, "fake", false); !errors.Is(err, runtime.ErrBusy) {
		if release != nil {
			release()
		}
		t.Fatalf("Hold with the uncertain run's agent still running: %v, want ErrBusy", err)
	}
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	release, err := n.h.Hold(c, "fake", true)
	if err != nil {
		t.Fatalf("Hold once the agent nobody drove was closed: %v", err)
	}
	release()
}
