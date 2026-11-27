package runtime_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
	"glitched/internal/provider"
	"glitched/internal/runtime"
	"glitched/internal/store"
)

type recordedStart struct {
	spec provider.SessionSpec
	text string
}

type compactAdapter struct {
	mu      sync.Mutex
	starts  []recordedStart
	steers  int
	stops   int
	session func(spec provider.SessionSpec) string
	onStart func(spec provider.SessionSpec, text string) (evs []provider.Event, gate <-chan struct{})
}

func (a *compactAdapter) Describe(context.Context) (provider.Info, error) {
	return provider.Info{Name: "compact-test", Version: "1"}, nil
}

func (a *compactAdapter) Open(_ context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	sess := spec.Session
	if sess == "" && a.session != nil {
		sess = a.session(spec)
	}
	return &compactAgent{a: a, spec: spec, sess: sess, evs: make(chan provider.Event, 16), done: make(chan struct{})}, nil
}

func (a *compactAdapter) Reconcile(context.Context, provider.RuntimeIdentity) (provider.Recovery, error) {
	return provider.Lost{Reason: "none"}, nil
}

func (a *compactAdapter) recorded() []recordedStart {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]recordedStart, len(a.starts))
	copy(out, a.starts)
	return out
}

type compactAgent struct {
	a    *compactAdapter
	spec provider.SessionSpec
	mu   sync.Mutex
	sess string
	evs  chan provider.Event
	done chan struct{}
}

func (g *compactAgent) Session() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.sess != "" {
		return g.sess
	}
	return g.spec.Session
}

func (g *compactAgent) Do(_ context.Context, act provider.Action) error {
	switch act := act.(type) {
	case provider.Start:
		g.a.mu.Lock()
		g.a.starts = append(g.a.starts, recordedStart{spec: g.spec, text: act.Text})
		fn := g.a.onStart
		g.a.mu.Unlock()
		var evs []provider.Event
		var gate <-chan struct{}
		if fn != nil {
			evs, gate = fn(g.spec, act.Text)
		}
		go func() {
			if gate != nil {
				<-gate
			}
			for _, e := range evs {
				g.evs <- e
			}
			g.evs <- provider.TurnEnded{Outcome: api.OutcomeCompleted}
		}()
	case provider.Steer:
		g.a.mu.Lock()
		g.a.steers++
		g.a.mu.Unlock()
	case provider.Interrupt:
		g.a.mu.Lock()
		g.a.stops++
		g.a.mu.Unlock()
		go func() { g.evs <- provider.TurnEnded{Outcome: api.OutcomeInterrupted} }()
	}
	return nil
}

func (g *compactAgent) Next(ctx context.Context) (provider.Event, error) {
	select {
	case e := <-g.evs:
		return e, nil
	case <-g.done:
		return nil, context.Canceled
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (g *compactAgent) Close(context.Context) error {
	select {
	case <-g.done:
	default:
		close(g.done)
	}
	return nil
}

func serveOpts(t *testing.T, path string, providers provider.Mux, opts ...runtime.Option) node {
	t.Helper()
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := runtime.New(ctx, st, providers, opts...)
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	n := node{st, h}
	t.Cleanup(n.kill)
	return n
}

func TestClaudeThreadCompactsBeforeCacheExpires(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	var clockMu sync.Mutex
	clock := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return now
	}
	expires := now.Add(time.Hour)

	ad := &compactAdapter{
		session: func(provider.SessionSpec) string { return "sess-claude-1" },
		onStart: func(spec provider.SessionSpec, text string) ([]provider.Event, <-chan struct{}) {
			if text == "/compact" {
				return []provider.Event{provider.Context{Compacted: true, PreTokens: 250000, PostTokens: 32000}}, nil
			}
			return []provider.Event{
				provider.Context{Used: 250000, Window: 1000000, Model: "claude-opus-4-6", CacheExpires: expires},
				provider.TextDelta{Text: "done"},
			}, nil
		},
	}

	n := serveOpts(t, dbPath(t), provider.Mux{"claude": ad}, runtime.WithClock(clock), runtime.WithIdleCompaction(true))
	thread := api.NewID()
	submit(t, n.h, thread, api.ThreadCreate{Workspace: "/src/app", Title: "claude", Model: api.ModelRef{Provider: "claude", Model: "claude-opus-4-6"}})
	submit(t, n.h, thread, dispatch("work", api.StartNow))

	first := until(t, n.h, thread, "first turn done", func(v api.Thread) bool {
		return v.Run == api.RunIdle && v.LastTurn != nil
	})
	firstEnded := first.LastTurn.Ended
	updatedBefore := first.UpdatedAt

	// Advance clock into the compaction window so compactLoop fires promptly.
	clockMu.Lock()
	now = expires.Add(-engine.ClaudeCacheTTL/10 + time.Second)
	clockMu.Unlock()
	// Re-arm timers at the new clock time.
	n.h.SetIdleCompaction(false)
	n.h.SetIdleCompaction(true)

	after := until(t, n.h, thread, "idle compaction completed", func(v api.Thread) bool {
		return v.Run == api.RunIdle && v.Context != nil && v.Context.Stale
	})
	if !after.Context.CacheExpires.IsZero() {
		t.Errorf("CacheExpires after compaction = %v, want zero", after.Context.CacheExpires)
	}
	if after.LastTurn == nil || !after.LastTurn.Ended.Equal(firstEnded) {
		t.Errorf("LastTurn = %+v, want Ended=%v (silent compaction excluded)", after.LastTurn, firstEnded)
	}
	if !after.UpdatedAt.Equal(updatedBefore) {
		t.Errorf("UpdatedAt moved from %v to %v", updatedBefore, after.UpdatedAt)
	}
	starts := ad.recorded()
	if len(starts) != 2 || starts[1].text != "/compact" || !starts[1].spec.ResumeOnly || starts[1].spec.Session != "sess-claude-1" {
		t.Fatalf("starts = %+v, want second start /compact with ResumeOnly on sess-claude-1", starts)
	}
}

func TestAntigravityGeminiCompactsAndClaudeDoesNot(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	var clockMu sync.Mutex
	clock := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return now
	}

	ad := &compactAdapter{
		session: func(spec provider.SessionSpec) string { return "ag-" + spec.Model.Model },
		onStart: func(spec provider.SessionSpec, text string) ([]provider.Event, <-chan struct{}) {
			if text == "/compact" {
				return []provider.Event{provider.Context{Compacted: true}}, nil
			}
			return []provider.Event{
				provider.Context{Used: 220000, Window: 1000000, Model: spec.Model.Model},
				provider.TextDelta{Text: "ok"},
			}, nil
		},
	}

	n := serveOpts(t, dbPath(t), provider.Mux{"antigravity": ad}, runtime.WithClock(clock), runtime.WithIdleCompaction(true))
	gemini := api.NewID()
	claudeOnAgy := api.NewID()
	submit(t, n.h, gemini, api.ThreadCreate{Workspace: "/src/app", Title: "gemini", Model: api.ModelRef{Provider: "antigravity", Model: "gemini-3.8-flash"}})
	submit(t, n.h, claudeOnAgy, api.ThreadCreate{Workspace: "/src/app", Title: "claude-agy", Model: api.ModelRef{Provider: "antigravity", Model: "claude-sonnet-4-6"}})
	submit(t, n.h, gemini, dispatch("work", api.StartNow))
	submit(t, n.h, claudeOnAgy, dispatch("work", api.StartNow))

	until(t, n.h, gemini, "gemini first turn", func(v api.Thread) bool { return v.Run == api.RunIdle && v.LastTurn != nil })
	until(t, n.h, claudeOnAgy, "claude-agy first turn", func(v api.Thread) bool { return v.Run == api.RunIdle && v.LastTurn != nil })

	// Move the clock to 90% of the 1 hour agy keeps its cache.
	clockMu.Lock()
	now = now.Add(engine.AntigravityCacheTTL*9/10 + time.Second)
	clockMu.Unlock()
	n.h.SetIdleCompaction(false)
	n.h.SetIdleCompaction(true)

	until(t, n.h, gemini, "gemini compacted", func(v api.Thread) bool {
		return v.Run == api.RunIdle && v.Context != nil && v.Context.Stale
	})
	// Give any unintended timer a moment, then assert claude-sonnet-4-6 on Antigravity never compacted.
	time.Sleep(50 * time.Millisecond)
	snap, err := n.h.Read(ctx, api.Query{Thread: claudeOnAgy})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Threads[0].Context.Stale {
		t.Fatalf("claude-sonnet-4-6 on Antigravity compacted, want excluded")
	}
	for _, s := range ad.recorded() {
		if s.text == "/compact" && s.spec.Thread == claudeOnAgy {
			t.Fatalf("claude-sonnet-4-6 on Antigravity received /compact")
		}
	}
}

func TestIdleCompactionToggleAndRestart(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	var clockMu sync.Mutex
	clock := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return now
	}
	expires := now.Add(time.Hour)

	ad := &compactAdapter{
		session: func(provider.SessionSpec) string { return "sess-restart" },
		onStart: func(spec provider.SessionSpec, text string) ([]provider.Event, <-chan struct{}) {
			if text == "/compact" {
				return []provider.Event{provider.Context{Compacted: true}}, nil
			}
			return []provider.Event{
				provider.Context{Used: 250000, Window: 1000000, Model: "claude-opus-4-6", CacheExpires: expires},
			}, nil
		},
	}

	path := dbPath(t)
	n := serveOpts(t, path, provider.Mux{"claude": ad}, runtime.WithClock(clock), runtime.WithIdleCompaction(false))
	thread := api.NewID()
	submit(t, n.h, thread, api.ThreadCreate{Workspace: "/src/app", Title: "restart", Model: api.ModelRef{Provider: "claude", Model: "claude-opus-4-6"}})
	submit(t, n.h, thread, dispatch("work", api.StartNow))
	until(t, n.h, thread, "first turn", func(v api.Thread) bool { return v.Run == api.RunIdle && v.LastTurn != nil })

	// Advance into the window while disabled: nothing should fire.
	clockMu.Lock()
	now = expires.Add(-engine.ClaudeCacheTTL/10 + time.Second)
	clockMu.Unlock()
	time.Sleep(50 * time.Millisecond)
	if len(ad.recorded()) != 1 {
		t.Fatalf("compacted while disabled: %+v", ad.recorded())
	}

	// Restart with idle compaction enabled: boot scan must pick up the thread from SQLite.
	n.stop()
	n = serveOpts(t, path, provider.Mux{"claude": ad}, runtime.WithClock(clock), runtime.WithIdleCompaction(true))
	until(t, n.h, thread, "boot scan compacted thread", func(v api.Thread) bool {
		return v.Run == api.RunIdle && v.Context != nil && v.Context.Stale
	})
}

func TestUserMessageQueuesBehindSilentCompaction(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	var clockMu sync.Mutex
	clock := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return now
	}
	expires := now.Add(time.Hour)
	compactStarted := make(chan struct{})
	releaseCompact := make(chan struct{})

	ad := &compactAdapter{
		session: func(provider.SessionSpec) string { return "sess-q" },
		onStart: func(spec provider.SessionSpec, text string) ([]provider.Event, <-chan struct{}) {
			switch text {
			case "/compact":
				close(compactStarted)
				return []provider.Event{provider.Context{Compacted: true}}, releaseCompact
			case "follow up":
				return []provider.Event{provider.TextDelta{Text: "after compact"}}, nil
			default:
				return []provider.Event{
					provider.Context{Used: 250000, Window: 1000000, Model: "claude-opus-4-6", CacheExpires: expires},
				}, nil
			}
		},
	}

	n := serveOpts(t, dbPath(t), provider.Mux{"claude": ad}, runtime.WithClock(clock), runtime.WithIdleCompaction(true))
	thread := api.NewID()
	submit(t, n.h, thread, api.ThreadCreate{Workspace: "/src/app", Title: "queue", Model: api.ModelRef{Provider: "claude", Model: "claude-opus-4-6"}})
	submit(t, n.h, thread, dispatch("work", api.StartNow))
	until(t, n.h, thread, "first turn", func(v api.Thread) bool { return v.Run == api.RunIdle && v.LastTurn != nil })

	clockMu.Lock()
	now = expires.Add(-engine.ClaudeCacheTTL/10 + time.Second)
	clockMu.Unlock()
	n.h.SetIdleCompaction(false)
	n.h.SetIdleCompaction(true)

	<-compactStarted
	until(t, n.h, thread, "silent turn running", func(v api.Thread) bool {
		return v.Run == api.RunRunning && v.Turn != nil && v.Turn.Silent
	})

	// User sends SteerActive mid-compaction: must queue without steering or interrupting.
	submit(t, n.h, thread, dispatch("follow up", api.SteerActive))
	time.Sleep(40 * time.Millisecond)
	ad.mu.Lock()
	steers, stops := ad.steers, ad.stops
	ad.mu.Unlock()
	if steers != 0 || stops != 0 {
		t.Fatalf("during silent compaction got steers=%d stops=%d, want 0, 0", steers, stops)
	}

	close(releaseCompact)
	until(t, n.h, thread, "follow-up completed", func(v api.Thread) bool {
		return v.Run == api.RunIdle && transcript(t, events(t, n.h, thread)) == "after compact"
	})
}
