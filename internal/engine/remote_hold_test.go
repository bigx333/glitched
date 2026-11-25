package engine_test

import (
	"testing"

	"glitched/internal/api"
	"glitched/internal/engine"
)

// Follow keeps what a stop on the child's machine holds as that machine's
// own fold does: a message of its person's held until released or sent,
// and a wake held behind a stopped run until a run takes it.
func TestFollowKeepsWhatAStopThereHolds(t *testing.T) {
	run, next, msg, wake := api.NewID(), api.NewID(), api.NewID(), api.NewID()
	r := engine.Remote{State: engine.RemoteBoundState}
	steps := []struct {
		b       api.EventBody
		holding bool
	}{
		{api.TurnAdmitted{Run: run}, false},
		{api.MessageQueued{Message: msg, Text: "mine", Mode: api.AfterActive}, false},
		{api.StopAsked{Run: run}, false},
		{api.TurnSignalled{Run: run}, false},
		{api.MessageHeld{Message: msg}, true},
		{api.TurnEnded{Run: run, Outcome: api.OutcomeInterrupted}, true},
		{api.MessageReleased{Message: msg}, false},
		{api.MessageQueued{Message: wake, Text: "report", Mode: api.AfterActive, Held: true, Report: api.NewID()}, true},
		{api.TurnAdmitted{Run: next}, false},
		{api.MessageClaimed{Message: wake, Run: next}, false},
	}
	for i, s := range steps {
		var err error
		if r, _, _, err = engine.Follow(r, []api.Event{on(t, uint64(i+1), s.b)}, false); err != nil {
			t.Fatal(err)
		}
		if r.Hold.Holding() != s.holding {
			t.Fatalf("after %s %d: holding %v (%+v); want %v", api.EventType(s.b), i, r.Hold.Holding(), r.Hold, s.holding)
		}
	}
}
