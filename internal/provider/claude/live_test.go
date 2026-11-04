//go:build claudelive

package claude_test

import (
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider/claude"
)

func init() { turnWait = 2 * time.Minute }

// TestLiveTurnsShareTheThreadsSession runs two haiku turns on the signed-in
// claude on PATH. It is the check before a CLI upgrade, and before record.
func TestLiveTurnsShareTheThreadsSession(t *testing.T) {
	h := host(t, &claude.Adapter{})
	thread := newThread(t, h, api.AccessAsk)
	first := turn(t, h, thread, "Remember the word PAPAYA. Reply with only: ok")
	if first.ended.Outcome != api.OutcomeCompleted || first.text == "" || first.ended.Tokens == 0 {
		t.Fatalf("first turn %+v, want streamed text and a completed turn", first)
	}
	second := turn(t, h, thread, "Which word did I ask you to remember? Reply with only the word.")
	if second.ended.Outcome != api.OutcomeCompleted || !strings.Contains(strings.ToUpper(second.text), "PAPAYA") {
		t.Errorf("second turn %+v, want it to resume the first turn's session", second)
	}
}
