package runtime_test

import (
	"slices"
	"testing"

	"glitched/internal/api"
	"glitched/internal/provider/fake"
)

// admitted is the runs the log admitted, in order.
func admitted(t *testing.T, evs []api.Event) []api.ID {
	t.Helper()
	var runs []api.ID
	for _, e := range evs {
		if e.Type == "turn.admitted" {
			b, err := e.Decode()
			if err != nil {
				t.Fatal(err)
			}
			runs = append(runs, b.(api.TurnAdmitted).Run)
		}
	}
	return runs
}

// The queue's edits, moves and cancels are the stored thread's across a
// crash, which the log replays to; a steer hands a message to the live
// run, and one on the thread a stop left idle starts it, with what the
// stop held after it in its new order.
func TestTheQueueIsManagedAcrossACrash(t *testing.T) {
	path, ad := dbPath(t), fake.New(fake.Turn(true))
	n := start(t, path, ad)
	thread := newThread(t, n.h)
	first := submit(t, n.h, thread, dispatch("first", api.StartNow)).Command
	until(t, n.h, thread, "the approval", waiting)
	a := submit(t, n.h, thread, dispatch("a", api.AfterActive)).Command
	b := submit(t, n.h, thread, dispatch("b", api.AfterActive)).Command
	c := submit(t, n.h, thread, dispatch("c", api.AfterActive)).Command
	d := submit(t, n.h, thread, dispatch("d", api.AfterActive)).Command
	steered := submit(t, n.h, thread, dispatch("steered", api.AfterActive)).Command
	submit(t, n.h, thread, api.QueueEdit{Message: a, Text: "a, shorter"})
	submit(t, n.h, thread, api.QueueMove{Message: d, Before: b})
	submit(t, n.h, thread, api.QueueCancel{Message: c})
	submit(t, n.h, thread, api.QueueSteer{Message: steered})
	until(t, n.h, thread, "the steer delivered", func(th api.Thread) bool {
		return th.Queued == 3 && count(events(t, n.h, thread), "message.dispatched") == 1
	})
	assertReplay(t, n.st)
	n.kill()

	fresh := fake.New(fake.Turn(false))
	n = start(t, path, fresh)
	until(t, n.h, thread, "uncertain", func(th api.Thread) bool { return th.Run == api.RunUncertain })
	assertReplay(t, n.st)
	submit(t, n.h, thread, api.TurnInterrupt{})
	until(t, n.h, thread, "the queue held", func(th api.Thread) bool { return idle(th) && th.Queued == 3 })
	submit(t, n.h, thread, api.QueueSteer{Message: b})
	until(t, n.h, thread, "the queue drained", func(th api.Thread) bool { return idle(th) && th.Queued == 0 && fresh.Starts() == 3 })

	if got, want := admitted(t, events(t, n.h, thread)), []api.ID{first, b, a, d}; !slices.Equal(got, want) {
		t.Fatalf("the turns ran %v, want first, then the steered b, then a and d in their order: %v", got, want)
	}
	evs := events(t, n.h, thread)
	if !slices.ContainsFunc(evs, func(e api.Event) bool {
		body, _ := e.Decode()
		s, ok := body.(api.MessageEdited)
		return ok && s.Message == a && s.Text == "a, shorter"
	}) || count(evs, "message.moved") != 3 {
		t.Fatalf("the log has %d moves and no edit of a; want the edit and three moves", count(evs, "message.moved"))
	}
	assertReplay(t, n.st)
}
