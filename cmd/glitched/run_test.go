package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
	daemonruntime "glitched/internal/runtime"
	"glitched/internal/store"
	"glitched/internal/transport"
)

// engine serves a real host on a unix socket in a fresh state dir and
// returns the dir with a client of it.
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
	h, err := daemonruntime.New(ctx, st, a)
	if err != nil {
		t.Fatal(err)
	}
	lb := socket(dir)
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
	if _, err := ep.Submit(context.Background(), api.Command{ID: api.NewID(), Target: thread, Body: api.ThreadCreate{}}); err != nil {
		t.Fatal(err)
	}
	return thread
}

// waitFor polls the thread's log until it holds an event of type typ.
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
	ctx := context.Background()
	thread := newThread(t, ep)
	if _, err := ep.Submit(ctx, api.Command{ID: api.NewID(), Target: thread, Body: api.MessageDispatch{Text: "first", Mode: api.StartNow}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, ep, thread, "request.opened")
	snap, err := ep.Read(ctx, api.Query{Thread: thread})
	if err != nil {
		t.Fatal(err)
	}
	req := snap.Threads[0].Requests[0]

	done := runAsync(dir, thread, "steer")
	waitFor(t, ep, thread, "message.dispatched")
	answer := api.RequestRespond{Request: req.ID, Generation: req.Generation, Answer: json.RawMessage(`{"allow":true}`)}
	if _, err := ep.Submit(ctx, api.Command{ID: api.NewID(), Target: thread, Body: answer}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("run -mode steer: %v, want it to end with the run it was steered into", err)
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

// refusesSteer is the fake adapter whose agents fail every steer.
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
