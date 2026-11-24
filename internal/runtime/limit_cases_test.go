package runtime_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
	"glitched/internal/runtime"
	"glitched/internal/store"
)

type askingAfterLimit struct {
	*fake.Adapter
	held *fake.Adapter
}

func (a askingAfterLimit) Open(ctx context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	if spec.Generation > 1 {
		return a.held.Open(ctx, spec)
	}
	return a.Adapter.Open(ctx, spec)
}

// Deleting the threads the engine continued frees their places: the next
// thread whose limit reset is continued.
func TestDeletingContinuedThreadsReleasesSlots(t *testing.T) {
	start := time.Now().UTC()
	reset := start.Add(time.Hour)
	var now atomic.Int64
	now.Store(start.UnixNano())
	clock := func() time.Time { return time.Unix(0, now.Load()) }
	ad := askingAfterLimit{fake.New(limitedAt(reset)), fake.New(fake.Asking())}
	st, err := store.Open(dbPath(t))
	if err != nil {
		t.Fatal(err)
	}
	h, err := runtime.New(ctx, st, provider.Mux{"fake": ad}, runtime.WithClock(clock), runtime.WithLimitResume(true))
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	t.Cleanup(node{st, h}.kill)
	ids := []api.ID{newThread(t, h), newThread(t, h), newThread(t, h)}
	for _, id := range ids {
		submit(t, h, id, dispatch("go", api.StartNow))
		until(t, h, id, "initial limit", func(th api.Thread) bool { return idle(th) && th.LastTurn != nil && th.LastTurn.Limit != nil })
	}
	now.Store(reset.Add(time.Second).UnixNano())
	submit(t, h, ids[0], api.ThreadRename{Title: "wake"})
	var active, waiting []api.ID
	until(t, h, ids[0], "two continues asking, and the third waiting its turn", func(api.Thread) bool {
		snap, err := h.Read(ctx, api.Query{})
		if err != nil {
			t.Fatal(err)
		}
		active, waiting = nil, nil
		for _, th := range snap.Threads {
			if len(th.Requests) > 0 {
				active = append(active, th.ID)
			}
			if !th.Live() {
				waiting = append(waiting, th.ID)
			}
		}
		return len(active) == 2 && len(waiting) == 1
	})
	for _, id := range active {
		submit(t, h, id, api.ThreadDelete{})
	}
	submit(t, h, waiting[0], api.ThreadRename{Title: "wake after deletion"})
	until(t, h, waiting[0], "the third thread continued", func(api.Thread) bool { return continuesOf(t, h, waiting[0]) == 1 })
}

type stopFirst struct {
	*fake.Adapter
	first *endsOnStop
}

func (a stopFirst) Open(ctx context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	if spec.Generation == 1 {
		return a.first, nil
	}
	return a.Adapter.Open(ctx, spec)
}

type endsOnStop struct {
	stopped chan struct{}
	end     provider.TurnEnded
}

func (a *endsOnStop) Do(ctx context.Context, act provider.Action) error {
	if _, ok := act.(provider.Interrupt); ok {
		close(a.stopped)
	}
	return nil
}

func (a *endsOnStop) Next(ctx context.Context) (provider.Event, error) {
	select {
	case <-a.stopped:
		a.stopped = nil
		return a.end, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (*endsOnStop) Close(context.Context) error { return nil }

// A turn the user stopped is held by no usage limit, even one its agent
// says ended it as the Stop arrived.
func TestRuntimeDoesNotContinueAStoppedLimitedTurn(t *testing.T) {
	start := time.Now().UTC()
	reset := start.Add(time.Hour)
	var now atomic.Int64
	now.Store(start.UnixNano())
	end := limitedAt(reset)[1].(provider.TurnEnded)
	first := &endsOnStop{make(chan struct{}), end}
	ad := stopFirst{fake.New(fake.Asking()), first}
	st, err := store.Open(dbPath(t))
	if err != nil {
		t.Fatal(err)
	}
	h, err := runtime.New(ctx, st, provider.Mux{"fake": ad}, runtime.WithClock(func() time.Time { return time.Unix(0, now.Load()) }), runtime.WithLimitResume(true))
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	t.Cleanup(node{st, h}.kill)
	id := newThread(t, h)
	submit(t, h, id, dispatch("go", api.StartNow))
	until(t, h, id, "initial turn running", func(th api.Thread) bool { return th.Run == api.RunRunning })
	submit(t, h, id, api.TurnInterrupt{})
	stopped := until(t, h, id, "stopped turn", func(th api.Thread) bool { return idle(th) && th.LastTurn != nil })
	if l := stopped.LastTurn.Limit; l != nil {
		t.Fatalf("a turn the user stopped is held by a usage limit that resets at %v, want none", l.Resets)
	}
	if _, ok := stopped.LimitResume(reset.Add(time.Second)); ok {
		t.Fatal("the engine would continue a turn the user stopped once the limit resets")
	}
}

type limitTwice struct {
	*fake.Adapter
	next *fake.Adapter
	last *fake.Adapter
}

func (a limitTwice) Open(ctx context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	switch spec.Generation {
	case 1:
		return a.Adapter.Open(ctx, spec)
	case 2:
		return a.next.Open(ctx, spec)
	default:
		return a.last.Open(ctx, spec)
	}
}

// A thread a second limit stops after its continue is continued again
// once that one resets, once each.
func TestASecondDistinctLimitContinuesOnce(t *testing.T) {
	start := time.Now().UTC()
	reset, again := start.Add(time.Hour), start.Add(2*time.Hour)
	var now atomic.Int64
	now.Store(start.UnixNano())
	ad := limitTwice{fake.New(limitedAt(reset)), fake.New(limitedAt(again)), fake.New(fake.Turn(false))}
	st, err := store.Open(dbPath(t))
	if err != nil {
		t.Fatal(err)
	}
	h, err := runtime.New(ctx, st, provider.Mux{"fake": ad}, runtime.WithClock(func() time.Time { return time.Unix(0, now.Load()) }), runtime.WithLimitResume(true))
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	t.Cleanup(node{st, h}.kill)
	id := newThread(t, h)
	submit(t, h, id, dispatch("go", api.StartNow))
	for _, at := range []time.Time{reset, again} {
		until(t, h, id, "expected usage limit", func(th api.Thread) bool {
			return idle(th) && th.LastTurn != nil && th.LastTurn.Limit != nil && th.LastTurn.Limit.Resets.Equal(at)
		})
		now.Store(at.Add(time.Second).UnixNano())
		submit(t, h, id, api.ThreadRename{Title: at.Format(time.RFC3339)})
	}
	until(t, h, id, "completed after second reset", func(th api.Thread) bool {
		return idle(th) && th.LastTurn != nil && th.LastTurn.Outcome == api.OutcomeCompleted
	})
	if n := continuesOf(t, h, id); n != 2 {
		t.Fatalf("%d continuations across two distinct reset windows, want 2", n)
	}
	assertReplay(t, st)
}
