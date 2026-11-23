package main

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/client"
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

// Continue hides while the run that replaced the uncertain one works.
func TestContinueHidesWhileTheReplacementRuns(t *testing.T) {
	dir, _ := lostDir(t, slowStop{Adapter: fake.New(fake.Turn(false)), release: make(chan struct{})}, "Stuck")
	h := start(t, dir)
	th := h.thread("Stuck")
	h.a.select_(th)
	h.until("its transcript", func() bool { return h.tt.HasText("May I edit it?") })
	h.tt.Advance(time.Second)
	h.click("Continue")
	h.until("the replacement running", func() bool { return th.View.Run == api.RunRunning })
	h.tt.Advance(time.Second)
	if _, ok := h.tt.Find("Continue"); ok {
		t.Fatalf("Continue shows while the replacement runs: %q", h.tt.Texts())
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

// A Continue sent from a view the engine has moved past, as a second
// client's or a stale Retry, is refused: it never stops the run the first
// Continue started.
func TestAStaleContinueIsRefused(t *testing.T) {
	dir, d := lostDir(t, fake.New(fake.Turn(true)), "Stuck")
	h := start(t, dir)
	th := h.thread("Stuck")
	first, second := h.a.continueRun(th), h.a.continueRun(th)
	refused := func() bool {
		rej := (*api.RejectedError)(nil)
		i := slices.IndexFunc(th.Pending, func(p client.Pending) bool { return p.Command == second })
		return i >= 0 && errors.As(th.Pending[i].Err, &rej)
	}
	h.until("the second refused", refused)
	h.a.backend.Retry(th.env, th.id(), second)
	h.until("the retry refused", refused)
	h.until("the first Continue's run waiting on its approval", func() bool {
		v := th.View
		return v.Turn != nil && v.Turn.Run == first && v.Run == api.RunWaiting
	})
	if qs := queuedOn(t, d, th.id()); len(qs) != 2 {
		t.Fatalf("queued %+v, want the first message and one Continue", qs)
	}
}

// Continue all in the inbox's head continues every thread a restart left
// uncertain, two at a time: a continued run holds its place while it's
// live, waiting on the user's answer too, and the next starts once one
// ends.
func TestContinueAllContinuesTwoAtATime(t *testing.T) {
	titles := []string{"One", "Two", "Three"}
	dir, d := lostDir(t, fake.New(fake.Turn(true)), titles...)
	h := start(t, dir)
	var ths []thread
	for _, title := range titles {
		ths = append(ths, h.thread(title))
	}
	in := func(s api.RunState) []thread {
		return slices.DeleteFunc(slices.Clone(ths), func(th thread) bool { return th.View.Run != s })
	}
	h.tt.Advance(time.Second)
	h.click("Continue all")
	h.until("two continued", func() bool { return len(in(api.RunWaiting)) == 2 })
	h.tt.Advance(time.Second)
	if left := in(api.RunUncertain); len(left) != 1 || !continuable(left[0]) {
		t.Fatalf("%d left uncertain with two continued runs waiting, want 1 not yet continued", len(left))
	}
	if _, ok := h.tt.Find("Continue all"); ok {
		t.Fatal("Continue all still shows with every lost thread continuing")
	}
	answered := in(api.RunWaiting)[0]
	h.a.select_(answered)
	h.tt.Advance(time.Second)
	if _, ok := h.tt.Find("Continue"); ok {
		t.Fatal("Continue shows on a thread whose continued run waits on its approval")
	}
	r := answered.View.Requests[0]
	if _, err := d.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: answered.id(), Body: api.RequestRespond{Request: r.ID, Generation: r.Generation, Answer: json.RawMessage(`{"allow":true}`)}}); err != nil {
		t.Fatal(err)
	}
	h.until("the third continued", func() bool { return len(in(api.RunUncertain)) == 0 })
	if len(h.a.continues) != 2 {
		t.Fatalf("%d threads held to continue, want the two live", len(h.a.continues))
	}
}
