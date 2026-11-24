package engine_test

import (
	"errors"
	"slices"
	"testing"

	"glitched/internal/api"
	"glitched/internal/engine"
)

// turns ends run and each turn the end starts, and says what each started
// with, in order: the queue as it plays out.
func (f *thread) turns(run api.ID) []string {
	f.t.Helper()
	var texts []string
	for {
		s, ok := started(f.see(engine.Ended{Run: run, Outcome: api.OutcomeCompleted}))
		if !ok {
			return texts
		}
		texts, run = append(texts, s.Text), s.Run
	}
}

// rejects decides body and wants it refused with nothing recorded.
func (f *thread) rejects(body api.Intent, why string) {
	f.t.Helper()
	evs, effects, err := engine.Decide(f.th, api.Command{ID: api.NewID(), Target: f.th.ID, Body: body}, now)
	if rej := (*api.RejectedError)(nil); !errors.As(err, &rej) || len(evs) != 0 || len(effects) != 0 {
		f.t.Fatalf("%s %s: %v, %d events, %d effects; want rejected with nothing recorded", api.IntentType(body), why, err, len(evs), len(effects))
	}
}

// An edit changes what the agent is given when the message's turn comes,
// held or ready; one that changes nothing records nothing, and one with no
// words is refused.
func TestAnEditChangesWhatTheTurnSends(t *testing.T) {
	f, run := running(t)
	ready := f.must(dispatch("draft", api.AfterActive))
	deferred := f.must(dispatch("later", api.Defer))
	f.must(api.QueueEdit{Message: ready, Text: "final"})
	f.must(api.QueueEdit{Message: deferred, Text: "much later"})
	if m, _ := f.th.Message(deferred); m.Text != "much later" || m.State != engine.QueueHeld {
		t.Fatalf("the edited Defer is %+v, want it held with its new words", m)
	}
	f.rejects(api.QueueEdit{Message: ready, Text: " \n"}, "with no words")
	if evs, _, err := engine.Decide(f.th, api.Command{ID: api.NewID(), Target: f.th.ID, Body: api.QueueEdit{Message: ready, Text: "final"}}, now); err != nil || len(evs) != 0 {
		t.Fatalf("an edit to the words it has: %v, %v; want nothing recorded", evs, err)
	}
	if got := f.turns(run); !slices.Equal(got, []string{"final"}) {
		t.Fatalf("the turns went %q, want the edited words", got)
	}
}

// A cancel takes a message out of the queue, so its turn never comes.
func TestACancelTakesAMessageOutOfTheQueue(t *testing.T) {
	f, run := running(t)
	gone := f.must(dispatch("gone", api.AfterActive))
	f.must(dispatch("kept", api.AfterActive))
	f.must(api.QueueCancel{Message: gone})
	if f.state(gone) != engine.QueueCancelled || engine.View(f.th).Queued != 1 {
		t.Fatalf("after the cancel the message is %v and %d queued, want cancelled and 1", f.state(gone), engine.View(f.th).Queued)
	}
	if got := f.turns(run); !slices.Equal(got, []string{"kept"}) {
		t.Fatalf("the turns went %q, want only the one kept", got)
	}
}

// A move puts a message just before another, or last, and the turns take
// that order: between two with no position free, the later ones make room.
func TestAMoveReordersTheQueue(t *testing.T) {
	for name, tc := range map[string]struct {
		move   func(a, b, c api.ID) api.QueueMove
		want   []string
		events int
	}{
		"to the front":       {func(a, b, c api.ID) api.QueueMove { return api.QueueMove{Message: c, Before: a} }, []string{"c", "a", "b"}, 1},
		"between neighbours": {func(a, b, c api.ID) api.QueueMove { return api.QueueMove{Message: c, Before: b} }, []string{"a", "c", "b"}, 2},
		"to the end":         {func(a, b, c api.ID) api.QueueMove { return api.QueueMove{Message: a} }, []string{"b", "c", "a"}, 1},
		"where it is":        {func(a, b, c api.ID) api.QueueMove { return api.QueueMove{Message: b, Before: c} }, []string{"a", "b", "c"}, 0},
		"last, where it is":  {func(a, b, c api.ID) api.QueueMove { return api.QueueMove{Message: c} }, []string{"a", "b", "c"}, 0},
	} {
		t.Run(name, func(t *testing.T) {
			f, run := running(t)
			a := f.must(dispatch("a", api.AfterActive))
			b := f.must(dispatch("b", api.AfterActive))
			c := f.must(dispatch("c", api.AfterActive))
			evs, _, err := engine.Decide(f.th, api.Command{ID: api.NewID(), Target: f.th.ID, Body: tc.move(a, b, c)}, now)
			if err != nil || len(evs) != tc.events {
				t.Fatalf("the move: %d events, %v; want %d", len(evs), err, tc.events)
			}
			f.fold(evs)
			if got := f.turns(run); !slices.Equal(got, tc.want) {
				t.Fatalf("the turns went %q, want %q", got, tc.want)
			}
		})
	}

	f, _ := running(t)
	a := f.must(dispatch("a", api.AfterActive))
	f.rejects(api.QueueMove{Message: a, Before: a}, "before itself")
	f.rejects(api.QueueMove{Message: a, Before: f.th.Live().ID}, "before the live run's message")
	f.rejects(api.QueueMove{Message: a, Before: api.NewID()}, "before no message")
}

// Moves between messages that earlier moves left no room between still
// order the queue, however often they're made.
func TestRepeatedMovesKeepTheOrder(t *testing.T) {
	f, run := running(t)
	ids := map[string]api.ID{}
	for _, s := range []string{"a", "b", "c", "d"} {
		ids[s] = f.must(dispatch(s, api.AfterActive))
	}
	for range 3 {
		f.must(api.QueueMove{Message: ids["d"], Before: ids["b"]})
		f.must(api.QueueMove{Message: ids["c"], Before: ids["d"]})
		f.must(api.QueueMove{Message: ids["b"], Before: ids["c"]})
	}
	if got, want := f.turns(run), []string{"a", "b", "c", "d"}; !slices.Equal(got, want) {
		t.Fatalf("the turns went %q, want %q", got, want)
	}
}

// Steering a queued message hands it to the live run now, as a steer
// sent then would, and releases what a Defer held; on an idle thread it
// starts the message's turn. A run that can't take it refuses.
func TestASteerSendsAQueuedMessageNow(t *testing.T) {
	f, run := running(t)
	later := f.must(dispatch("later", api.AfterActive))
	deferred := f.must(dispatch("deferred", api.Defer))
	_, effects, err := f.submit(api.QueueSteer{Message: later})
	if len(effects) != 1 || err != nil {
		t.Fatalf("steer: %v, %v; want one effect", effects, err)
	}
	if s, ok := effects[0].(engine.SteerTurn); !ok || s.Run != run || s.Message != later || s.Text != "later" {
		t.Fatalf("steer: %+v; want the message steered into the live run", effects[0])
	}
	if f.state(later) != engine.QueueClaimed || f.state(deferred) != engine.QueueReady {
		t.Fatalf("after the steer the message is %v and the Defer %v; want claimed and released", f.state(later), f.state(deferred))
	}

	idle, run := running(t)
	held := idle.must(dispatch("held", api.AfterActive))
	idle.must(api.TurnInterrupt{})
	idle.see(engine.Ended{Run: run, Outcome: api.OutcomeInterrupted})
	_, effects, err = idle.submit(api.QueueSteer{Message: held})
	if s, ok := started(effects); err != nil || !ok || s.Run != held || s.Text != "held" {
		t.Fatalf("steer on an idle thread: %+v, %v; want the held message's turn", effects, err)
	}

	stopping, _ := running(t)
	queued := stopping.must(dispatch("queued", api.AfterActive))
	stopping.must(api.TurnInterrupt{})
	stopping.rejects(api.QueueSteer{Message: queued}, "while the run stops")

	uncertain, run := running(t)
	queued = uncertain.must(dispatch("queued", api.AfterActive))
	uncertain.observe(engine.Reconciled{Run: run})
	uncertain.rejects(api.QueueSteer{Message: queued}, "while the run is uncertain")
}

// The queue's intents act only on a message of the user's that still
// waits its turn: never on one claimed, sent or cancelled, nor on a wake
// the engine queued with a child's report.
func TestTheQueuesIntentsRefuseAMessageNoLongerWaiting(t *testing.T) {
	f, run := running(t)
	claimed := f.must(dispatch("hurry", api.SteerActive))
	sent := f.must(dispatch("sent", api.SteerActive))
	f.see(engine.Steered{Message: sent})
	cancelled := f.must(dispatch("cancelled", api.AfterActive))
	f.must(api.QueueCancel{Message: cancelled})
	f.see(report(api.ReportDone, "child", "result"))
	wake := f.th.Queue[len(f.th.Queue)-1].ID
	waiting := f.must(dispatch("waiting", api.AfterActive))
	for name, id := range map[string]api.ID{"its run's own": run, "claimed": claimed, "sent": sent, "cancelled": cancelled, "a wake": wake, "unknown": api.NewID()} {
		for _, body := range []api.Intent{
			api.QueueEdit{Message: id, Text: "new"},
			api.QueueCancel{Message: id},
			api.QueueMove{Message: id},
			api.QueueMove{Message: id, Before: waiting},
			api.QueueSteer{Message: id},
		} {
			f.rejects(body, name)
		}
	}
}
