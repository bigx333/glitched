package runtime_test

import (
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
)

func threadOn(t *testing.T, ep api.Endpoint, name string) api.ID {
	t.Helper()
	id := api.NewID()
	submit(t, ep, id, api.ThreadCreate{Workspace: "/src/app", Title: "on " + name, Model: api.ModelRef{Provider: name, Model: "m"}})
	return id
}

func lastEnd(t *testing.T, ep api.Endpoint, thread api.ID) api.TurnEnded {
	t.Helper()
	evs := events(t, ep, thread)
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Type == "turn.ended" {
			b, err := evs[i].Decode()
			if err != nil {
				t.Fatal(err)
			}
			return b.(api.TurnEnded)
		}
	}
	t.Fatal("no turn ended")
	return api.TurnEnded{}
}

func TestEachRunGoesToItsThreadsProvider(t *testing.T) {
	a := fake.New([]provider.Event{provider.TextDelta{Text: "from a"}})
	b := fake.New([]provider.Event{provider.TextDelta{Text: "from b"}})
	n := serve(t, dbPath(t), provider.Mux{"a": a, "b": b})
	onA, onB := threadOn(t, n.h, "a"), threadOn(t, n.h, "b")
	submit(t, n.h, onA, dispatch("go", api.StartNow))
	submit(t, n.h, onB, dispatch("go", api.StartNow))
	until(t, n.h, onA, "a's turn", idle)
	until(t, n.h, onB, "b's turn", idle)
	if got := transcript(t, events(t, n.h, onA)); got != "from a" {
		t.Errorf("the thread on a says %q, want a's script", got)
	}
	if got := transcript(t, events(t, n.h, onB)); got != "from b" {
		t.Errorf("the thread on b says %q, want b's script", got)
	}
	if a.Starts() != 1 || b.Starts() != 1 {
		t.Errorf("starts: a %d, b %d; want 1 each", a.Starts(), b.Starts())
	}
}

func TestAProviderWithNoAdapterFailsTheTurn(t *testing.T) {
	ad := fake.New(fake.Turn(false))
	n := serve(t, dbPath(t), provider.Mux{"fake": ad})
	thread := threadOn(t, n.h, "nope")
	submit(t, n.h, thread, dispatch("go", api.StartNow))
	until(t, n.h, thread, "the turn's end", idle)
	end := lastEnd(t, n.h, thread)
	if end.Outcome != api.OutcomeFailed || !strings.Contains(end.Error, `no adapter for "nope"`) {
		t.Fatalf("the turn ended %v with %q, want it failed naming the provider", end.Outcome, end.Error)
	}
	if ad.Starts() != 0 {
		t.Fatalf("another provider's adapter started %d turns", ad.Starts())
	}

	other := newThread(t, n.h)
	submit(t, n.h, other, dispatch("go", api.StartNow))
	until(t, n.h, other, "a turn on fake", idle)
	if end := lastEnd(t, n.h, other); end.Outcome != api.OutcomeCompleted {
		t.Fatalf("a turn on a known provider then ended %v, want completed", end.Outcome)
	}
}

// After a restart each live run is reconciled by its own thread's adapter.
func TestARestartReconcilesEachRunWithItsProvider(t *testing.T) {
	crashWaiting := func(t *testing.T, path string, providers provider.Mux) (onA, onB api.ID) {
		n := serve(t, path, providers)
		onA, onB = threadOn(t, n.h, "a"), threadOn(t, n.h, "b")
		submit(t, n.h, onA, dispatch("go", api.StartNow))
		submit(t, n.h, onB, dispatch("go", api.StartNow))
		until(t, n.h, onA, "a's approval", waiting)
		until(t, n.h, onB, "b's approval", waiting)
		n.kill()
		return onA, onB
	}

	t.Run("each adapter restores its own run", func(t *testing.T) {
		path := dbPath(t)
		a, b := fake.New(fake.Turn(true)), fake.New(fake.Turn(true))
		onA, onB := crashWaiting(t, path, provider.Mux{"a": a, "b": b})
		n := serve(t, path, provider.Mux{"a": a, "b": b})
		for _, thread := range []api.ID{onA, onB} {
			respond(t, n.h, thread, until(t, n.h, thread, "still waiting", waiting).Requests[0])
			until(t, n.h, thread, "the restored turn's end", idle)
			evs := events(t, n.h, thread)
			if count(evs, "turn.restored") != 1 || lastEnd(t, n.h, thread).Outcome != api.OutcomeCompleted {
				t.Errorf("thread %s: restored %d times, ended %v; want restored once and completed", thread, count(evs, "turn.restored"), lastEnd(t, n.h, thread).Outcome)
			}
		}
		if a.Starts() != 1 || b.Starts() != 1 {
			t.Errorf("starts: a %d, b %d; want 1 each", a.Starts(), b.Starts())
		}
	})

	t.Run("a provider gone since the crash leaves its run uncertain", func(t *testing.T) {
		path := dbPath(t)
		a, b := fake.New(fake.Turn(true)), fake.New(fake.Turn(true))
		onA, onB := crashWaiting(t, path, provider.Mux{"a": a, "b": b})
		n := serve(t, path, provider.Mux{"a": a})
		until(t, n.h, onA, "a's run restored", waiting)
		until(t, n.h, onB, "b's run uncertain", uncertain)
	})

	t.Run("a delete closes the agent of the thread's provider", func(t *testing.T) {
		path := dbPath(t)
		a := fake.New(fake.Turn(true))
		b := &unreachable{Adapter: fake.New(fake.Turn(true)), tried: make(chan struct{}, 1)}
		n := serve(t, path, provider.Mux{"a": a, "b": b})
		thread := threadOn(t, n.h, "b")
		run := submit(t, n.h, thread, dispatch("go", api.StartNow)).Command
		until(t, n.h, thread, "the approval", waiting)
		b.fail.Store(true)
		submit(t, n.h, thread, api.ThreadDelete{})
		<-b.tried
		n.kill()
		b.fail.Store(false)
		serve(t, path, provider.Mux{"a": a, "b": b})
		gone(t, b, thread, run)
	})
}

// A deleted thread's close waits for its provider to be registered again,
// and holds up neither other threads nor the host's shutdown.
func TestACloseWaitingOnAMissingProviderBlocksNothingElse(t *testing.T) {
	path := dbPath(t)
	b := &unreachable{Adapter: fake.New(fake.Turn(true)), tried: make(chan struct{}, 1)}
	n := serve(t, path, provider.Mux{"b": b})
	deleted := threadOn(t, n.h, "b")
	submit(t, n.h, deleted, dispatch("go", api.StartNow))
	until(t, n.h, deleted, "the approval", waiting)
	b.fail.Store(true)
	submit(t, n.h, deleted, api.ThreadDelete{})
	<-b.tried
	n.kill()

	n = serve(t, path, provider.Mux{"a": fake.New(fake.Turn(false))})
	db := sideDB(t, path)
	closeState := func() (state string) {
		t.Helper()
		if err := db.QueryRow("SELECT state FROM effects WHERE kind = 'agent.close'").Scan(&state); err != nil {
			t.Fatal(err)
		}
		return state
	}
	for deadline := time.Now().Add(5 * time.Second); closeState() == "pending"; time.Sleep(2 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the close was never taken up")
		}
	}

	other := threadOn(t, n.h, "a")
	submit(t, n.h, other, dispatch("go", api.StartNow))
	until(t, n.h, other, "a's turn", idle)
	if end := lastEnd(t, n.h, other); end.Outcome != api.OutcomeCompleted {
		t.Fatalf("another thread's turn ended %v, want completed", end.Outcome)
	}

	began := time.Now()
	n.h.Close()
	if took := time.Since(began); took > time.Second {
		t.Errorf("Close took %v with a close waiting on a missing provider", took)
	}
	if state := closeState(); state == "done" {
		t.Error("the close is done though no adapter closed the agent")
	}
}
