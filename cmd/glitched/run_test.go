package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/daemon"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
	daemonruntime "glitched/internal/runtime"
	"glitched/internal/store"
	"glitched/internal/transport"
)

func engine(t *testing.T, a provider.Adapter) (string, *transport.EndpointClient) {
	t.Helper()
	dir, err := os.MkdirTemp("", "gl")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	st, err := store.Open(filepath.Join(dir, "engine.db"))
	if err != nil {
		t.Fatal(err)
	}
	h, err := daemonruntime.New(ctx, st, provider.Mux{"fake": a})
	if err != nil {
		t.Fatal(err)
	}
	lb := daemon.Socket(dir)
	ln, err := lb.Listen(ctx)
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- (&transport.Server{Transport: lb, Endpoint: h}).Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		<-served
		h.Close()
		st.Close()
		os.RemoveAll(dir)
	})
	return dir, transport.NewEndpointClient(lb, api.ID{})
}

func newThread(t *testing.T, ep *transport.EndpointClient) api.ID {
	t.Helper()
	thread := api.NewID()
	if _, err := ep.Submit(context.Background(), api.Command{ID: api.NewID(), Target: thread, Body: api.ThreadCreate{Model: api.ModelRef{Provider: "fake"}}}); err != nil {
		t.Fatal(err)
	}
	return thread
}

func waitFor(t *testing.T, ep *transport.EndpointClient, thread api.ID, typ string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		snap, err := ep.Read(context.Background(), api.Query{Thread: thread})
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range snap.Events {
			if e.Type == typ {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no %s on the thread", typ)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func runAsync(dir string, thread api.ID, mode string) <-chan error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	done := make(chan error, 1)
	go func() {
		defer cancel()
		done <- run(ctx, []string{"-dir", dir, "-thread", thread.String(), "-mode", mode, "-yes", "nudge"})
	}()
	return done
}

func TestRunFollowsASteeredMessageIntoTheLiveRun(t *testing.T) {
	dir, ep := engine(t, fake.New(fake.Turn(true)))
	thread := newThread(t, ep)
	if _, err := ep.Submit(context.Background(), api.Command{ID: api.NewID(), Target: thread, Body: api.MessageDispatch{Text: "first", Mode: api.StartNow}}); err != nil {
		t.Fatal(err)
	}
	// The run asks before the steering command subscribes, so the command
	// must find the open request itself and answer it with -yes.
	waitFor(t, ep, thread, "request.opened")
	if err := <-runAsync(dir, thread, "steer"); err != nil {
		t.Fatalf("run -mode steer: %v, want it to answer and end with the run it was steered into", err)
	}
}

// glitched run on a settled thread brings it back to the inbox and runs
// the message, as the GUI's composer does.
func TestRunOnASettledThreadBringsItBack(t *testing.T) {
	ad := fake.New(fake.Turn(false))
	dir, ep := engine(t, ad)
	thread := newThread(t, ep)
	if _, err := ep.Submit(context.Background(), api.Command{ID: api.NewID(), Target: thread, Body: api.InboxSet{Disposition: api.Disposition{State: api.Settled}}}); err != nil {
		t.Fatal(err)
	}
	err := <-runAsync(dir, thread, "after")
	snap, readErr := ep.Read(context.Background(), api.Query{Thread: thread})
	if readErr != nil {
		t.Fatal(readErr)
	}
	if th := snap.Threads[0]; err != nil || th.Disposition.State != api.Open || ad.Starts() != 1 {
		t.Fatalf("run: %v; the thread is %v after %d starts, want it back in the inbox after one", err, th.Disposition.State, ad.Starts())
	}
}

func TestRunReturnsOnceADeferredMessageIsHeld(t *testing.T) {
	dir, ep := engine(t, fake.New(fake.Turn(false)))
	thread := newThread(t, ep)
	if err := <-runAsync(dir, thread, "defer"); err != nil {
		t.Fatalf("run -mode defer: %v", err)
	}
	snap, err := ep.Read(context.Background(), api.Query{Thread: thread})
	if err != nil {
		t.Fatal(err)
	}
	if th := snap.Threads[0]; th.Queued != 1 || th.Run != api.RunIdle {
		t.Fatalf("thread after defer: queued %d, run %d; want 1 held message and no run", th.Queued, th.Run)
	}
}

type refusesSteer struct{ *fake.Adapter }

func (a refusesSteer) Open(ctx context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	g, err := a.Adapter.Open(ctx, spec)
	return steerless{g}, err
}

type steerless struct{ provider.Agent }

func (g steerless) Do(ctx context.Context, act provider.Action) error {
	if _, ok := act.(provider.Steer); ok {
		return errors.New("steering is not supported")
	}
	return g.Agent.Do(ctx, act)
}

func TestRunEndsWhenItsMessageIsCancelled(t *testing.T) {
	dir, ep := engine(t, refusesSteer{fake.New(fake.Turn(true))})
	thread := newThread(t, ep)
	if _, err := ep.Submit(context.Background(), api.Command{ID: api.NewID(), Target: thread, Body: api.MessageDispatch{Text: "first", Mode: api.StartNow}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, ep, thread, "request.opened")
	err := <-runAsync(dir, thread, "steer")
	if err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("run -mode steer refused by the agent: %v, want the cancellation", err)
	}
}

type steerTimesOut struct{ *fake.Adapter }

func (a steerTimesOut) Open(ctx context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	g, err := a.Adapter.Open(ctx, spec)
	return steerLost{g}, err
}

type steerLost struct{ provider.Agent }

func (g steerLost) Do(ctx context.Context, act provider.Action) error {
	if _, ok := act.(provider.Steer); ok {
		return context.DeadlineExceeded
	}
	return g.Agent.Do(ctx, act)
}

func TestRunReportsASteerThatMayNotHaveArrived(t *testing.T) {
	dir, ep := engine(t, steerTimesOut{fake.New(fake.Turn(true))})
	thread := newThread(t, ep)
	if _, err := ep.Submit(context.Background(), api.Command{ID: api.NewID(), Target: thread, Body: api.MessageDispatch{Text: "first", Mode: api.StartNow}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, ep, thread, "request.opened")
	err := <-runAsync(dir, thread, "steer")
	if err == nil || !strings.Contains(err.Error(), "may or may not") {
		t.Fatalf("run -mode steer whose delivery is unknown: %v, want an error saying so", err)
	}
}

// olderEngine answers Info as an engine of protocol 1 does, and nothing
// else.
type olderEngine struct{ api.Endpoint }

func (olderEngine) Info(context.Context) (api.Environment, error) {
	return api.Environment{ID: api.NewID(), Name: "older", Protocol: 1}, nil
}

// The CLI refuses an engine of another protocol before it reads or sends
// anything.
func TestTheCLIRefusesAnEngineOfAnotherProtocol(t *testing.T) {
	dir, err := os.MkdirTemp("", "gl")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	lb := daemon.Socket(dir)
	ln, err := lb.Listen(ctx)
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- (&transport.Server{Transport: lb, Endpoint: olderEngine{}}).Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		<-served
		os.RemoveAll(dir)
	})
	if err := threads(ctx, []string{"-dir", dir}); !errors.Is(err, api.ErrProtocol) {
		t.Errorf("threads on a protocol 1 engine: %v, want api.ErrProtocol", err)
	}
	if err := run(ctx, []string{"-dir", dir, "hello"}); !errors.Is(err, api.ErrProtocol) {
		t.Errorf("run on a protocol 1 engine: %v, want api.ErrProtocol", err)
	}
}
