package main

import (
	"context"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/daemon"
	"glitched/internal/provider/fake"
	"glitched/internal/transport"
)

func TestManagerUIHeaderSweepAndWakeDismiss(t *testing.T) {
	dir := tempDir(t)
	h := startWith(t, dir, func(c *daemon.Config) {
		c.Providers = daemon.FakeProviders(fake.New(fake.Turn(true)))
	})

	ws := gitWorkspace(t)
	mgrID := h.a.backend.CreateManager(h.local(), ws, "Workspace Coordinator", api.ModelRef{Provider: "claude", Model: "claude-opus-5-5"}, api.AccessAutoEdits, &api.SweepSchedule{IntervalSeconds: 1800})
	h.until("manager created", func() bool {
		th := h.local().Threads[mgrID]
		return th != nil && th.View.Role == api.RoleManager && th.View.Revision > 0
	})

	h.a.selected = mgrID
	h.a.backend.OpenThread(h.local(), mgrID)
	h.tt.Advance(time.Second)

	if !h.tt.HasText("Manager") || !h.tt.HasText("Sweep 30m") {
		t.Fatalf("header missing Manager/Sweep 30m pills: %q", h.tt.Texts())
	}

	// Clicking Sweep 30m cycles to Sweep 1h (3600s) and persists in engine.
	h.click("Sweep 30m")
	h.until("sweep updated to 1h", func() bool {
		th := h.local().Threads[mgrID]
		return th != nil && th.View.Sweep != nil && th.View.Sweep.IntervalSeconds == 3600
	})
	h.tt.Frame()
	if !h.tt.HasText("Sweep 1h") {
		t.Fatalf("expected updated Sweep 1h pill, got %q", h.tt.Texts())
	}

	// Send a message so run stays live (waiting at approval), then dismiss its wake.
	runID := h.a.backend.Send(h.local(), mgrID, "sweep turn", api.StartNow)
	h.until("manager turn waiting", func() bool {
		th := h.local().Threads[mgrID]
		return th != nil && th.View.Turn != nil && th.View.Run == api.RunWaiting
	})

	cli := h.a.backend.Envs()[0]
	h.a.backend.MarkRead(cli.Threads[mgrID])
	ep := transport.NewEndpointClient(daemon.Socket(dir), api.ID{})
	defer ep.Close()
	if _, err := ep.Submit(context.Background(), api.Command{
		ID:     api.NewID(),
		Target: mgrID,
		Body:   api.WakeDismiss{Run: runID, Reason: "quiet"},
	}); err != nil {
		t.Fatalf("WakeDismiss: %v", err)
	}
	h.until("silent wake reflected in client", func() bool {
		th := cli.Threads[mgrID]
		return th != nil && th.View.Turn != nil && th.View.Turn.Silent
	})
	if h.a.backend.Unread(cli.Threads[mgrID]) {
		t.Fatal("dismissed Manager wake unexpectedly marked thread unread")
	}
}
