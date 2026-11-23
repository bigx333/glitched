package main

import (
	"context"
	"slices"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/daemon"
	"glitched/internal/provider/fake"
)

// queuedOn is every message the engine queued on thread, in order.
func queuedOn(t *testing.T, d *daemon.Daemon, thread api.ID) []api.MessageQueued {
	t.Helper()
	snap, err := d.Endpoint().Read(context.Background(), api.Query{Thread: thread})
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

// replaced is whether th's run was abandoned, and the one that replaced it
// ended.
func replaced(th thread) bool {
	v := th.View
	return v.Run == api.RunIdle && v.LastTurn != nil && v.LastTurn.Outcome == api.OutcomeCompleted
}

// Continue shows on a thread whose run was left uncertain, and only there:
// it replaces the run with one that picks up where the agent left off.
func TestContinueShowsOnlyOnAnUncertainThread(t *testing.T) {
	dir, d := lostDir(t, fake.New(fake.Turn(false)), "Stuck")
	seed(t, d, "/work/t3code", "Fresh")
	h := start(t, dir)
	h.a.select_(h.thread("Fresh"))
	h.tt.Advance(time.Second)
	if _, ok := h.tt.Find("Continue"); ok {
		t.Fatalf("Continue shows on a thread with no run: %q", h.tt.Texts())
	}
	th := h.thread("Stuck")
	h.a.select_(th)
	h.until("its transcript", func() bool { return h.tt.HasText("May I edit it?") })
	h.tt.Advance(time.Second)
	h.click("Continue")
	h.until("the run replaced", func() bool { return replaced(th) })
	h.tt.Advance(time.Second)
	if _, ok := h.tt.Find("Continue"); ok {
		t.Fatalf("Continue still shows once the run is replaced: %q", h.tt.Texts())
	}
	qs := queuedOn(t, d, th.id())
	if last := qs[len(qs)-1]; len(qs) != 2 || last.Text != api.ContinueText || last.Mode != api.RestartActive {
		t.Fatalf("queued %+v, want the first message and then the continue, RestartActive", qs)
	}
}

// A message sent to a thread whose run was left uncertain replaces the run,
// rather than being refused as a steer.
func TestAMessageReplacesAnUncertainRun(t *testing.T) {
	dir, d := lostDir(t, fake.New(fake.Turn(false)), "Stuck")
	h := start(t, dir)
	th := h.thread("Stuck")
	h.a.select_(th)
	h.a.draft = "Keep going."
	h.tt.Advance(time.Second)
	h.click("Send")
	h.until("the run replaced", func() bool { return replaced(th) })
	h.tt.Advance(time.Second)
	if h.a.draft != "" || h.tt.HasText("can't take a message") {
		t.Fatalf("the composer holds %q, the window %q", h.a.draft, h.tt.Texts())
	}
	qs := queuedOn(t, d, th.id())
	if last := qs[len(qs)-1]; len(qs) != 2 || last.Text != "Keep going." || last.Mode != api.RestartActive {
		t.Fatalf("queued %+v, want the message RestartActive", qs)
	}
}

// Continue all in the inbox's head continues every thread a restart left
// uncertain, two at a time: the next starts once one of those at work
// stops working.
func TestContinueAllContinuesTwoAtATime(t *testing.T) {
	held := fake.New(fake.Turn(false))
	held.HoldStart = true
	titles := []string{"One", "Two", "Three"}
	dir, _ := lostDir(t, held, titles...)
	h := start(t, dir)
	var ths []thread
	for _, title := range titles {
		ths = append(ths, h.thread(title))
	}
	state := func(s api.RunState) []thread {
		return slices.DeleteFunc(slices.Clone(ths), func(th thread) bool { return th.View.Run != s })
	}
	h.tt.Advance(time.Second)
	h.click("Continue all")
	h.until("two continued", func() bool { return len(state(api.RunStarting)) == 2 })
	h.tt.Advance(time.Second)
	if left := state(api.RunUncertain); len(left) != 1 || held.Starts() != 2 {
		t.Fatalf("%d left uncertain and %d starts, want 1 and 2", len(left), held.Starts())
	}
	if _, ok := h.tt.Find("Continue all"); ok {
		t.Fatal("Continue all still shows with every lost thread continuing")
	}
	h.a.backend.Interrupt(h.local(), state(api.RunStarting)[0].id())
	h.until("the third continued", func() bool { return len(state(api.RunUncertain)) == 0 && held.Starts() == 3 })
	if len(h.a.continues) > 2 {
		t.Fatalf("%d threads still held to continue, want at most the two at work", len(h.a.continues))
	}
}
