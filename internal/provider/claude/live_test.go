//go:build claudelive

package claude_test

import (
	"os"
	"path/filepath"
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

// TestLiveApproveForMeRunsAuto spends a haiku turn: at Approve for me
// claude runs its auto mode, which writes a file in the workspace without
// asking, and Describe says nothing of auto being off.
func TestLiveApproveForMeRunsAuto(t *testing.T) {
	ad := &claude.Adapter{}
	h := host(t, ad)
	thread := newThread(t, h, api.AccessAutoEdits)
	got := turn(t, h, thread, "Create the file hello.txt in the current folder containing exactly: hi")
	b, err := os.ReadFile(filepath.Join(got.workspace, "hello.txt"))
	t.Logf("ended %+v, %d requests, hello.txt %q %v", got.ended, len(got.requests), b, err)
	if got.ended.Outcome != api.OutcomeCompleted || len(got.requests) != 0 || strings.TrimSpace(string(b)) != "hi" {
		t.Errorf("at Approve for me the turn %+v asked %d times, want hello.txt written unasked", got.ended, len(got.requests))
	}
	info, err := ad.Describe(ctx)
	if err != nil || info.NoAuto != "" {
		t.Errorf("Describe = %q, %v, want auto on offer", info.NoAuto, err)
	}
}
