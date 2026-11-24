package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/provider/codex/codextest"
)

// codexRewound is a Codex thread in a worktree of its own whose second
// turn came after notes.txt was written by hand, rewound to its first
// turn with the files, natively: the rewind took the notes out. It
// returns the rollout of the conversation the thread had before the
// rewind.
func codexRewound(t *testing.T) (*harness, string, string) {
	t.Helper()
	h := startWith(t, tempDir(t), withCodex(t, "memory"))
	allAnswered(h)
	h.a.startIn(draft{env: h.local(), workspace: gitWorkspace(t)})
	h.a.model = modelKey{"codex", "gpt-6.2-sol"}
	h.tt.Frame()
	h.a.draft = "first"
	h.click("Workspace")
	h.tt.Advance(time.Second)
	if err := h.tt.ChooseMenuItem("New worktree"); err != nil {
		t.Fatal(err)
	}
	h.tt.Advance(time.Second)
	h.click("Send")
	h.until("the first turn's checkpoint", func() bool {
		th := h.a.current()
		return th.View.Branch != "" && !th.View.Live() && th.Transcript != nil && len(th.Transcript.Checkpoints) == 2
	})
	th := h.a.current()
	first := th.Transcript.Checkpoints[1].Run
	notes := filepath.Join(th.View.Workspace, "notes.txt")
	if err := os.WriteFile(notes, []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.a.draft = "second"
	h.click("Send")
	h.until("the second turn's checkpoint", func() bool {
		th := h.a.current()
		return !th.View.Live() && th.Transcript != nil && len(th.Transcript.Checkpoints) == 3
	})
	h.a.backend.Rewind(th.env, th.id(), api.CheckpointRef{Phase: api.CheckpointRun, ID: first}, true)
	h.until("the rewind's row", func() bool { h.tt.Frame(); return h.tt.HasText("Rewound the conversation and restored 1 file") })
	var from string
	for _, e := range h.a.current().Transcript.Entries {
		if r, ok := e.(*client.RevertEntry); ok && r.Conversation == api.RewindNative {
			from = r.From
		}
	}
	if from == "" {
		t.Fatal("the rewind wasn't native")
	}
	if _, err := os.Stat(notes); !os.IsNotExist(err) {
		t.Fatalf("notes.txt after the rewind: %v, want it gone", err)
	}
	h.tt.Advance(time.Second)
	return h, notes, filepath.Join(filepath.Dir(os.Getenv(codextest.Log)), "rollout-"+from)
}

// An Undo of a rewind once Codex lost the conversation from before it
// still brings the files back, and hands the turns to a new conversation.
func TestAnUndoAfterCodexLostTheConversationHandsItBack(t *testing.T) {
	h, notes, rollout := codexRewound(t)
	if err := os.Remove(rollout); err != nil {
		t.Fatal(err)
	}
	h.click("Undo")
	h.until("the undo's row", func() bool {
		h.tt.Frame()
		return h.tt.HasText("Brought back the conversation and put back 1 file") || strings.Contains(strings.Join(h.tt.Texts(), "\n"), "Couldn't rewind")
	})
	if !h.tt.HasText("Brought back the conversation and put back 1 file") || !h.tt.HasText("Portable") {
		t.Fatalf("the undo's row reads %q; want the conversation brought back, portable", h.tt.Texts())
	}
	if b, err := os.ReadFile(notes); err != nil || string(b) != "mine\n" {
		t.Fatalf("notes.txt %q, %v after the undo; want it back", b, err)
	}
}

// An Undo of a rewind that fails changes nothing, and its row still puts
// back the files the rewind saved.
func TestAFailedUndoOfARewindStillPutsBackItsFiles(t *testing.T) {
	h, notes, rollout := codexRewound(t)
	if err := os.Remove(rollout + ".turns"); err != nil {
		t.Fatal(err)
	}
	h.click("Undo")
	h.until("the failed undo's row", func() bool {
		h.tt.Frame()
		return strings.Contains(strings.Join(h.tt.Texts(), "\n"), "Couldn't rewind the conversation")
	})
	if _, err := os.Stat(notes); !os.IsNotExist(err) {
		t.Fatalf("notes.txt after the failed undo: %v, want it still gone", err)
	}
	if !h.tt.HasText("Undo files only") {
		t.Fatalf("the failed undo's row reads %q; want it to offer the files", h.tt.Texts())
	}
	h.tt.Advance(time.Second)
	h.click("Undo files only")
	h.until("the notes back", func() bool { b, err := os.ReadFile(notes); return err == nil && string(b) == "mine\n" })
}
