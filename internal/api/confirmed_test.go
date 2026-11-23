package api

import "testing"

// Each way what the user chose for a thread can stand against what its
// provider runs, for its access, and the same rule for its model.
func TestWhatWasChosenStandsAgainstWhatTheProviderConfirmed(t *testing.T) {
	r1, r2 := NewID(), NewID()
	ran := func(run ID, a Access) *TurnResult { return &TurnResult{Run: run, Access: a, Outcome: OutcomeCompleted} }
	failed := func(run ID, a Access) *TurnResult { return &TurnResult{Run: run, Access: a, Outcome: OutcomeFailed} }
	granted := func(run ID, asked, a Access, why string) Confirmed {
		return Confirmed{Access: &ConfirmedAccess{Run: run, Asked: asked, Access: a, Why: why}}
	}
	for _, c := range []struct {
		name string
		t    Thread
		want Standing
	}{
		{"a thread that never ran", Thread{Access: AccessFull}, InEffect},
		{"confirmed as asked", Thread{Access: AccessAsk, LastTurn: ran(r1, AccessAsk), Confirmed: granted(r1, AccessAsk, AccessAsk, "")}, InEffect},
		{"changed since the last turn", Thread{Access: AccessFull, LastTurn: ran(r1, AccessAsk), Confirmed: granted(r1, AccessAsk, AccessAsk, "")}, NextTurn},
		{"switched back before it applied", Thread{Access: AccessAsk, LastTurn: ran(r1, AccessAsk), Confirmed: granted(r1, AccessAsk, AccessAsk, "")}, InEffect},
		{"a turn still running at the old one", Thread{Access: AccessFull, Turn: &Turn{Run: r2, Access: AccessAsk}, Confirmed: granted(r2, AccessAsk, AccessAsk, "")}, Held},
		{"the new turn not confirmed yet", Thread{Access: AccessFull, Turn: &Turn{Run: r2, Access: AccessFull}, LastTurn: ran(r1, AccessAsk), Confirmed: granted(r1, AccessAsk, AccessAsk, "")}, Applying},
		{"the new turn confirmed", Thread{Access: AccessFull, Turn: &Turn{Run: r2, Access: AccessFull}, LastTurn: ran(r1, AccessAsk), Confirmed: granted(r2, AccessFull, AccessFull, "")}, InEffect},
		{"an unchanged turn not confirmed yet", Thread{Access: AccessAsk, Turn: &Turn{Run: r2, Access: AccessAsk}, LastTurn: ran(r1, AccessAsk), Confirmed: granted(r1, AccessAsk, AccessAsk, "")}, InEffect},
		{"a first turn not confirmed yet", Thread{Access: AccessAsk, Turn: &Turn{Run: r1, Access: AccessAsk}}, InEffect},
		{"the provider fell back", Thread{Access: AccessAutoEdits, LastTurn: ran(r1, AccessAutoEdits), Confirmed: granted(r1, AccessAutoEdits, AccessAutoEdits, "auto mode unavailable for this model")}, Short},
		{"the provider runs a level below", Thread{Access: AccessFull, LastTurn: ran(r1, AccessFull), Confirmed: granted(r1, AccessFull, AccessAsk, "")}, Short},
		{"a fallback stands for the next turn until it confirms", Thread{Access: AccessAutoEdits, Turn: &Turn{Run: r2, Access: AccessAutoEdits}, LastTurn: ran(r1, AccessAutoEdits), Confirmed: granted(r1, AccessAutoEdits, AccessAutoEdits, "no auto")}, Short},
		{"the provider refused: its turn ended unconfirmed", Thread{Access: AccessFull, LastTurn: failed(r2, AccessFull), Confirmed: granted(r1, AccessAsk, AccessAsk, "")}, Unapplied},
		{"a thread from before confirmations", Thread{Access: AccessAsk, LastTurn: ran(r1, AccessAsk)}, InEffect},
		{"a thread from before confirmations, changed", Thread{Access: AccessFull, LastTurn: ran(r1, AccessAsk)}, NextTurn},
		{"a thread from before confirmations, its new turn", Thread{Access: AccessFull, Turn: &Turn{Run: r2, Access: AccessFull}, LastTurn: ran(r1, AccessAsk)}, Applying},
		{"a thread from before confirmations, its new turn failed", Thread{Access: AccessFull, LastTurn: failed(r2, AccessFull)}, Unapplied},
		{"the provider runs a level above", Thread{Access: AccessAsk, LastTurn: ran(r1, AccessAsk), Confirmed: granted(r1, AccessAsk, AccessFull, "")}, Short},
	} {
		if got := c.t.AccessStanding(); got != c.want {
			t.Errorf("%s: the access stands %d, want %d", c.name, got, c.want)
		}
	}

	sonnet, opus := ModelRef{Provider: "claude", Model: "Claude Sonnet 5.5", Effort: "high"}, ModelRef{Provider: "claude", Model: "Claude Opus 5.5", Effort: "high"}
	low := opus
	low.Effort = "low"
	for _, c := range []struct {
		name          string
		t             Thread
		model, effort Standing
	}{
		{"confirmed", Thread{Model: sonnet, LastTurn: &TurnResult{Run: r1, Model: sonnet}, Confirmed: Confirmed{Model: &ConfirmedModel{Run: r1, Asked: sonnet, Model: sonnet.Model, Effort: "high"}}}, InEffect, InEffect},
		{"a new model", Thread{Model: opus, LastTurn: &TurnResult{Run: r1, Model: sonnet}, Confirmed: Confirmed{Model: &ConfirmedModel{Run: r1, Asked: sonnet, Model: sonnet.Model}}}, NextTurn, InEffect},
		{"a new effort while a turn runs", Thread{Model: low, Turn: &Turn{Run: r2, Model: opus}, Confirmed: Confirmed{Model: &ConfirmedModel{Run: r2, Asked: opus, Model: opus.Model}}}, InEffect, Held},
		{"a substituted model", Thread{Model: opus, LastTurn: &TurnResult{Run: r1, Model: opus}, Confirmed: Confirmed{Model: &ConfirmedModel{Run: r1, Asked: opus, Model: "claude-sonnet-5-5", Why: "Claude Code runs claude-sonnet-5-5"}}}, Short, InEffect},
		{"an effort unsaid", Thread{Model: opus, LastTurn: &TurnResult{Run: r1, Model: opus}, Confirmed: Confirmed{Model: &ConfirmedModel{Run: r1, Asked: opus, Model: opus.Model}}}, InEffect, InEffect},
		{"another effort", Thread{Model: opus, LastTurn: &TurnResult{Run: r1, Model: opus}, Confirmed: Confirmed{Model: &ConfirmedModel{Run: r1, Asked: opus, Model: opus.Model, Effort: "medium"}}}, InEffect, Short},
	} {
		if got := c.t.ModelStanding(); got != c.model {
			t.Errorf("%s: the model stands %d, want %d", c.name, got, c.model)
		}
		if got := c.t.EffortStanding(); got != c.effort {
			t.Errorf("%s: the effort stands %d, want %d", c.name, got, c.effort)
		}
	}
}
