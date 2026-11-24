package main

import (
	"context"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/daemon"
	"glitched/internal/provider/fake"
)

// An engine another process runs applies engine.json only as it next
// starts, so the thread it holds says nothing of when it continues, and
// offers Continue.
func TestAnExternalEngineThreadOffersContinue(t *testing.T) {
	dir := tempDir(t)
	cfg, err := daemon.Defaults(dir)
	if err != nil {
		t.Fatal(err)
	}
	reset := time.Now().Add(5 * time.Second)
	cfg.Providers["fake"] = fake.New(limitedScript(reset))
	served, err := startEngine(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { served.Close() })
	h := start(t, dir)
	ticking(t, h)
	if h.a.backend.Machine().Embedded {
		t.Fatal("test attached to embedded engine")
	}
	ep := served.Endpoint()
	id := api.NewID()
	for _, body := range []api.Intent{
		api.ThreadCreate{Workspace: t.TempDir(), Title: "External limited", Model: fakeModel},
		api.MessageDispatch{Text: "go", Mode: api.StartNow},
	} {
		if _, err := ep.Submit(context.Background(), api.Command{ID: api.NewID(), Target: id, Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	th := h.thread("External limited")
	h.a.select_(th)
	h.until("the limited thread", func() bool { return th.View.LastTurn != nil && th.View.LastTurn.Limit != nil && !th.View.Live() })
	h.tt.Advance(time.Second)
	if _, ok := h.tt.Find("Continue"); !ok {
		t.Fatalf("no manual Continue before change: %q", h.tt.Texts())
	}

	if err := h.a.backend.Configure(func(c *daemon.Config) { c.ContinueAfterLimit = true }); err != nil {
		t.Fatal(err)
	}
	h.until("config saved but external daemon still running", func() bool {
		m := h.a.backend.Machine()
		return !m.Embedded && m.Config.ContinueAfterLimit && m.Engine != nil
	})
	h.tt.Advance(time.Second)
	if h.tt.HasText("Continues at") {
		t.Fatalf("the thread says when an engine this app doesn't run continues it, which it may not have applied: %q", h.tt.Texts())
	}
	if _, ok := h.tt.Find("Continue"); !ok {
		t.Fatal("GUI did not offer manual Continue")
	}
}
