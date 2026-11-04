//go:build claudelive && linux

package claude_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider/claude"
	"glitched/internal/provider/claude/transcript"
)

// script writes an executable shell script to dir, since Claude Code's
// Bash tool refuses a long sleep it can see.
func script(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestLiveStopSteerAndCrash runs Stop, a steer and a killed engine against
// the signed-in claude on PATH, with haiku in scratch directories under
// /tmp. With -record, the stop and steer transcripts go to testdata.
func TestLiveStopSteerAndCrash(t *testing.T) {
	real, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	if v, _, _ := strings.Cut(strings.TrimSpace(string(must(claude.Command(ctx, real, "--version").Output()))), " "); v != shippedCLI {
		t.Fatalf("claude on PATH is %s, want %s", v, shippedCLI)
	}
	tapes := t.TempDir()
	if *record {
		tapes = must(filepath.Abs("testdata"))
	}
	scratch := func(t *testing.T) string {
		ws := must(os.MkdirTemp("/tmp", "glitched-live-"))
		t.Cleanup(func() { os.RemoveAll(ws) })
		return ws
	}

	t.Run("stop", func(t *testing.T) {
		t.Setenv(transcript.EnvReal, real)
		t.Setenv(transcript.EnvOut, filepath.Join(tapes, "interrupt"))
		ws := scratch(t)
		script(t, ws, "busy.sh", "sleep 30\necho finished")
		h := host(t, nil)
		thread := threadIn(t, h, ws)
		got, run := during(t, h, thread, "Run ./busy.sh with the Bash tool. Then reply done.", toolRunning, func() {
			time.Sleep(time.Second)
			submit(t, h, thread, api.TurnInterrupt{})
		})
		checkStopped(t, got, run)
	})

	t.Run("stop at a prompt", func(t *testing.T) {
		ws := scratch(t)
		h := host(t, &claude.Adapter{})
		thread := api.NewID()
		submit(t, h, thread, api.ThreadCreate{Workspace: ws, Title: "ask", Access: api.AccessAsk, Model: api.ModelRef{Provider: "claude", Model: "haiku"}})
		got := playOn(t, h, thread, "Use the Write tool to create x.txt containing x. Then reply done.", func(requestLog) bool { return true })
		if got.ended.Outcome != api.OutcomeInterrupted || len(got.requests) != 1 || got.requests[0].state != api.RequestInvalidated {
			t.Errorf("the turn ended %+v with requests %+v, want it interrupted and its prompt void", got.ended, got.requests)
		}
		if _, err := os.Stat(filepath.Join(ws, "x.txt")); err == nil {
			t.Error("x.txt exists, though the turn stopped at its prompt")
		}
	})

	t.Run("steer", func(t *testing.T) {
		t.Setenv(transcript.EnvReal, real)
		t.Setenv(transcript.EnvOut, filepath.Join(tapes, "steer"))
		ws := scratch(t)
		script(t, ws, "slow.sh", "sleep 8\necho one")
		h := host(t, nil)
		thread := threadIn(t, h, ws)
		got, _ := during(t, h, thread, "Run ./slow.sh with the Bash tool. Then tell me what it printed.", toolRunning, func() {
			submit(t, h, thread, api.MessageDispatch{Text: "Also, after that, say the word BANANA.", Mode: api.SteerActive})
		})
		checkSteered(t, got)
	})

	t.Run("crash", func(t *testing.T) {
		ws := scratch(t)
		script(t, ws, "busy.sh", "sleep 60\necho finished")
		path := filepath.Join(t.TempDir(), "host.db")
		thread, run := queue(t, path, ws, "Remember the word PAPAYA. Then run ./busy.sh with the Bash tool.")
		killedEngine(t, path, func() bool { return running(run, "busy.sh") })
		if !noneLeft(run, 6*time.Second) {
			t.Fatalf("processes %v of the run outlived the engine", marked(run))
		}
		h := hostAt(t, path, &claude.Adapter{})
		if got := runOf(t, h, thread, uncertain); got != api.RunUncertain {
			t.Fatalf("the run is %v after the restart, want uncertain", got)
		}
		submit(t, h, thread, api.TurnInterrupt{})
		got := turn(t, h, thread, "Which word did I ask you to remember? Reply with only the word.")
		if got.ended.Outcome != api.OutcomeCompleted || !strings.Contains(strings.ToUpper(got.text), "PAPAYA") {
			t.Errorf("the next turn %+v, want it to resume the crashed run's session", got)
		}
	})
}

// running is whether one of run's processes runs name.
func running(run api.ID, name string) bool {
	for _, pid := range marked(run) {
		cmd, _ := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
		if strings.Contains(string(cmd), name) {
			return true
		}
	}
	return false
}
