package runtime_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
	"glitched/internal/provider"
	"glitched/internal/runtime"
	"glitched/internal/store"
)

func TestCompactionsLeftByARestartAreNotSent(t *testing.T) {
	now := time.Now().UTC()
	path := dbPath(t)
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var threads, prior []api.ID
	for i := range 4 {
		id, run := idleSeed(t, st, now.Add(6*time.Minute-time.Duration(i+1)*time.Second))
		threads, prior = append(threads, id), append(prior, run)
	}
	for i := range 2 {
		if _, err := st.Observe(ctx, threads[i], engine.IdleCompact{Run: api.NewID(), AfterRun: prior[i]}, now, 0); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()

	gate := make(chan struct{})
	defer close(gate)
	ad := &compactAdapter{onStart: func(provider.SessionSpec, string) ([]provider.Event, <-chan struct{}) {
		return nil, gate
	}}
	serveOpts(t, path, provider.Mux{"claude": ad}, runtime.WithClock(func() time.Time { return now }))
	for deadline := time.Now().Add(5 * time.Second); len(compactions(ad)) < 2; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%d compactions started after the restart, want 2", len(compactions(ad)))
		}
	}
	for deadline := time.Now().Add(500 * time.Millisecond); ; time.Sleep(5 * time.Millisecond) {
		if count := len(compactions(ad)); count > 2 {
			t.Fatalf("%d /compact prompts started after restart while no compaction had ended; want at most 2", count)
		}
		if time.Now().After(deadline) {
			return
		}
	}
}

func TestTheCompactionWhoseWindowClosesFirstGoesFirst(t *testing.T) {
	now := time.Now().UTC()
	path := dbPath(t)
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		idleSeed(t, st, now.Add(6*time.Minute-time.Duration(i+5)*time.Second))
	}
	gemini, run := api.NewID(), api.NewID()
	finished := now.Add(-13*time.Minute - 31*time.Second)
	model := api.ModelRef{Provider: "antigravity", Model: "gemini-3-pro"}
	for _, command := range []api.Command{
		{ID: api.NewID(), Target: gemini, Body: api.ThreadCreate{Workspace: t.TempDir(), Title: "Gemini", Model: model, Access: api.AccessAsk}},
		{ID: run, Target: gemini, Body: dispatch("work", api.StartNow)},
	} {
		if _, err := st.Submit(ctx, command, finished); err != nil {
			t.Fatal(err)
		}
	}
	start, ok, err := st.Lease(ctx, func() []api.ID { return nil })
	if err != nil || !ok {
		t.Fatalf("lease Gemini's turn: %v, %v", ok, err)
	}
	for _, o := range []engine.Observation{
		engine.Bound{Run: run, Session: "gemini-session"},
		engine.Started{Run: run},
		engine.Context{Run: run, Model: model.Model, Used: 250_000, Window: 1_000_000},
		engine.Ended{Run: run, Outcome: api.OutcomeCompleted},
	} {
		var done int64
		if _, ok := o.(engine.Started); ok {
			done = start.ID
		}
		if _, err := st.Observe(ctx, gemini, o, finished, done); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()

	gate := make(chan struct{})
	defer close(gate)
	ad := &compactAdapter{onStart: func(provider.SessionSpec, string) ([]provider.Event, <-chan struct{}) { return nil, gate }}
	n := serveOpts(t, path, provider.Mux{"claude": ad, "antigravity": ad}, runtime.WithClock(func() time.Time { return now }))
	until(t, n.h, gemini, "two compactions to start", func(api.Thread) bool { return len(compactions(ad)) >= 2 })
	starts := compactions(ad)
	// Gemini's cache lapses first, in 89s, but its window stays open until
	// 30s before that; both Claude windows close sooner, 5 minutes before
	// their caches, at 54s and 55s. The one that would miss its window goes.
	if slices.ContainsFunc(starts[:2], func(s recordedStart) bool { return s.spec.Thread == gemini }) {
		t.Fatalf("first two providers: %s, %s; want both Claude, whose windows close first", starts[0].spec.Model.Provider, starts[1].spec.Model.Provider)
	}
}

type brokenInterrupt struct{ *compactAdapter }

func (a brokenInterrupt) Open(c context.Context, s provider.SessionSpec) (provider.Agent, error) {
	g, err := a.compactAdapter.Open(c, s)
	if err != nil || !s.ResumeOnly {
		return g, err
	}
	return &brokenInterruptAgent{Agent: g}, nil
}

type brokenInterruptAgent struct {
	provider.Agent
	asked bool
}

func (a *brokenInterruptAgent) Do(c context.Context, act provider.Action) error {
	if _, ok := act.(provider.Interrupt); ok {
		return errors.New("the CLI's interrupt write failed")
	}
	return a.Agent.Do(c, act)
}

func (a *brokenInterruptAgent) Next(c context.Context) (provider.Event, error) {
	if !a.asked {
		a.asked = true
		return provider.RequestOpened{ID: "permission", Kind: "approval"}, nil
	}
	<-c.Done()
	return nil, c.Err()
}

func TestACompactionThatCannotBeStoppedEndsFailed(t *testing.T) {
	now := time.Now().UTC()
	gate := make(chan struct{})
	defer close(gate)
	ad := brokenInterrupt{&compactAdapter{onStart: func(s provider.SessionSpec, _ string) ([]provider.Event, <-chan struct{}) {
		if !s.ResumeOnly {
			return nil, nil
		}
		return nil, gate
	}}}
	n := serveOpts(t, dbPath(t), provider.Mux{"claude": ad}, runtime.WithClock(func() time.Time { return now }), runtime.WithIdleCompaction(false))
	thread, _ := idleSeed(t, n.st, now.Add(6*time.Minute-time.Second))
	n.h.SetIdleCompaction(true)
	until(t, n.h, thread, "the compaction to resolve", func(v api.Thread) bool {
		return len(compactions(ad.compactAdapter)) > 0 && (v.Run == api.RunUncertain || v.Run == api.RunIdle)
	})
	submit(t, n.h, thread, dispatch("next", api.StartNow))
	snap, err := n.h.Read(ctx, api.Query{Thread: thread})
	if err != nil {
		t.Fatal(err)
	}
	v := snap.Threads[0]
	if v.Run == api.RunUncertain && !v.Stoppable() {
		t.Fatalf("permission-triggered interrupt left run=%v, Stop=%v and queued follow-up still unsent", v.Run, v.Stoppable())
	}
}
