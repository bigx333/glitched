package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"glitched/internal/daemon"
)

// tripwire puts an agent named name on PATH and in ~/.local/bin, where
// its adapter looks for one, that leaves a mark if anything runs it.
func tripwire(t *testing.T, name string) (ran string) {
	t.Helper()
	home, bin := t.TempDir(), t.TempDir()
	ran = filepath.Join(t.TempDir(), "ran")
	script := "#!/bin/sh\necho \"$@\" >> " + ran + "\nexit 1\n"
	for _, dir := range []string{bin, filepath.Join(home, ".local", "bin")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return ran
}

// sendNew sends text as a new thread's first message, in a scratch
// workspace on this machine, with the model the picker shows.
func sendNew(h *harness, text string) {
	h.a.startIn(draft{h.local(), h.t.TempDir()})
	h.tt.Frame()
	h.a.draft = text
	h.a.send(h.a.current())
}

// The app a test starts plays Claude Code with the fake, even with a
// signed-in claude on PATH: only the app's main wires in the real one.
func TestTheGUIsTestsNeverRunClaude(t *testing.T) {
	ran := tripwire(t, "claude")
	h := start(t, tempDir(t))
	sendNew(h, "Say hi.")
	h.until("the fake's ask, or a claude run", func() bool {
		_, err := os.Stat(ran)
		// The ask's card, not the turn's text: the text reveals over
		// frames and may still show only its first word.
		return err == nil || h.tt.HasText("Allow once")
	})
	if p := h.a.current().View.Model.Provider; p != "claude" {
		t.Fatalf("the turn ran on %q; the test means to send to Claude Code", p)
	}
	if out, err := os.ReadFile(ran); err == nil {
		t.Fatalf("the app a test starts ran claude: %s", strings.TrimSpace(string(out)))
	}
}

// Nor does it run agy, with one on PATH: only the app's main wires in the
// ACP agents (daemon.Config.WithAgents).
func TestTheGUIsTestsNeverRunAgy(t *testing.T) {
	ran := tripwire(t, "agy")
	h := start(t, tempDir(t))
	sendNew(h, "Say hi.")
	h.until("the fake's ask, or an agy run", func() bool {
		_, err := os.Stat(ran)
		return err == nil || h.tt.HasText("Allow once")
	})
	if out, err := os.ReadFile(ran); err == nil {
		t.Fatalf("the app a test starts ran agy: %s", strings.TrimSpace(string(out)))
	}
}

// With Claude Code wired in as the app's main has it, a missing claude
// fails the turn with what to do, in the thread.
func TestAMissingClaudeSaysSoInTheThread(t *testing.T) {
	h := startWith(t, tempDir(t), func(cfg *daemon.Config) {
		cfg.ClaudePath = filepath.Join(t.TempDir(), "claude")
		cfg.WithClaude()
	})
	sendNew(h, "Say hi.")
	h.until("the failure in the thread", func() bool {
		return h.tt.HasText("Claude Code is not installed.")
	})
}
