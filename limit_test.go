package main

import (
	"context"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/daemon"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
	"glitched/internal/transport"
)

// limitedScript is a turn a usage limit ends, which resets at resets.
func limitedScript(resets time.Time) []provider.Event {
	return []provider.Event{
		provider.TextDelta{Text: "Working on it."},
		provider.TurnEnded{Outcome: api.OutcomeFailed, Error: "You've hit your usage limit.", Limit: &api.UsageLimit{Resets: resets}},
	}
}

// limitedHarness starts the app on an engine whose agent a usage limit
// stops, with continue after a usage limit as on says, and has the agent
// of a thread titled title hit it.
func limitedHarness(t *testing.T, on bool, title string) (*harness, api.Endpoint, thread) {
	t.Helper()
	dir := tempDir(t)
	if _, err := daemon.Edit(dir, func(c *daemon.Config) error {
		c.ContinueAfterLimit = on
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	h := startWith(t, dir, func(c *daemon.Config) {
		c.Providers["fake"] = fake.New(limitedScript(time.Now().Add(90 * time.Minute)))
		c.ContinueAfterLimit = on
	})
	ticking(t, h)
	ep := transport.NewEndpointClient(daemon.Socket(dir), api.ID{})
	t.Cleanup(func() { ep.Close() })
	id := api.NewID()
	for _, b := range []api.Intent{api.ThreadCreate{Workspace: "/work/t3code", Title: title, Model: fakeModel}, api.MessageDispatch{Text: "go", Mode: api.StartNow}} {
		if _, err := ep.Submit(context.Background(), api.Command{ID: api.NewID(), Target: id, Body: b}); err != nil {
			t.Fatal(err)
		}
	}
	th := h.thread(title)
	h.a.select_(th)
	h.until("the usage limit", func() bool { return th.View.LastTurn != nil && th.View.LastTurn.Limit != nil && !th.View.Live() })
	h.tt.Advance(time.Second)
	return h, ep, th
}

// queuedThrough is every message the engine queued on thread, in order.
func queuedThrough(t *testing.T, ep api.Endpoint, thread api.ID) []api.MessageQueued {
	t.Helper()
	snap, err := ep.Read(context.Background(), api.Query{Thread: thread})
	if err != nil {
		t.Fatal(err)
	}
	var qs []api.MessageQueued
	for _, e := range snap.Events {
		if b, _ := e.Decode(); b != nil {
			if q, ok := b.(api.MessageQueued); ok {
				qs = append(qs, q)
			}
		}
	}
	return qs
}

// With continue after a usage limit off, a thread a usage limit stopped
// offers Continue, which sends the agent on.
func TestALimitedThreadOffersContinue(t *testing.T) {
	h, d, th := limitedHarness(t, false, "Limited")
	if h.tt.HasText("Continues at") {
		t.Fatalf("the thread says when it continues with the switch off: %q", h.tt.Texts())
	}
	h.click("Continue")
	h.until("the continue", func() bool {
		qs := queuedThrough(t, d, th.id())
		return len(qs) == 2 && qs[1].Text == api.LimitContinueText && !qs[1].Auto
	})
}

// With it on, the thread says when the engine continues it, and Cancel
// stops that: the thread offers Continue instead.
func TestALimitedThreadSaysWhenItContinuesAndCancels(t *testing.T) {
	h, d, th := limitedHarness(t, true, "Limited")
	h.until("when it continues", func() bool { return h.tt.HasText("Continues at") })
	h.click("Cancel")
	h.until("the cancel", func() bool { return th.View.LastTurn.ResumeCancelled })
	h.tt.Advance(time.Second)
	if h.tt.HasText("Continues at") {
		t.Fatalf("the thread still says when it continues once cancelled: %q", h.tt.Texts())
	}
	if _, ok := h.tt.Find("Continue"); !ok {
		t.Fatalf("no Continue once cancelled: %q", h.tt.Texts())
	}
	if qs := queuedThrough(t, d, th.id()); len(qs) != 1 {
		t.Fatalf("queued %+v, want only the first message", qs)
	}
}

// The Agents card's continue after a usage limit switch is off until the
// user turns it on, saves it to engine.json, and applies it at once,
// without the restart that would cut the engine's agents off.
func TestTheAgentsCardTogglesContinueAfterAUsageLimit(t *testing.T) {
	dir := tempDir(t)
	held := fake.New(fake.Turn(false))
	held.HoldStart = true
	h := startWith(t, dir, func(c *daemon.Config) { c.Providers["fake"] = held })
	h.tt.SetSize(1280, 1400)
	ticking(t, h)
	ep := transport.NewEndpointClient(daemon.Socket(dir), api.ID{})
	t.Cleanup(func() { ep.Close() })
	id := api.NewID()
	for _, b := range []api.Intent{api.ThreadCreate{Workspace: "/work/t3code", Title: "Busy", Model: fakeModel}, api.MessageDispatch{Text: "go", Mode: api.StartNow}} {
		if _, err := ep.Submit(context.Background(), api.Command{ID: api.NewID(), Target: id, Body: b}); err != nil {
			t.Fatal(err)
		}
	}
	h.until("the agent starting", func() bool { return held.Starts() == 1 })
	h.click("Settings")
	h.click("This machine")
	h.until("the Agents card", func() bool { return h.tt.HasText("Continue agents after a usage limit") && h.a.backend.Machine().Embedded })
	for _, want := range []bool{true, false} {
		cfg, err := daemon.Defaults(dir)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.ContinueAfterLimit == want {
			t.Fatalf("continue after a usage limit is %v before the click, want %v", cfg.ContinueAfterLimit, !want)
		}
		h.click("Enable continue agents after a usage limit")
		h.tt.Advance(time.Second)
		if cfg, err = daemon.Defaults(dir); err != nil || cfg.ContinueAfterLimit != want {
			t.Fatalf("continue after a usage limit %v (%v), want %v", cfg.ContinueAfterLimit, err, want)
		}
	}
	if snap, err := ep.Read(context.Background(), api.Query{Thread: id}); err != nil || snap.Threads[0].Run != api.RunStarting || held.Starts() != 1 {
		t.Fatalf("the agent's run is %v after the switch, started %d times; want it still starting, once", snap.Threads, held.Starts())
	}
}
