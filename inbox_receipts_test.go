package main

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/daemon"
	"glitched/internal/provider/fake"
)

// heldViews is an engine whose shell sends its first item, the snapshot,
// and then nothing until resume closes, so its receipts run ahead of its
// views; and whose next submit fails once fail is set, as one the network
// lost on its way.
type heldViews struct {
	api.Endpoint
	resume chan struct{}
	fail   *atomic.Bool
}

func (e heldViews) Open(ctx context.Context, sub api.Subscription) (api.Stream, error) {
	st, err := e.Endpoint.Open(ctx, sub)
	if err != nil || !sub.Thread.IsZero() {
		return st, err
	}
	return &heldStream{Stream: st, resume: e.resume}, nil
}

func (e heldViews) Submit(ctx context.Context, c api.Command) (api.Receipt, error) {
	if e.fail.Swap(false) {
		return api.Receipt{}, errors.New("heldViews: lost on its way")
	}
	return e.Endpoint.Submit(ctx, c)
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

// answersIn waits till the answers to what was sent on the thread before
// are in: a thread's commands go one after another, so they are once a
// rename sent after them, to no title, which the engine refuses, has
// failed.
func answersIn(h *harness, th thread) {
	h.t.Helper()
	id := h.a.backend.Rename(th.env, th.id(), "")
	h.until("the answers before the fence", func() bool {
		return slices.ContainsFunc(th.Pending, func(p client.Pending) bool { return p.Command == id && p.Err != nil })
	})
	h.a.backend.Dismiss(th.env, th.id(), id)
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

// Sending in a snoozed thread wakes it, however its view and what was
// chosen here disagree as the send goes: a snooze taken but not yet in a
// view, or a Back to inbox on its way that fails.
func TestSendingInASnoozedThreadWakesIt(t *testing.T) {
	snooze := api.Disposition{State: api.Snoozed, Until: time.Now().Add(time.Hour)}
	for name, before := range map[string]func(h *harness, ep heldViews, th thread){
		"its snooze taken, its view behind": func(h *harness, ep heldViews, th thread) {
			h.a.backend.SetDisposition(th.env, th.id(), snooze)
			answersIn(h, th)
		},
		"its Back to inbox lost on its way": func(h *harness, ep heldViews, th thread) {
			h.a.backend.SetDisposition(th.env, th.id(), snooze)
			close(ep.resume)
			h.until("the snoozed view", func() bool { return th.View.Disposition.State == api.Snoozed && len(th.Pending) == 0 })
			ep.fail.Store(true)
			h.a.backend.SetDisposition(th.env, th.id(), api.Disposition{})
		},
	} {
		t.Run(name, func(t *testing.T) {
			d := engine(t, daemon.Config{Dir: tempDir(t)}, fake.New(nil))
			seed(t, d, "/work/snooze", "Snoozed one")
			ep := heldViews{d.Endpoint(), make(chan struct{}), new(atomic.Bool)}
			h := serveApp(t, ep)
			th := h.thread("Snoozed one")
			before(h, ep, th)
			h.a.select_(th)
			h.a.draft = "wake up"
			h.a.send(th)
			answersIn(h, th)
			if v := stored(t, ep, th.id()); v.Disposition.State == api.Snoozed {
				t.Errorf("the engine took the send and keeps the thread snoozed")
			}
		})
	}
}
