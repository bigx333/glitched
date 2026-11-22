package runtime_test

import (
	"context"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
	"glitched/internal/store"
)

type closingAdapter struct {
	provider.Adapter
	entered, release chan struct{}
}

func (a closingAdapter) Open(ctx context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	ag, err := a.Adapter.Open(ctx, spec)
	if err != nil {
		return nil, err
	}
	return closingAgent{Agent: ag, entered: a.entered, release: a.release}, nil
}

type closingAgent struct {
	provider.Agent
	entered, release chan struct{}
}

func (a closingAgent) Close(ctx context.Context) error {
	close(a.entered)
	select {
	case <-a.release:
		return a.Agent.Close(ctx)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestARetriedChildCreateGetsItsReceiptWhileTheParentCloses(t *testing.T) {
	a := closingAdapter{
		Adapter: fake.New(fake.Turn(true)),
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	n := start(t, dbPath(t), a)
	defer close(a.release)
	thread := newThread(t, n.h)
	run := submit(t, n.h, thread, dispatch("first", api.StartNow)).Command
	until(t, n.h, thread, "first turn waiting", waiting)
	create := api.Command{ID: api.NewID(), Target: api.NewID(), Body: api.ThreadCreate{
		Workspace: "/src/app", Title: "child", Model: api.ModelRef{Provider: "fake", Model: "fake"},
		Parent: thread, Run: run,
	}}
	first, err := n.h.Submit(ctx, create)
	if err != nil {
		t.Fatal(err)
	}
	submit(t, n.h, thread, api.TurnInterrupt{})
	select {
	case <-a.entered:
	case <-time.After(time.Second):
		t.Fatal("first turn did not enter Close")
	}
	replayed, err := n.h.Submit(ctx, create)
	if err != nil || replayed != first {
		t.Fatalf("exact replay: got receipt=%+v error=%v, want original receipt=%+v", replayed, err, first)
	}
}

func TestDeletingAThreadNoAgentEverServedDrainsAcrossARestart(t *testing.T) {
	path := dbPath(t)
	n := serve(t, path, provider.Mux{})
	thread := threadOn(t, n.h, "missing")
	run := submit(t, n.h, thread, dispatch("first", api.StartNow)).Command
	until(t, n.h, thread, "the first start failed because no adapter exists", func(th api.Thread) bool {
		return idle(th) && ended(t, n.h, thread, run)
	})
	n.kill()

	offline(t, path, thread, false, dispatch("second", api.StartNow), api.ThreadDelete{})
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := st.Thread(ctx, thread)
	st.Close()
	if err != nil || !projection.Deleted || projection.Generation != 2 {
		t.Fatalf("expected tombstone of second generation, got %+v, %v", projection, err)
	}
	msg, _ := projection.Message(projection.Runs[1].ID)
	if msg.State != engine.QueueCancelled {
		t.Fatalf("second run's message state is %s, want cancelled", msg.State)
	}
	serve(t, path, provider.Mux{})
	state := closeState(t, path)
	deadline := time.Now().Add(1500 * time.Millisecond)
	for got := state(); got != "done"; got = state() {
		if time.Now().After(deadline) {
			t.Fatalf("agent.close=%s after restart; neither generation had an agent, want done", got)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
