package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// twoTurnsInAWorktree is a thread in a worktree of its own whose second
// turn added notes.txt, as the turn's checkpoint saw it once it ended.
func twoTurnsInAWorktree(t *testing.T) (*harness, thread, string) {
	t.Helper()
	h, seeded := workspaceHarness(t)
	th := worktreeThread(t, h, seeded.View.Workspace, "Write the notes")
	h.until("the first turn's checkpoint", func() bool {
		th = h.a.current()
		return th.Transcript != nil && len(th.Transcript.Checkpoints) == 2
	})
	notes := filepath.Join(th.View.Workspace, "notes.txt")
	if err := os.WriteFile(notes, []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.a.draft = "Keep going"
	h.click("Send")
	h.until("the second turn's checkpoint", func() bool {
		th = h.a.current()
		return !th.View.Live() && th.Transcript != nil && len(th.Transcript.Checkpoints) == 3
	})
	h.tt.Advance(time.Second)
	return h, th, notes
}

// A turn's chip undoes the files the turn changed, after asking, without
// touching the worktree's commits, index or branch, and the row that says
// so puts them back with Undo.
func TestATurnsFilesRevertAndUndo(t *testing.T) {
	h, th, notes := twoTurnsInAWorktree(t)
	wt := th.View.Workspace
	head, branch := gitRun(t, wt, "rev-parse", "HEAD"), branchIn(t, wt)
	h.click("1 file")
	h.tt.Advance(time.Second)
	if m := h.tt.Menu(); !slices.Equal(m, []string{"Restore files to here", "Undo this turn's files"}) {
		t.Fatalf("the chip's menu: %q", m)
	}
	if err := h.tt.ChooseMenuItem("Undo this turn's files"); err != nil {
		t.Fatal(err)
	}
	h.tt.Advance(time.Second)
	if !h.tt.HasText("Undo this turn's files?") {
		t.Fatalf("no alert asks first")
	}
	if _, err := os.Stat(notes); err != nil {
		t.Fatalf("the alert alone changed the files: %v", err)
	}
	h.click("Restore files")
	h.until("the files before the turn", func() bool { _, err := os.Stat(notes); return os.IsNotExist(err) })
	h.until("the row saying so", func() bool { h.tt.Frame(); return h.tt.HasText("Restored 1 file") })
	if got := gitRun(t, wt, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD moved from %s to %s", head, got)
	}
	if got := branchIn(t, wt); got != branch {
		t.Errorf("the branch went from %q to %q", branch, got)
	}
	if st := gitRun(t, wt, "status", "--porcelain"); st != "" {
		t.Errorf("the worktree isn't as it was before the turn: %q", st)
	}

	h.tt.Advance(time.Second)
	h.click("Undo")
	h.until("the notes back", func() bool { b, err := os.ReadFile(notes); return err == nil && string(b) == "mine\n" })
	h.until("the row saying so", func() bool { h.tt.Frame(); return h.tt.HasText("Put back 1 file as they were before a revert") })
	if st := gitRun(t, wt, "status", "--porcelain"); st != "?? notes.txt" {
		t.Errorf("after Undo git status says %q, want the notes untracked again", st)
	}
}

// A thread that runs in a folder of its own choosing, not a worktree of
// Glitched's, can't revert: its chip's menu says why.
func TestAThreadOutsideAWorktreeSaysWhyItCantRevert(t *testing.T) {
	h, seeded := workspaceHarness(t)
	h.a.select_(seeded)
	h.until("its transcript", func() bool { return seeded.Transcript != nil && len(seeded.Transcript.Entries) > 0 })
	h.tt.Advance(time.Second)
	h.click("Checkpoint")
	h.tt.Advance(time.Second)
	if err := h.tt.ChooseMenuItem("Restore files to here"); err == nil {
		t.Fatal("Restore files to here could be chosen")
	}
	if !h.tt.HasText("Revert needs this thread to run in its own worktree") {
		t.Errorf("the menu doesn't say why")
	}
}

// This machine shows how much the checkpoint store holds of its cap, once
// a thread's turns saved their files there.
func TestThisMachineShowsTheCheckpointStore(t *testing.T) {
	h, _, _ := twoTurnsInAWorktree(t)
	h.click("Settings")
	h.click("This machine")
	var got string
	h.until("the store's size", func() bool {
		h.tt.Advance(inspectEvery)
		i := slices.IndexFunc(h.tt.Texts(), func(s string) bool { return strings.HasSuffix(s, " of 2.0 GB") })
		if i >= 0 {
			got = h.tt.Texts()[i]
		}
		return i >= 0 && !strings.HasPrefix(got, "0 B ")
	})
	if !h.tt.HasText("CHECKPOINTS") {
		t.Errorf("the store's size %q shows with no Checkpoints card", got)
	}
}
