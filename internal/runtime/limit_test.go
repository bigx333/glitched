package runtime_test

import (
	"sync/atomic"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
	"glitched/internal/runtime"
	"glitched/internal/store"
)

// limitedAt is a script whose turn a usage limit ends, which resets at
// resets.
func limitedAt(resets time.Time) []provider.Event {
	return []provider.Event{
		provider.TextDelta{Text: "Working."},
		provider.TurnEnded{Outcome: api.OutcomeFailed, Error: "You've hit your usage limit.", Limit: &api.UsageLimit{Resets: resets}},
	}
}

func continuesOf(t *testing.T, ep api.Endpoint, thread api.ID) (n int) {
	t.Helper()
	for _, e := range events(t, ep, thread) {
		if b, _ := e.Decode(); b != nil {
			if q, ok := b.(api.MessageQueued); ok && q.Text == api.LimitContinueText {
				n++
			}
		}
	}
	return n
}

// With the switch on, the engine continues each of the user's threads a
// usage limit stopped once the limit resets, and only once: the turn it
// continues ends past that reset. A thread the user cancelled it on, or
// snoozed, is left for the user, as is every thread while the switch is
// off.
func TestAUsageLimitContinuesTheThreadOnceItResets(t *testing.T) {
	start := time.Now()
	var now atomic.Int64
	now.Store(start.UnixNano())
	clock := func() time.Time { return time.Unix(0, now.Load()) }
	resets := start.Add(time.Hour)
	ad := fake.New(limitedAt(resets))
	st, err := store.Open(dbPath(t))
	if err != nil {
		t.Fatal(err)
	}
	h, err := runtime.New(ctx, st, provider.Mux{"fake": ad}, runtime.WithClock(clock))
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	n := node{st, h}
	t.Cleanup(n.kill)

	open := []api.ID{newThread(t, h), newThread(t, h), newThread(t, h)}
	cancelled, snoozed := newThread(t, h), newThread(t, h)
	all := append(append([]api.ID{}, open...), cancelled, snoozed)
	for _, id := range all {
		submit(t, h, id, dispatch("go", api.StartNow))
		th := until(t, h, id, "the limited turn", func(th api.Thread) bool { return idle(th) && th.LastTurn != nil })
		if l := th.LastTurn.Limit; l == nil || !l.Resets.Equal(resets) {
			t.Fatalf("the last turn's limit is %+v, want one that resets at %v", l, resets)
		}
	}
	submit(t, h, cancelled, api.ResumeCancel{})
	until(t, h, cancelled, "the cancel", func(th api.Thread) bool { return th.LastTurn.ResumeCancelled })
	submit(t, h, snoozed, api.InboxSet{Disposition: api.Disposition{State: api.Snoozed, Until: start.Add(2 * time.Hour)}})

	now.Store(resets.Add(time.Minute).UnixNano())
	submit(t, h, open[0], api.InboxSet{Disposition: api.Disposition{State: api.Open}})
	if got := ad.Starts(); got != len(all) {
		t.Fatalf("%d starts with the switch off, want %d", got, len(all))
	}

	h.SetLimitResume(true)
	for _, id := range open {
		until(t, h, id, "the continued turn", func(th api.Thread) bool {
			return idle(th) && th.LastTurn != nil && th.LastTurn.Ended.After(resets)
		})
	}
	for _, id := range open {
		if got := continuesOf(t, h, id); got != 1 {
			t.Errorf("continued %d times, want once", got)
		}
	}
	submit(t, h, open[0], api.InboxSet{Disposition: api.Disposition{State: api.Open}})
	for _, id := range []api.ID{cancelled, snoozed} {
		if got := continuesOf(t, h, id); got != 0 {
			t.Errorf("a thread cancelled or snoozed was continued %d times, want left for the user", got)
		}
	}
	if got := ad.Starts(); got != len(all)+len(open) {
		t.Errorf("%d starts, want %d", got, len(all)+len(open))
	}
	assertReplay(t, st)
}
