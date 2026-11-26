package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
	"glitched/internal/store"
)

type captureAdapter struct {
	*fake.Adapter
	mu     sync.Mutex
	starts []string
}

func (c *captureAdapter) Open(ctx context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	ag, err := c.Adapter.Open(ctx, spec)
	if err != nil {
		return nil, err
	}
	return &captureAgent{Agent: ag, parent: c}, nil
}

type captureAgent struct {
	provider.Agent
	parent *captureAdapter
}

func (a *captureAgent) Do(ctx context.Context, act provider.Action) error {
	if s, ok := act.(provider.Start); ok {
		a.parent.mu.Lock()
		a.parent.starts = append(a.parent.starts, s.Text)
		a.parent.mu.Unlock()
	}
	return a.Agent.Do(ctx, act)
}

func TestManagerSharedWorkspaceDocsInjected(t *testing.T) {
	ctx := context.Background()
	ws := t.TempDir()
	for name, content := range map[string]string{
		"AGENTS.md":  "Repository conventions: run go test.",
		"WORKERS.md": "Worker W1 owns phase 6.",
		"NOTES.md":   "Remember schema version 20.",
	} {
		if err := os.WriteFile(filepath.Join(ws, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	st, err := store.Open(filepath.Join(t.TempDir(), "host.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	capAd := &captureAdapter{Adapter: fake.New(fake.Turn(false))}
	h, err := New(ctx, st, provider.Mux{"fake": capAd})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })

	mgr := api.NewID()
	if _, err := h.Submit(ctx, api.Command{
		ID:     api.NewID(),
		Target: mgr,
		Body: api.ThreadCreate{
			Workspace: ws,
			Title:     "Manager",
			Model:     api.ModelRef{Provider: "fake", Model: "fake"},
			Role:      api.RoleManager,
			Text:      "Coordinate phase 6",
		},
	}); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		snap, err := h.Read(ctx, api.Query{Thread: mgr})
		if err == nil && len(snap.Threads) == 1 && snap.Threads[0].Run == api.RunIdle && snap.Threads[0].LastTurn != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	capAd.mu.Lock()
	if len(capAd.starts) != 1 {
		capAd.mu.Unlock()
		t.Fatalf("captured starts = %d, want 1", len(capAd.starts))
	}
	firstPrompt := capAd.starts[0]
	capAd.mu.Unlock()

	for _, want := range []string{"<workspace_context>", "AGENTS.md", "WORKERS.md", "NOTES.md", "Repository conventions: run go test.", "Coordinate phase 6"} {
		if !strings.Contains(firstPrompt, want) {
			t.Fatalf("first turn prompt missing %q:\n%s", want, firstPrompt)
		}
	}

	if _, err := h.Submit(ctx, api.Command{
		ID:     api.NewID(),
		Target: mgr,
		Body:   api.MessageDispatch{Text: "follow up turn", Mode: api.StartNow},
	}); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		capAd.mu.Lock()
		n := len(capAd.starts)
		capAd.mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	capAd.mu.Lock()
	secondPrompt := capAd.starts[1]
	capAd.mu.Unlock()
	if strings.Contains(secondPrompt, "<workspace_context>") || secondPrompt != "follow up turn" {
		t.Fatalf("second turn unexpectedly altered: %q", secondPrompt)
	}
}

func TestManagerScheduledSweepFiresAndDismissesSilently(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "host.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	var (
		mu  sync.Mutex
		now = time.Unix(10_000, 0).UTC()
	)
	h, err := New(ctx, st, provider.Mux{"fake": fake.New(fake.Turn(true))}, WithClock(func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })

	mgr := api.NewID()
	if _, err := h.Submit(ctx, api.Command{
		ID:     api.NewID(),
		Target: mgr,
		Body: api.ThreadCreate{
			Workspace: t.TempDir(),
			Title:     "Sweeper",
			Model:     api.ModelRef{Provider: "fake", Model: "fake"},
			Role:      api.RoleManager,
			Sweep:     &api.SweepSchedule{IntervalSeconds: 60},
		},
	}); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	now = now.Add(65 * time.Second)
	mu.Unlock()
	h.pokeSweeps()

	var runID api.ID
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		snap, err := h.Read(ctx, api.Query{Thread: mgr})
		if err == nil && len(snap.Threads) == 1 && !snap.Threads[0].LastSweep.IsZero() && snap.Threads[0].Turn != nil {
			runID = snap.Threads[0].Turn.Run
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if runID.IsZero() {
		t.Fatal("timed out waiting for scheduled sweep to start")
	}

	if _, err := h.Submit(ctx, api.Command{
		ID:     api.NewID(),
		Target: mgr,
		Body:   api.WakeDismiss{Run: runID, Reason: "no changes needed"},
	}); err != nil {
		t.Fatalf("WakeDismiss: %v", err)
	}

	snap, err := h.Read(ctx, api.Query{Thread: mgr})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Threads[0].Turn == nil || !snap.Threads[0].Turn.Silent || snap.Threads[0].Working() {
		t.Fatalf("expected silent non-working turn after WakeDismiss, got %+v", snap.Threads[0])
	}
}

func TestAPlainThreadGetsNoWorkspaceDocs(t *testing.T) {
	ctx := context.Background()
	ws := t.TempDir()
	for _, name := range managerDocFiles {
		if err := os.WriteFile(filepath.Join(ws, name), []byte(name+" says hello"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "host.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	capAd := &captureAdapter{Adapter: fake.New(fake.Turn(false))}
	h, err := New(ctx, st, provider.Mux{"fake": capAd})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	plain := api.NewID()
	if _, err := h.Submit(ctx, api.Command{ID: api.NewID(), Target: plain, Body: api.ThreadCreate{
		Workspace: ws, Title: "Plain", Model: api.ModelRef{Provider: "fake", Model: "fake"}, Text: "Fix the typo",
	}}); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if snap, err := h.Read(ctx, api.Query{Thread: plain}); err == nil && snap.Threads[0].LastTurn != nil {
			break
		}
	}
	capAd.mu.Lock()
	starts := slices.Clone(capAd.starts)
	capAd.mu.Unlock()
	if len(starts) != 1 || starts[0] != "Fix the typo" {
		t.Fatalf("the plain thread's first prompts = %q, want only its own words", starts)
	}
	if docs := loadCoordinatorDocs(ws, "", api.RoleDefault, true); !strings.Contains(docs, "<workspace_context>") {
		t.Fatalf("a worker's docs = %q, want the workspace context", docs)
	}
}

func TestOnlyAManagersChangeMovesSweeps(t *testing.T) {
	plain := api.Thread{ID: api.NewID(), Title: "plain"}
	manager := api.Thread{ID: api.NewID(), Role: api.RoleManager}
	if movesSweeps([]api.Thread{plain, plain}) {
		t.Fatal("a commit of plain threads woke the sweep scheduler")
	}
	if !movesSweeps([]api.Thread{plain, manager}) {
		t.Fatal("a Manager's commit left the sweep scheduler asleep")
	}
}

// failingOpen fails its first open, before any agent takes a prompt.
type failingOpen struct {
	*captureAdapter
	opens atomic.Int32
}

func (f *failingOpen) Open(ctx context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	if f.opens.Add(1) == 1 {
		return nil, errors.New("provider unavailable")
	}
	return f.captureAdapter.Open(ctx, spec)
}

func TestAManagerWhoseFirstTurnNeverReachedItsAgentGetsTheDocsNext(t *testing.T) {
	ctx := context.Background()
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "WORKERS.md"), []byte("Worker W1 owns phase 6."), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "host.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ad := &failingOpen{captureAdapter: &captureAdapter{Adapter: fake.New(fake.Turn(false))}}
	h, err := New(ctx, st, provider.Mux{"fake": ad})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	mgr := api.NewID()
	submit := func(b api.Intent) {
		t.Helper()
		if _, err := h.Submit(ctx, api.Command{ID: api.NewID(), Target: mgr, Body: b}); err != nil {
			t.Fatal(err)
		}
	}
	ended := func(n int) {
		t.Helper()
		for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
			snap, err := h.Read(ctx, api.Query{Thread: mgr})
			if err == nil && snap.Threads[0].Run == api.RunIdle && len(snap.Threads[0].Requests) == 0 && snap.Threads[0].LastTurn != nil && ad.opens.Load() >= int32(n) {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("turn %d never ended", n)
			}
		}
	}
	submit(api.ThreadCreate{Workspace: ws, Title: "Manager", Model: api.ModelRef{Provider: "fake", Model: "fake"}, Role: api.RoleManager, Text: "Coordinate"})
	ended(1)
	submit(api.MessageDispatch{Text: "Try again", Mode: api.StartNow})
	ended(2)
	submit(api.MessageDispatch{Text: "And again", Mode: api.StartNow})
	ended(3)
	ad.mu.Lock()
	starts := slices.Clone(ad.starts)
	ad.mu.Unlock()
	if len(starts) != 2 || !strings.Contains(starts[0], "Worker W1 owns phase 6.") || strings.Contains(starts[1], "<workspace_context>") {
		t.Fatalf("prompts after a failed first open = %q, want the docs once, with the first the agent took", starts)
	}
}

func TestASnoozedManagerSweepsWhenItsSnoozeRunsOut(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "host.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	h, err := New(ctx, st, provider.Mux{"fake": fake.New(fake.Turn(false))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	mgr := api.NewID()
	if _, err := h.Submit(ctx, api.Command{ID: api.NewID(), Target: mgr, Body: api.ThreadCreate{
		Workspace: t.TempDir(), Title: "Sweeper", Model: api.ModelRef{Provider: "fake", Model: "fake"},
		Role: api.RoleManager, Sweep: &api.SweepSchedule{IntervalSeconds: 1},
	}}); err != nil {
		t.Fatal(err)
	}
	until := time.Now().UTC().Add(1200 * time.Millisecond)
	if _, err := h.Submit(ctx, api.Command{ID: api.NewID(), Target: mgr, Body: api.InboxSet{Disposition: api.Disposition{State: api.Snoozed, Until: until}}}); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		snap, err := h.Read(ctx, api.Query{Thread: mgr})
		if err != nil {
			t.Fatal(err)
		}
		if v := snap.Threads[0]; !v.LastSweep.IsZero() {
			if v.LastSweep.Before(until) {
				t.Fatalf("the Manager swept at %v, during its snooze until %v", v.LastSweep, until)
			}
			return
		}
	}
	t.Fatalf("the Manager never swept after its snooze ran out at %v", until)
}
