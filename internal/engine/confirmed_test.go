package engine_test

import (
	"testing"

	"glitched/internal/api"
	"glitched/internal/engine"
)

// A change made while a turn runs shows held until the turn ends, then
// waits for the next turn, which applies it until its provider confirms
// it. A confirmation from a run no longer the thread's is dropped, and a
// fallback shows what the provider runs instead.
func TestAChangeIsPendingUntilTheProviderConfirmsIt(t *testing.T) {
	opus := api.ModelRef{Model: "Claude Opus 5.5", Effort: "high"}
	sonnet := api.ModelRef{Model: "Claude Sonnet 5.5", Effort: "low"}
	f := newThread(t)
	f.must(whole(opus, api.AccessAsk))
	first := f.must(dispatch("go", api.StartNow))
	f.observe(engine.Started{Run: first})
	f.observe(engine.Granted{Run: first, Access: api.AccessAsk, Mode: "default"})
	f.observe(engine.Ran{Run: first, Model: opus.Model, Effort: opus.Effort})
	stands := func(when string, access, model, effort api.Standing) {
		t.Helper()
		v := engine.View(f.th)
		if a, m, e := v.AccessStanding(), v.ModelStanding(), v.EffortStanding(); a != access || m != model || e != effort {
			t.Errorf("%s: access, model and effort stand %d %d %d, want %d %d %d", when, a, m, e, access, model, effort)
		}
	}
	stands("confirmed", api.InEffect, api.InEffect, api.InEffect)

	f.must(whole(sonnet, api.AccessAutoEdits))
	stands("changed mid-turn", api.Held, api.Held, api.Held)
	f.observe(engine.Ended{Run: first, Outcome: api.OutcomeCompleted})
	stands("after the turn", api.NextTurn, api.NextTurn, api.NextTurn)
	f.observe(engine.Granted{Run: first, Access: api.AccessFull})
	if g := f.th.Confirmed.Access; g == nil || g.Run != first || g.Access != api.AccessAsk {
		t.Errorf("a confirmation of an ended run changed the thread's: %+v", g)
	}

	second := f.must(dispatch("again", api.StartNow))
	f.observe(engine.Started{Run: second})
	stands("the next turn started", api.Applying, api.Applying, api.Applying)
	f.observe(engine.Granted{Run: second, Access: api.AccessAutoEdits, Mode: "acceptEdits", Why: "auto mode unavailable for this model"})
	f.observe(engine.Ran{Run: second, Model: sonnet.Model, Effort: sonnet.Effort})
	stands("confirmed with a fallback", api.Short, api.InEffect, api.InEffect)
	want := api.ConfirmedAccess{Run: second, Asked: api.AccessAutoEdits, Access: api.AccessAutoEdits, Mode: "acceptEdits", Why: "auto mode unavailable for this model"}
	if g := engine.View(f.th).Confirmed.Access; g == nil || *g != want {
		t.Errorf("the view's confirmed access is %+v, want %+v", g, want)
	}

	f.must(whole(sonnet, api.AccessAsk))
	stands("changed again mid-turn", api.Held, api.InEffect, api.InEffect)
	f.must(whole(sonnet, api.AccessAutoEdits))
	stands("switched back", api.Short, api.InEffect, api.InEffect)
}
