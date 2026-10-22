package runtime_test

import (
	"reflect"
	"strings"
	"testing"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
)

// crashDuring brings a thread to an approval, holds the first provider call
// of a's type that body causes, and crashes the host while it is in flight,
// so the effect is left leased.
func crashDuring(t *testing.T, path string, g *gated, a provider.Action, body func(n node, thread api.ID)) api.ID {
	t.Helper()
	n := start(t, path, g)
	thread := newThread(t, n.h)
	submit(t, n.h, thread, dispatch("go", api.StartNow))
	until(t, n.h, thread, "approval", waiting)
	entered, never := make(chan struct{}, 1), make(chan struct{})
	g.setGate(func(_ api.ID, act provider.Action) <-chan struct{} {
		if reflect.TypeOf(act) == reflect.TypeOf(a) {
			select {
			case entered <- struct{}{}:
			default:
			}
			return never
		}
		return nil
	})
	body(n, thread)
	<-entered
	n.crash()
	g.setGate(nil)
	return thread
}

func TestCrashWithALeasedEffect(t *testing.T) {
	t.Run("steer becomes uncertain and is never sent again", func(t *testing.T) {
		path, g := dbPath(t), newGated(fake.Turn(true))
		thread := crashDuring(t, path, g, provider.Steer{}, func(n node, thread api.ID) {
			submit(t, n.h, thread, dispatch("hurry", api.SteerActive))
		})
		n := start(t, path, g)
		respond(t, n.h, thread, until(t, n.h, thread, "restored and waiting", waiting).Requests[0])
		until(t, n.h, thread, "turn end", idle)
		evs := events(t, n.h, thread)
		if count(evs, "message.uncertain") != 1 || g.count(provider.Steer{}) != 0 || strings.Contains(transcript(t, evs), "steered") {
			t.Fatalf("message.uncertain %d, steers taken %d, transcript %q; want the steer uncertain and never resent",
				count(evs, "message.uncertain"), g.count(provider.Steer{}), transcript(t, evs))
		}
		assertReplay(t, n.st)
	})

	t.Run("interrupt runs again", func(t *testing.T) {
		path, g := dbPath(t), newGated(fake.Turn(true))
		thread := crashDuring(t, path, g, provider.Interrupt{}, func(n node, thread api.ID) {
			submit(t, n.h, thread, api.TurnInterrupt{})
		})
		n := start(t, path, g)
		until(t, n.h, thread, "turn end", idle)
		if got := outcomes(t, n.h, thread); len(got) != 1 || got[0] != api.OutcomeInterrupted || g.count(provider.Interrupt{}) != 1 {
			t.Fatalf("turn ended %v after %d interrupts reached the agent, want interrupted after 1", got, g.count(provider.Interrupt{}))
		}
		assertReplay(t, n.st)
	})

	t.Run("answer is delivered again", func(t *testing.T) {
		path, g := dbPath(t), newGated(fake.Turn(true))
		thread := crashDuring(t, path, g, provider.Respond{}, func(n node, thread api.ID) {
			respond(t, n.h, thread, until(t, n.h, thread, "approval", waiting).Requests[0])
		})
		n := start(t, path, g)
		until(t, n.h, thread, "turn end", idle)
		evs := events(t, n.h, thread)
		if got := outcomes(t, n.h, thread); len(got) != 1 || got[0] != api.OutcomeCompleted || count(evs, "request.delivered") != 1 || g.count(provider.Respond{}) != 1 {
			t.Fatalf("turn ended %v with %d request.delivered after %d answers reached the agent, want completed, 1 and 1",
				got, count(evs, "request.delivered"), g.count(provider.Respond{}))
		}
		assertReplay(t, n.st)
	})

	t.Run("answer for a lost agent is invalidated", func(t *testing.T) {
		path, g := dbPath(t), newGated(fake.Turn(true))
		thread := crashDuring(t, path, g, provider.Respond{}, func(n node, thread api.ID) {
			respond(t, n.h, thread, until(t, n.h, thread, "approval", waiting).Requests[0])
		})
		n := start(t, path, fake.New(fake.Turn(true)))
		until(t, n.h, thread, "invalidated", func(api.Thread) bool {
			return count(events(t, n.h, thread), "request.invalidated") == 1
		})
		snap, err := n.h.Read(ctx, api.Query{Thread: thread})
		if err != nil {
			t.Fatal(err)
		}
		if snap.Threads[0].Run != api.RunUncertain || count(snap.Events, "request.delivered") != 0 {
			t.Fatalf("run %v with %d request.delivered, want uncertain and none", snap.Threads[0].Run, count(snap.Events, "request.delivered"))
		}
		assertReplay(t, n.st)
	})
}
