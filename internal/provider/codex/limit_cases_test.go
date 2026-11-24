package codex

import (
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
)

// A Stop while the turn's end waits on Codex's windows ends it as stopped,
// with no usage limit to hold the thread.
func TestAStopWhileCodexReadsItsWindowsEndsTheTurnStopped(t *testing.T) {
	a, log := fake(t, "limit-stop")
	g := open(t, a, spec(t))
	do(t, g, provider.Start{Text: "hi"})
	deadline := time.Now().Add(3 * time.Second)
	for len(sent(t, log, "account/rateLimits/read")) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("stub did not receive rate-limit read")
		}
		time.Sleep(10 * time.Millisecond)
	}
	do(t, g, provider.Interrupt{})
	end := ended(turn(t, g))
	closeRun(t, g)
	if end.Outcome != api.OutcomeInterrupted || end.Limit != nil {
		if end.Limit == nil {
			t.Fatalf("Stop during rate-limit read ended %+v, want interrupted with no limit", end)
		}
		t.Fatalf("Stop during rate-limit read ended with %q and reset %s, want interrupted with no limit", end.Error, end.Limit.Resets)
	}
}

// The reset is the account's: not a model's own allowance's, and one Codex
// says while the read of its windows fails still counts.
func TestALimitedTurnTakesTheAccountsReset(t *testing.T) {
	for _, scenario := range []string{"limit-model-bucket", "limit-update-during-read"} {
		t.Run(scenario, func(t *testing.T) {
			a, _ := fake(t, scenario)
			g := open(t, a, spec(t))
			do(t, g, provider.Start{Text: "hi"})
			end := ended(turn(t, g))
			closeRun(t, g)
			want := time.Unix(1795521600, 0).UTC()
			if end.Limit == nil {
				t.Fatalf("no limit; want supplied account reset %s", want)
			}
			if !end.Limit.Resets.Equal(want) {
				t.Fatalf("usage limit reset %s; want supplied account reset %s", end.Limit.Resets, want)
			}
		})
	}
}
