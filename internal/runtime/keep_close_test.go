package runtime_test

import (
	"context"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
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
