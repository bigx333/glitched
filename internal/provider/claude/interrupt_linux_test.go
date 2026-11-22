package claude_test

import (
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider/claude"
	"glitched/internal/runtime"
)

func threadIn(t *testing.T, h *runtime.Host, ws string) api.ID {
	t.Helper()
	thread := api.NewID()
	submit(t, h, thread, api.ThreadCreate{Workspace: ws, Title: "stop", Access: api.AccessFull,
		Model: api.ModelRef{Provider: "claude", Model: "haiku"}})
	return thread
}

// during sends text, calls do once when holds for the turn so far, and
// returns what the thread's log holds of the run once the turn is over.
func during(t *testing.T, h *runtime.Host, thread api.ID, text string, when func(promptLog) bool, do func()) (promptLog, api.ID) {
	t.Helper()
	run := api.NewID()
	if _, err := h.Submit(ctx, api.Command{ID: run, Target: thread, Body: api.MessageDispatch{Text: text, Mode: api.StartNow}}); err != nil {
		t.Fatal(err)
	}
	done := false
	for deadline := time.Now().Add(turnWait); ; time.Sleep(5 * time.Millisecond) {
		l, over := readLog(t, h, thread, run)
		if over {
			if !done {
				t.Fatalf("the turn ended before it got to do its part: %+v", l)
			}
			return l, run
		}
		if !done && when(l) {
			done = true
			do()
		}
		if time.Now().After(deadline) {
			t.Fatalf("the turn did not end: %+v", l)
		}
	}
}

// toolRunning is whether a Bash step has started and not finished.
func toolRunning(l promptLog) bool {
	open := map[string]bool{}
	for _, w := range l.work {
		if strings.HasPrefix(w.Title, "Bash") {
			open[w.Item] = w.Status == api.WorkStarted
		}
	}
	for _, o := range open {
		if o {
			return true
		}
	}
	return false
}

// checkStopped is what Stop leaves of a run busy in a tool call: an
// interrupted turn, not an uncertain one, no step still going, and no
// process left.
func checkStopped(t *testing.T, got promptLog, thread api.ID) {
	t.Helper()
	if got.ended.Outcome != api.OutcomeInterrupted || got.ended.Error != "" {
		t.Errorf("the turn ended %+v, want interrupted and no error", got.ended)
	}
	if toolRunning(got) {
		t.Errorf("work items %+v, want the stopped Bash step ended", got.work)
	}
	if !noneLeft(thread, 6*time.Second) {
		t.Errorf("processes %v of the stopped run still run", marked(thread))
	}
}

// checkSteered is what a steer leaves: one turn that took both messages.
func checkSteered(t *testing.T, got promptLog) {
	t.Helper()
	if got.ended.Outcome != api.OutcomeCompleted || !strings.Contains(got.text, "BANANA") {
		t.Errorf("the turn ended %+v saying %q, want it completed and to take the steer", got.ended, got.text)
	}
}

func TestStopEndsTheTurnInterrupted(t *testing.T) {
	replay(t, "interrupt", "")
	h := host(t, nil)
	thread := threadIn(t, h, t.TempDir())
	got, _ := during(t, h, thread, "Run ./busy.sh with the Bash tool. Then reply done.", toolRunning, func() {
		submit(t, h, thread, api.TurnInterrupt{})
	})
	checkStopped(t, got, thread)
}

func TestASteerJoinsTheLiveTurn(t *testing.T) {
	replay(t, "steer", "")
	h := host(t, nil)
	thread := threadIn(t, h, t.TempDir())
	got, _ := during(t, h, thread, "Run ./slow.sh with the Bash tool. Then tell me what it printed.", toolRunning, func() {
		submit(t, h, thread, api.MessageDispatch{Text: "Also, after that, say the word BANANA.", Mode: api.SteerActive})
	})
	checkSteered(t, got)
	if want := streamed(t, "steer"); got.text != want {
		t.Errorf("assistant text %q, want all Claude streamed: %q", got.text, want)
	}
}

// A steer that comes too late for the turn's first result is taken after
// it, and the run ends only with the result that finishes it.
func TestARunEndsOnceItsSteerIsDone(t *testing.T) {
	replay(t, "steer-late", "")
	h := host(t, nil)
	thread := threadIn(t, h, t.TempDir())
	got, _ := during(t, h, thread, "Run ./slow.sh with the Bash tool. Then tell me what it printed.", toolRunning, func() {
		submit(t, h, thread, api.MessageDispatch{Text: "Also, after that, say the word BANANA.", Mode: api.SteerActive})
	})
	checkSteered(t, got)
	if got.text != "It printed one.\n\nBANANA" || got.ended.Tokens != 18 {
		t.Errorf("the turn said %q and used %d tokens, want both results' text and tokens", got.text, got.ended.Tokens)
	}
}

// Claude Code that does not end the turn on an interrupt is stopped, and
// the turn still ends interrupted.
func TestAnIgnoredStopStillEndsTheTurn(t *testing.T) {
	replay(t, "interrupt-deaf", "")
	claude.SetInterruptGrace(t, 100*time.Millisecond)
	h := host(t, nil)
	thread := threadIn(t, h, t.TempDir())
	got, _ := during(t, h, thread, "Run ./busy.sh with the Bash tool. Then reply done.", toolRunning, func() {
		submit(t, h, thread, api.TurnInterrupt{})
	})
	checkStopped(t, got, thread)
}
