package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/daemon"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
)

// idleClaude plays a Claude thread whose turns write a one-hour cache
// six minutes from lapsing, so idle compaction comes due as each ends.
// Its /compact runs until it is stopped.
type idleClaude struct {
	*fake.Adapter
	compacting chan struct{}
	once       sync.Once
}

func (a *idleClaude) Open(_ context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	return &idleAgent{a: a, evs: make(chan provider.Event, 8), done: make(chan struct{})}, nil
}

type idleAgent struct {
	a    *idleClaude
	evs  chan provider.Event
	done chan struct{}
}

func (g *idleAgent) Do(_ context.Context, act provider.Action) error {
	switch act := act.(type) {
	case provider.Start:
		if act.Text == "/compact" {
			g.a.once.Do(func() { close(g.a.compacting) })
			return nil
		}
		g.evs <- provider.Context{Used: 250000, Window: 1000000, CacheExpires: time.Now().Add(6 * time.Minute)}
		g.evs <- provider.TextDelta{Text: "done"}
		g.evs <- provider.TurnEnded{Outcome: api.OutcomeCompleted}
	case provider.Interrupt:
		g.evs <- provider.TurnEnded{Outcome: api.OutcomeInterrupted}
	}
	return nil
}

func (g *idleAgent) Next(ctx context.Context) (provider.Event, error) {
	select {
	case e := <-g.evs:
		return e, nil
	case <-g.done:
		return nil, context.Canceled
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (g *idleAgent) Close(context.Context) error {
	select {
	case <-g.done:
	default:
		close(g.done)
	}
	return nil
}

// An idle compaction shows nothing at work: the thread isn't Working, the
// composer offers Send and no Stop, and the row's Snooze and Mark settled
// stay offered.
func TestAnIdleCompactionShowsNothingAtWork(t *testing.T) {
	dir := tempDir(t)
	ad := &idleClaude{Adapter: fake.New(nil), compacting: make(chan struct{})}
	d := engine(t, daemon.Config{Dir: dir}, ad)
	const title = "Cache the sidebar"
	id := api.NewID()
	ep := d.Endpoint()
	for _, b := range []api.Intent{
		api.ThreadCreate{Workspace: "/work/t3code", Title: title, Model: api.ModelRef{Provider: "claude"}},
		api.MessageDispatch{Text: "Cache it.", Mode: api.StartNow},
	} {
		if _, err := ep.Submit(context.Background(), api.Command{ID: api.NewID(), Target: id, Body: b}); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-ad.compacting:
	case <-time.After(30 * time.Second):
		t.Fatal("the idle compaction never started")
	}
	h := start(t, dir)
	th := h.thread(title)
	h.a.select_(th)
	h.until("the silent run", func() bool { return th.View.Turn != nil && th.View.Turn.Silent })
	h.tt.Advance(time.Second)
	if th.View.Working() {
		t.Error("a thread compacting while idle is Working")
	}
	if got := h.a.statusOf(th); got == statusWorking {
		t.Error("a thread compacting while idle shows as working")
	}
	if h.tt.HasText("Stop") {
		t.Error("the composer offers Stop for an idle compaction")
	}
	if err := h.tt.RightClick(title); err != nil {
		t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem("Snooze", "Tomorrow"); err != nil {
		t.Fatalf("Snooze while compacting: %v", err)
	}
	h.until("the snooze", func() bool { return th.View.Disposition.State == api.Snoozed })
}
