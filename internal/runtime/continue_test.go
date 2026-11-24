package runtime_test

import (
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
	"glitched/internal/runtime"
	"glitched/internal/store"
)

// autoHost is a host on path that continues the runs its boot lost.
func autoHost(t *testing.T, path string, providers provider.Mux) node {
	t.Helper()
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := runtime.New(ctx, st, providers, runtime.WithAutoContinue(true))
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	n := node{st, h}
	t.Cleanup(n.kill)
	return n
}

func held() *fake.Adapter {
	ad := fake.New(fake.Turn(false))
	ad.HoldStart = true
	return ad
}

func startsReach(t *testing.T, ad *fake.Adapter, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for ad.Starts() < n {
		if time.Now().After(deadline) {
			t.Fatalf("%d starts, want %d", ad.Starts(), n)
		}
		time.Sleep(time.Millisecond)
	}
}

func lostFor(t *testing.T, ep api.Endpoint, thread api.ID) api.UncertainReason {
	t.Helper()
	for _, e := range events(t, ep, thread) {
		if b, _ := e.Decode(); b != nil {
			if u, ok := b.(api.TurnUncertain); ok {
				return u.Reason
			}
		}
	}
	return api.UncertainUnknown
}

// With auto-continue on, a restarted host continues the user's threads it
// lost while they worked, two at a time: the next once one of those is no
// longer live. One that waited on the user's answer, or is snoozed, stays
// uncertain for the user.
func TestARestartContinuesTheRunsItLost(t *testing.T) {
	path := dbPath(t)
	work, ask := held(), fake.New(fake.Turn(true))
	n := serve(t, path, provider.Mux{"fake": work, "ask": ask})
	working := []api.ID{newThread(t, n.h), newThread(t, n.h), newThread(t, n.h)}
	snoozed := newThread(t, n.h)
	for _, id := range append(working, snoozed) {
		submit(t, n.h, id, dispatch("go", api.StartNow))
	}
	submit(t, n.h, snoozed, api.InboxSet{Disposition: api.Disposition{State: api.Snoozed, Until: time.Now().Add(time.Hour)}})
	asking := threadOn(t, n.h, "ask")
	submit(t, n.h, asking, dispatch("go", api.StartNow))
	until(t, n.h, asking, "approval", waiting)
	startsReach(t, work, 4)
	n.kill()

	next := fake.New(fake.Turn(true))
	n = autoHost(t, path, provider.Mux{"fake": next, "ask": fake.New(fake.Turn(true))})
	count := func(ok func(api.Thread) bool) (in []api.Thread) {
		for _, id := range working {
			if th := until(t, n.h, id, "a read", func(api.Thread) bool { return true }); ok(th) {
				in = append(in, th)
			}
		}
		return in
	}
	until(t, n.h, working[0], "two continued", func(api.Thread) bool { return len(count(waiting)) == 2 })
	if left := count(uncertain); len(left) != 1 || next.Starts() != 2 {
		t.Fatalf("%d left uncertain and %d starts with two continued runs waiting, want 1 and 2", len(left), next.Starts())
	}
	first := count(waiting)[0]
	respond(t, n.h, first.ID, first.Requests[0])
	until(t, n.h, working[0], "the third continued", func(api.Thread) bool { return len(count(uncertain)) == 0 && next.Starts() == 3 })
	until(t, n.h, working[0], "every run at rest", func(api.Thread) bool {
		return len(count(func(th api.Thread) bool { return idle(th) || waiting(th) })) == len(working)
	})

	for _, id := range []api.ID{snoozed, asking} {
		if th := until(t, n.h, id, "a read", func(api.Thread) bool { return true }); th.Run != api.RunUncertain {
			t.Errorf("a thread snoozed or waiting on its approval is %v, want left uncertain", th.Run)
		}
	}
	for _, id := range append(working, snoozed, asking) {
		if why := lostFor(t, n.h, id); why != api.UncertainRestarted {
			t.Errorf("lost for %v, want restarted", why)
		}
	}
	for _, id := range working {
		evs := events(t, n.h, id)
		last := api.MessageQueued{}
		for _, e := range evs {
			if b, _ := e.Decode(); b != nil {
				if q, ok := b.(api.MessageQueued); ok {
					last = q
				}
			}
		}
		if last.Text != api.ContinueText || last.Mode != api.RestartActive {
			t.Errorf("the continue was queued as %+v", last)
		}
	}
	assertReplay(t, n.st)
}

// A run that is lost each time it is continued is continued three times
// in a row, each after a real restart, and then left for the user.
func TestAChainOfLostContinuesStopsAtThree(t *testing.T) {
	path := dbPath(t)
	first := held()
	n := start(t, path, first)
	chained := newThread(t, n.h)
	submit(t, n.h, chained, dispatch("go", api.StartNow))
	startsReach(t, first, 1)
	n.kill()
	for range 3 {
		ad := held()
		n = autoHost(t, path, provider.Mux{"fake": ad})
		startsReach(t, ad, 1)
		n.kill()
	}
	last := held()
	n = autoHost(t, path, provider.Mux{"fake": last})
	select {
	case <-n.h.LostContinued():
	case <-time.After(5 * time.Second):
		t.Fatal("the host never got through the runs it lost")
	}
	if th := until(t, n.h, chained, "a read", func(api.Thread) bool { return true }); th.Run != api.RunUncertain || last.Starts() != 0 {
		t.Fatalf("after three continues lost in a row the thread is %v with %d starts, want left uncertain", th.Run, last.Starts())
	}
	assertReplay(t, n.st)
}
