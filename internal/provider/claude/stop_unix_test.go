//go:build unix

package claude_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"glitched/internal/api"
	"glitched/internal/provider"
)

// until reads ag's events up to the first that holds for want, which it
// returns.
func until(t *testing.T, ag provider.Agent, want func(provider.Event) bool) provider.Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, turnWait)
	defer cancel()
	for {
		e, err := ag.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if want(e) {
			return e
		}
	}
}

func turnEnded(e provider.Event) bool {
	_, ok := e.(provider.TurnEnded)
	return ok
}

// Claude Code ends a stopped turn at the interrupt's result, and 18 ms
// later takes a steer still queued as a turn of its own, writing it to the
// session the next turn resumes. Nothing of that turn may run: the user
// stopped it.
func TestNothingRunsAfterAStoppedTurnEnds(t *testing.T) {
	t.Run("interrupt_cancel_queued_v1", func(t *testing.T) { nothingRunsAfterAStop(t, "steer-stop") })
	// A Claude Code without the capability ignores cancel_queued.
	t.Run("without it", func(t *testing.T) { nothingRunsAfterAStop(t, "steer-stop-uncancellable") })
	t.Run("without it, with msg_lifecycle_v1", func(t *testing.T) { nothingRunsAfterAStop(t, "steer-stop-uncancellable-lifecycle") })
}

func nothingRunsAfterAStop(t *testing.T, tape string) {
	replay(t, tape, "")
	ws := t.TempDir()
	ag := openIn(t, ws)
	if err := ag.Do(ctx, provider.Start{Text: "Run ./busy.sh with the Bash tool."}); err != nil {
		t.Fatal(err)
	}
	until(t, ag, func(e provider.Event) bool { w, ok := e.(provider.Work); return ok && w.Status == api.WorkStarted })
	if err := ag.Do(ctx, provider.Steer{Text: "After that, run touch steer-ran."}); err != nil {
		t.Fatal(err)
	}
	if err := ag.Do(ctx, provider.Interrupt{}); err != nil {
		t.Fatal(err)
	}
	if e := until(t, ag, turnEnded).(provider.TurnEnded); e.Outcome != api.OutcomeInterrupted {
		t.Errorf("the turn ended %+v, want interrupted", e)
	}
	if err := ag.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ws, "steer-taken")); err == nil {
		t.Error("claude took the steer after the stopped turn ended")
	}
}

// A stop that reaches Claude Code after it finished the turn leaves the
// turn as Claude Code ended it.
func TestAStopAfterTheTurnCompletedLeavesItCompleted(t *testing.T) {
	replay(t, "stop-after-result", "")
	ag := open(t)
	defer ag.Close(ctx)
	if err := ag.Do(ctx, provider.Start{Text: "Reply done."}); err != nil {
		t.Fatal(err)
	}
	if err := ag.Do(ctx, provider.Interrupt{}); err != nil {
		t.Fatal(err)
	}
	if e := until(t, ag, turnEnded).(provider.TurnEnded); e.Outcome != api.OutcomeCompleted {
		t.Errorf("the turn ended %+v, want completed: Claude Code finished it before it read the stop", e)
	}
}
