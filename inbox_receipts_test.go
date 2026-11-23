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

// heldViews is an engine whose shell sends its first item, the snapshot,
// and then nothing until resume closes: its receipts run ahead of its
// views.
type heldViews struct {
	api.Endpoint
	resume chan struct{}
}

func (e heldViews) Open(ctx context.Context, sub api.Subscription) (api.Stream, error) {
	st, err := e.Endpoint.Open(ctx, sub)
	if err != nil || !sub.Thread.IsZero() {
		return st, err
	}
	return &heldStream{Stream: st, resume: e.resume}, nil
}

type heldStream struct {
	api.Stream
	resume <-chan struct{}
	sent   bool
}

func (s *heldStream) Next(ctx context.Context) (api.StreamItem, error) {
	if s.sent {
		select {
		case <-s.resume:
		case <-ctx.Done():
			return api.StreamItem{}, ctx.Err()
		}
	}
	s.sent = true
	return s.Stream.Next(ctx)
}

// stored is the thread as the engine has it.
func stored(t *testing.T, ep api.Endpoint, id api.ID) api.Thread {
	t.Helper()
	snap, err := ep.Read(context.Background(), api.Query{})
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(snap.Threads, func(v api.Thread) bool { return v.ID == id })
	if i < 0 {
		t.Fatal("the engine has no such thread")
	}
	return snap.Threads[i]
}

// Sending in a thread snoozed from here wakes it, though the view that
// has the snooze hasn't come yet: the thread shows as snoozed from when
// it's snoozed, and the send goes by what it shows.
func TestSendingInAThreadSnoozedBeforeItsViewWakesIt(t *testing.T) {
	d := engine(t, daemon.Config{Dir: tempDir(t)}, fake.New(nil))
	seed(t, d, "/work/snooze", "Snoozed one")
	ep := heldViews{d.Endpoint(), make(chan struct{})}
	h := serveApp(t, ep)
	th := h.thread("Snoozed one")
	h.a.backend.SetDisposition(th.env, th.id(), api.Disposition{State: api.Snoozed, Until: time.Now().Add(time.Hour)})
	h.until("the engine to snooze it", func() bool { return stored(t, ep, th.id()).Disposition.State == api.Snoozed })
	if h.a.categoryOf(th) != catSnoozed {
		t.Fatalf("a thread snoozed from here shows in section %d before its view", h.a.categoryOf(th))
	}
	h.a.select_(th)
	h.a.draft = "wake up"
	h.a.send(th)
	close(ep.resume)
	var v api.Thread
	h.until("the views to catch up", func() bool {
		v = stored(t, ep, th.id())
		return th.View.Revision == v.Revision && len(th.Pending) == 0
	})
	if v.Disposition.State == api.Snoozed || h.a.categoryOf(th) == catSnoozed {
		t.Errorf("after a send the engine has the thread %v, the sidebar shows it in section %d", v.Disposition.State, h.a.categoryOf(th))
	}
}
