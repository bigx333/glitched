package claude_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider/claude"
)

// tapeList is the transcripts names, as the list one fake claude plays,
// a turn each.
func tapeList(names ...string) string {
	paths := make([]string, len(names))
	for i, n := range names {
		paths[i] = filepath.Join(must(os.Getwd()), "testdata", n+".jsonl")
	}
	return strings.Join(paths, string(os.PathListSeparator))
}

// keep has runs from now on replay turns, a transcript each, on one fake
// claude, and resumes on another. It returns the file their argv go to,
// and the processes they started in a role, claude or tool.
func keep(t *testing.T, turns, resumes []string) (argv string, started func(role string) []int) {
	t.Helper()
	t.Setenv(envFake, tapeList(turns...))
	if len(resumes) > 0 {
		t.Setenv(envResume, tapeList(resumes...))
	}
	argv = filepath.Join(t.TempDir(), "argv")
	t.Setenv(envArgv, argv)
	pids := filepath.Join(t.TempDir(), "pids")
	t.Setenv(envPids, pids)
	started = func(want string) []int {
		var found []int
		data, _ := os.ReadFile(pids)
		for _, l := range strings.Split(string(data), "\n") {
			var role string
			var pid int
			if n, _ := fmt.Sscan(l, &role, &pid); n == 2 && role == want {
				found = append(found, pid)
			}
		}
		return found
	}
	t.Cleanup(func() {
		for _, p := range slices.Concat(started("claude"), started("tool")) {
			kill(p)
		}
	})
	return argv, started
}

// launches is the session flag and session each claude started with.
func launches(t *testing.T, argv string) [][2]string {
	t.Helper()
	var got [][2]string
	for _, l := range strings.Split(strings.TrimSpace(string(must(os.ReadFile(argv)))), "\n") {
		var args []string
		json.Unmarshal([]byte(l), &args)
		got = append(got, [2]string{args[len(args)-2], args[len(args)-1]})
	}
	return got
}

// resumed is whether claude started twice, a new session and then that
// session resumed, with no start refused in between.
func resumed(l [][2]string) bool {
	return len(l) == 2 && l[0][0] == "--session-id" && l[1] == [2]string{"--resume", l[0][1]}
}

func waitGone(t *testing.T, pid int, what string) {
	t.Helper()
	if !exits(pid, 6*time.Second) {
		t.Fatalf("%s, claude %d, still runs", what, pid)
	}
}

const (
	firstAsk = "Say hi in three words."
	againAsk = "What did I ask you to do last time? Answer in under ten words."
)

// A thread's second turn goes to the claude its first turn started, with
// nothing started for it.
func TestASecondTurnRunsOnTheFirstTurnsClaude(t *testing.T) {
	argv, started := keep(t, []string{"hi", "again-resume"}, nil)
	h := host(t, nil)
	thread := newThread(t, h, api.AccessAsk)
	if first := turn(t, h, thread, firstAsk); first.ended.Outcome != api.OutcomeCompleted {
		t.Fatalf("first turn: %+v", first)
	}
	got := turn(t, h, thread, againAsk)
	if got.ended.Outcome != api.OutcomeCompleted || !strings.Contains(got.text, "say hi") || got.ended.Tokens == 0 {
		t.Errorf("second turn %+v, want Claude to answer from the first turn, with its tokens", got)
	}
	if l := launches(t, argv); len(l) != 1 || len(started("claude")) != 1 {
		t.Errorf("claude started %v, %d processes, want one for both turns", l, len(started("claude")))
	}
}

// What a claude starts with is fixed for its life. A turn that would start
// it otherwise, as at another access, gets a claude of its own, which
// resumes the thread's session at once.
func TestAChangedAccessStartsAClaudeThatResumesTheSession(t *testing.T) {
	argv, started := keep(t, []string{"hi"}, []string{"again-resume"})
	h := host(t, nil)
	thread := newThread(t, h, api.AccessAsk)
	turn(t, h, thread, firstAsk)
	submit(t, h, thread, api.ThreadConfigure{Access: new(api.AccessFull)})
	got := turn(t, h, thread, againAsk)
	if got.ended.Outcome != api.OutcomeCompleted || !strings.Contains(got.text, "say hi") {
		t.Errorf("second turn %+v, want Claude to answer from the session", got)
	}
	if l := launches(t, argv); !resumed(l) {
		t.Errorf("claude ran with sessions %v, want the session created, then resumed", l)
	}
	args := strings.Split(strings.TrimSpace(string(must(os.ReadFile(argv)))), "\n")
	if !strings.Contains(args[1], `"bypassPermissions"`) {
		t.Errorf("the second claude ran with %s, want Full access's permission mode", args[1])
	}
	waitGone(t, started("claude")[0], "the first turn's claude, at the old access")
}

// A parked claude that dies costs the thread nothing but its next turn's
// start, which resumes the session directly.
func TestAParkedClaudeThatDiesIsReplacedOnTheNextTurn(t *testing.T) {
	argv, started := keep(t, []string{"hi", "again-resume"}, []string{"again-resume"})
	h := host(t, nil)
	thread := newThread(t, h, api.AccessAsk)
	turn(t, h, thread, firstAsk)
	kill(started("claude")[0])
	waitGone(t, started("claude")[0], "the killed claude")
	got := turn(t, h, thread, againAsk)
	if got.ended.Outcome != api.OutcomeCompleted || !strings.Contains(got.text, "say hi") {
		t.Errorf("the turn after the crash %+v, want Claude to answer from the session", got)
	}
	if l := launches(t, argv); !resumed(l) {
		t.Errorf("claude ran with sessions %v, want the session created, then resumed", l)
	}
}

// A kept claude that dies mid-turn fails that turn, as a turn's own claude
// dying does, and the next turn resumes on a new one.
func TestAKeptClaudeThatDiesMidTurnFailsOnlyThatTurn(t *testing.T) {
	argv, _ := keep(t, []string{"hi", "exit-mid-tool"}, []string{"again-resume"})
	h := host(t, nil)
	thread := newThread(t, h, api.AccessFull)
	turn(t, h, thread, firstAsk)
	if got := turn(t, h, thread, "Run sleep 60."); got.ended.Outcome != api.OutcomeFailed || !strings.Contains(got.ended.Error, "Claude Code exited") {
		t.Errorf("the turn claude died in ended %+v, want failed with why", got.ended)
	}
	if got := turn(t, h, thread, againAsk); got.ended.Outcome != api.OutcomeCompleted {
		t.Errorf("the turn after %+v, want completed", got.ended)
	}
	if l := launches(t, argv); !resumed(l) {
		t.Errorf("claude ran with sessions %v, want the session created, then resumed", l)
	}
}

// A claude nobody comes back to is stopped.
func TestAnIdleClaudeIsStopped(t *testing.T) {
	claude.SetKeep(t, 100*time.Millisecond, 4)
	argv, started := keep(t, []string{"hi"}, []string{"again-resume"})
	h := host(t, nil)
	thread := newThread(t, h, api.AccessAsk)
	turn(t, h, thread, firstAsk)
	waitGone(t, started("claude")[0], "a claude parked past its idle time")
	if got := turn(t, h, thread, againAsk); got.ended.Outcome != api.OutcomeCompleted {
		t.Errorf("the turn after the reap %+v, want completed", got.ended)
	}
	if l := launches(t, argv); !resumed(l) {
		t.Errorf("claude ran with sessions %v, want the session created, then resumed", l)
	}
}

// Only so many claudes wait at once: parking one more stops the one that
// waited longest.
func TestParkedClaudesAreCapped(t *testing.T) {
	claude.SetKeep(t, time.Hour, 1)
	_, started := keep(t, []string{"hi"}, nil)
	h := host(t, nil)
	turn(t, h, newThread(t, h, api.AccessAsk), firstAsk)
	turn(t, h, newThread(t, h, api.AccessAsk), firstAsk)
	waitGone(t, started("claude")[0], "the claude parked first, past the cap")
	if p := started("claude")[1]; gone(p) {
		t.Errorf("claude %d, parked last, is gone; want it kept", p)
	}
}

// A deleted thread leaves nothing behind, its kept claude included.
func TestDeletingAThreadStopsItsKeptClaude(t *testing.T) {
	_, started := keep(t, []string{"hi"}, nil)
	h := host(t, nil)
	thread := newThread(t, h, api.AccessAsk)
	turn(t, h, thread, firstAsk)
	if gone(started("claude")[0]) {
		t.Fatal("the turn's claude was not kept")
	}
	submit(t, h, thread, api.ThreadDelete{})
	waitGone(t, started("claude")[0], "a deleted thread's kept claude")
}

// Approve for me chosen mid-thread holds from the thread's next turn: its
// claude starts in auto, and is switched to it before its first message.
func TestApproveForMeChosenMidThreadRunsAutoNextTurn(t *testing.T) {
	argv, _ := keep(t, []string{"hi"}, []string{"again-resume"})
	switched := filepath.Join(t.TempDir(), "modes")
	t.Setenv(envModes, switched)
	h := host(t, nil)
	thread := newThread(t, h, api.AccessAsk)
	turn(t, h, thread, firstAsk)
	submit(t, h, thread, api.ThreadConfigure{Access: new(api.AccessAutoEdits)})
	if got := turn(t, h, thread, againAsk); got.ended.Outcome != api.OutcomeCompleted {
		t.Fatalf("second turn %+v", got)
	}
	args := strings.Split(strings.TrimSpace(string(must(os.ReadFile(argv)))), "\n")
	if len(args) != 2 || !strings.Contains(args[0], `"default"`) || !strings.Contains(args[1], `"auto"`) {
		t.Errorf("claude ran with %q, want default, then auto", args)
	}
	if got := strings.TrimSpace(string(must(os.ReadFile(switched)))); got != "auto success" {
		t.Errorf("claude was switched to %q, want auto once, for the second turn", got)
	}
}
