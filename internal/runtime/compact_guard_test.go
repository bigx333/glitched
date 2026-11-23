package runtime_test

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
	"glitched/internal/provider"
	"glitched/internal/runtime"
	"glitched/internal/store"
)

// idleSeed is a Claude thread whose one turn ended with 250k tokens of a
// cache that expires then.
func idleSeed(t *testing.T, st *store.Store, expires time.Time) (api.ID, api.ID) {
	t.Helper()
	thread, run := api.NewID(), api.NewID()
	when := expires.Add(-time.Hour)
	command := func(id api.ID, body api.Intent) {
		t.Helper()
		if _, err := st.Submit(ctx, api.Command{ID: id, Target: thread, Body: body}, when); err != nil {
			t.Fatal(err)
		}
	}
	command(api.NewID(), api.ThreadCreate{Workspace: t.TempDir(), Title: "idle", Model: api.ModelRef{Provider: "claude", Model: "haiku"}, Access: api.AccessFull})
	command(run, dispatch("prior user turn", api.StartNow))
	effect, ok, err := st.Lease(ctx, func() []api.ID { return nil })
	if err != nil || !ok {
		t.Fatalf("lease first run: %v, %v", ok, err)
	}
	for _, step := range []struct {
		obs engine.Observation
		id  int64
	}{
		{engine.Started{Run: run}, effect.ID},
		{engine.Context{Run: run, Model: "haiku", Used: 250_000, Window: 1_000_000, CacheExpires: expires}, 0},
		{engine.Ended{Run: run, Outcome: api.OutcomeCompleted}, 0},
	} {
		if _, err := st.Observe(ctx, thread, step.obs, when, step.id); err != nil {
			t.Fatal(err)
		}
	}
	return thread, run
}

// seeded is a new store holding an idle thread for each expiry, as
// idleSeed leaves one, closed for a host to serve: idleSeed leases its
// thread's start itself, which a host already serving the store races it
// for.
func seeded(t *testing.T, expires ...time.Time) (path string, threads []api.ID) {
	t.Helper()
	path = dbPath(t)
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, e := range expires {
		id, _ := idleSeed(t, st, e)
		threads = append(threads, id)
	}
	return path, threads
}

// windowOf is when a thread whose cache expires then may compact.
func windowOf(expires time.Time) time.Time { return expires.Add(-engine.ClaudeCacheTTL / 10) }

func compactions(ad *compactAdapter) []recordedStart {
	return slices.DeleteFunc(ad.recorded(), func(r recordedStart) bool { return r.text != "/compact" })
}

// blockedOpen holds a compaction's agent open until release, and says so
// on opening: the first time, as the thread may compact again later.
type blockedOpen struct {
	*compactAdapter
	opening chan struct{}
	opened  *sync.Once
	release chan struct{}
}

func (ad blockedOpen) Open(ctx context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	if spec.ResumeOnly {
		ad.opened.Do(func() { close(ad.opening) })
		select {
		case <-ad.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return ad.compactAdapter.Open(ctx, spec)
}

// A restart that loses a compaction ends it, and the message the user
// queued behind it runs with no Stop from them.
func TestALostCompactionDoesNotHoldTheQueue(t *testing.T) {
	expires := time.Now().UTC().Add(time.Hour)
	now := windowOf(expires).Add(time.Second)
	path, seeds := seeded(t, expires)
	thread := seeds[0]
	release := make(chan struct{})
	defer close(release)
	ad := &compactAdapter{onStart: func(spec provider.SessionSpec, text string) ([]provider.Event, <-chan struct{}) {
		if text == "/compact" {
			return nil, release
		}
		return nil, nil
	}}
	n := serveOpts(t, path, provider.Mux{"claude": ad}, runtime.WithClock(func() time.Time { return now }), runtime.WithIdleCompaction(false))
	n.h.SetIdleCompaction(true)
	until(t, n.h, thread, "the compaction to run", func(v api.Thread) bool {
		return v.Run == api.RunRunning && v.Turn != nil && v.Turn.Silent
	})
	submit(t, n.h, thread, dispatch("next", api.StartNow))
	n.stop()

	n = serveOpts(t, path, provider.Mux{"claude": ad}, runtime.WithClock(func() time.Time { return now }), runtime.WithIdleCompaction(false))
	until(t, n.h, thread, "the queued message to run after the lost compaction", func(v api.Thread) bool {
		starts := ad.recorded()
		return v.Run == api.RunIdle && len(starts) == 2 && starts[1].text == "next"
	})
}

// A compaction admitted before a restart that turned idle compaction off,
// or that the clock has passed, is never sent.
func TestACompactionNoLongerDueIsNeverSent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		enabled bool
		late    time.Duration
	}{{"turned off", false, 0}, {"past its window", true, time.Hour}} {
		t.Run(tc.name, func(t *testing.T) {
			path := dbPath(t)
			st, err := store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			expires := time.Now().UTC().Add(time.Hour)
			thread, prior := idleSeed(t, st, expires)
			if _, err := st.Observe(ctx, thread, engine.IdleCompact{Run: api.NewID(), AfterRun: prior}, windowOf(expires).Add(time.Second), 0); err != nil {
				t.Fatal(err)
			}
			st.Close()
			ad := &compactAdapter{}
			n := serveOpts(t, path, provider.Mux{"claude": ad}, runtime.WithClock(func() time.Time {
				return windowOf(expires).Add(time.Second + tc.late)
			}), runtime.WithIdleCompaction(tc.enabled))
			until(t, n.h, thread, "the compaction to end unsent", func(v api.Thread) bool { return v.Run == api.RunIdle })
			if got := compactions(ad); len(got) != 0 {
				t.Errorf("sent %+v", got)
			}
		})
	}
}

// What the user does while a compaction's agent opens, or the time it
// takes, stops it before its /compact goes out.
func TestAChangeWhileACompactionOpensCancelsIt(t *testing.T) {
	for _, tc := range []string{"expiry", "opt-out", "snooze", "access", "model", "message"} {
		t.Run(tc, func(t *testing.T) {
			expires := time.Now().UTC().Add(time.Hour)
			var shift atomic.Int64
			clock := func() time.Time { return windowOf(expires).Add(time.Second + time.Duration(shift.Load())) }
			// The message's turn reports the cache it made, as Claude's
			// does each turn, so the thread isn't due again in this window.
			fresh := func(_ provider.SessionSpec, text string) ([]provider.Event, <-chan struct{}) {
				if text == "next" {
					return []provider.Event{provider.Context{Model: "haiku", Used: 250_000, Window: 1_000_000, CacheExpires: expires.Add(time.Hour)}}, nil
				}
				return nil, nil
			}
			ad := blockedOpen{compactAdapter: &compactAdapter{onStart: fresh}, opening: make(chan struct{}), opened: new(sync.Once), release: make(chan struct{})}
			path, seeds := seeded(t, expires)
			thread := seeds[0]
			n := serveOpts(t, path, provider.Mux{"claude": ad}, runtime.WithClock(clock), runtime.WithIdleCompaction(false))
			n.h.SetIdleCompaction(true)
			select {
			case <-ad.opening:
			case <-time.After(10 * time.Second):
				t.Fatal("the compaction never opened")
			}
			switch tc {
			case "expiry":
				shift.Store(int64(time.Minute))
			case "opt-out":
				n.h.SetIdleCompaction(false)
			case "snooze":
				submit(t, n.h, thread, api.InboxSet{Disposition: api.Disposition{State: api.Snoozed, Until: expires.Add(time.Hour)}})
			case "access":
				submit(t, n.h, thread, api.ThreadConfigure{Access: new(api.AccessAsk)})
			case "model":
				submit(t, n.h, thread, api.ThreadConfigure{Model: new("sonnet")})
			case "message":
				submit(t, n.h, thread, dispatch("next", api.StartNow))
			}
			close(ad.release)
			until(t, n.h, thread, "the compaction to end", func(v api.Thread) bool {
				return v.Run == api.RunIdle && (tc != "message" || len(ad.recorded()) == 1)
			})
			if got := compactions(ad.compactAdapter); len(got) != 0 {
				t.Errorf("after a change (%s) while it opened, the compaction sent %+v", tc, got)
			}
		})
	}
}

// A timer the clock refused, as after it stepped back, goes again, and a
// snooze that ends inside the window wakes the thread then.
func TestARefusedOrSnoozedCompactionGoesWhenDue(t *testing.T) {
	for _, tc := range []string{"clock-rollback", "snooze-wake"} {
		t.Run(tc, func(t *testing.T) {
			expires := time.Now().UTC().Add(time.Hour)
			window := windowOf(expires)
			t0 := time.Now()
			var calls atomic.Int64
			clock := func() time.Time {
				at := window.Add(100*time.Millisecond + time.Since(t0))
				if calls.Add(1) == 3 && tc == "clock-rollback" {
					at = window.Add(-2 * time.Minute)
				}
				return at
			}
			path := dbPath(t)
			st, err := store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			thread, _ := idleSeed(t, st, expires)
			if tc == "snooze-wake" {
				if _, err := st.Submit(ctx, api.Command{ID: api.NewID(), Target: thread, Body: api.InboxSet{Disposition: api.Disposition{
					State: api.Snoozed, Until: window.Add(500 * time.Millisecond),
				}}}, window.Add(-time.Minute)); err != nil {
					t.Fatal(err)
				}
			}
			st.Close()
			ad := &compactAdapter{}
			n := serveOpts(t, path, provider.Mux{"claude": ad}, runtime.WithClock(clock))
			until(t, n.h, thread, "the compaction to run without a rescan", func(v api.Thread) bool {
				return v.Run == api.RunIdle && len(compactions(ad)) == 1
			})
		})
	}
}

// Many compactions due at once run two at a time, the one whose window
// closes first first, and one whose window closed while it waited never
// runs.
func TestCompactionsRunTwoAtATimeNearestFirst(t *testing.T) {
	base := time.Now().UTC().Add(time.Hour)
	path := dbPath(t)
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var order []api.ID
	for i := range 6 {
		id, _ := idleSeed(t, st, base.Add(engine.ClaudeCacheTTL/10-time.Duration(i)*5*time.Second))
		order = append([]api.ID{id}, order...)
	}
	lapsed, _ := idleSeed(t, st, base.Add(-time.Second))
	st.Close()
	var mu sync.Mutex
	running, most := 0, 0
	ad := &compactAdapter{onStart: func(provider.SessionSpec, string) ([]provider.Event, <-chan struct{}) {
		mu.Lock()
		running++
		most = max(most, running)
		mu.Unlock()
		gate := make(chan struct{})
		go func() {
			time.Sleep(20 * time.Millisecond)
			mu.Lock()
			running--
			mu.Unlock()
			close(gate)
		}()
		return nil, gate
	}}
	n := serveOpts(t, path, provider.Mux{"claude": ad}, runtime.WithClock(func() time.Time { return base }))
	for _, id := range order {
		until(t, n.h, id, "its compaction", func(v api.Thread) bool {
			return v.Run == api.RunIdle && slices.ContainsFunc(compactions(ad), func(r recordedStart) bool { return r.spec.Thread == id })
		})
	}
	var got []api.ID
	for _, r := range compactions(ad) {
		got = append(got, r.spec.Thread)
	}
	if most > 2 {
		t.Errorf("%d compactions ran at once, want at most 2", most)
	}
	if !slices.Equal(got[:2], order[:2]) && !slices.Equal(got[:2], []api.ID{order[1], order[0]}) {
		t.Errorf("the first two compactions were %v, want the two whose windows close first, %v", got[:2], order[:2])
	}
	if slices.Contains(got, lapsed) {
		t.Error("a thread whose window had closed compacted")
	}
}

// A CLI update's Hold is not refused because of a compaction: it stops
// it, waits for it to end, and no compaction starts while it holds.
func TestAHoldStopsACompaction(t *testing.T) {
	expires := time.Now().UTC().Add(time.Hour)
	now := windowOf(expires).Add(time.Second)
	ad := &compactAdapter{onStart: func(spec provider.SessionSpec, text string) ([]provider.Event, <-chan struct{}) {
		return nil, make(chan struct{})
	}}
	path, seeds := seeded(t, expires, expires)
	thread, other := seeds[0], seeds[1]
	n := serveOpts(t, path, provider.Mux{"claude": ad}, runtime.WithClock(func() time.Time { return now }), runtime.WithIdleCompaction(false))
	n.h.SetIdleCompaction(true)
	until(t, n.h, thread, "the compaction to run", func(v api.Thread) bool { return v.Run == api.RunRunning })
	hctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	release, err := n.h.Hold(hctx, "claude", false)
	if err != nil {
		t.Fatalf("Hold during a compaction: %v, want it to stop the compaction and hold", err)
	}
	defer release()
	for _, id := range []api.ID{thread, other} {
		if v := until(t, n.h, id, "no compaction while held", func(v api.Thread) bool { return v.Run == api.RunIdle }); v.Run != api.RunIdle {
			t.Errorf("thread %v is %v while held", id, v.Run)
		}
	}
	ad.mu.Lock()
	stops := ad.stops
	ad.mu.Unlock()
	if stops == 0 {
		t.Error("the Hold did not stop the compaction")
	}
}
