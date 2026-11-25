package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

	// Second turn in bound session should NOT re-inject workspace docs.
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
	// Use fake.Turn(true) so run stays waiting in approval/live state long enough to dismiss wake.
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

	// Advance past 60s and poke sweeps.
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
