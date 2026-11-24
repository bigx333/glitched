package main

import (
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
)

// A thread in its project's folder can't put its files back, and its
// chip says so, but an earlier turn's chip still rewinds the
// conversation, as its tooltip says too.
func TestAProjectFolderThreadRewindsItsConversation(t *testing.T) {
	h, seeded := workspaceHarness(t)
	h.a.select_(seeded)
	h.until("its transcript", func() bool { return seeded.Transcript != nil && len(seeded.Transcript.Entries) > 0 })
	first := h.a.current().View.LastTurn.Run
	h.a.draft = "Keep going"
	h.click("Send")
	h.until("the second turn", func() bool {
		th := h.a.current()
		return !th.View.Live() && th.View.LastTurn != nil && th.View.LastTurn.Run != first
	})
	h.tt.Advance(time.Second)
	chip, ok := h.tt.Find("Checkpoint")
	if !ok {
		t.Fatal("the first turn has no chip")
	}
	h.tt.Move(chip.X+chip.W/2, chip.Y+chip.H/2)
	h.tt.Advance(2 * time.Second)
	h.tt.Frame()
	for _, want := range []string{"Rewind the conversation to this turn", "Revert needs this thread to run in its own worktree"} {
		if !h.tt.HasText(want) {
			t.Errorf("the chip's tooltip doesn't say %q", want)
		}
	}
	h.tt.Move(0, 0)
	h.tt.Advance(time.Second)
	h.tt.ClickAt(chip.X+chip.W/2, chip.Y+chip.H/2)
	h.tt.Advance(time.Second)
	want := []string{"Revert needs this thread to run in its own worktree", "-", "Restore files to here", "Undo this turn's files", "-", "Rewind conversation to here", "Rewind conversation and files to here"}
	if got := h.tt.Menu(); !slices.Equal(got, want) {
		t.Fatalf("the chip's menu is %q, want %q", got, want)
	}
	if err := h.tt.ChooseMenuItem("Rewind conversation to here"); err != nil {
		t.Fatal(err)
	}
	h.tt.Advance(time.Second)
	if !h.tt.HasText("Rewind the conversation to this turn?") {
		t.Fatal("choosing the rewind doesn't ask")
	}
}

// An earlier turn's chip rewinds the conversation to it, after asking,
// leaving the files as they are or taking them back too; the row that
// says so names how the agent took the conversation up, and the next turn
// runs on. The last turn's chip says the conversation already ends there.
func TestATurnsChipRewindsTheConversation(t *testing.T) {
	for _, files := range []bool{false, true} {
		t.Run(map[bool]string{false: "leaving the files", true: "with the files"}[files], func(t *testing.T) {
			h, th, notes := twoTurnsInAWorktree(t, nil)
			h.click("1 file")
			h.tt.Advance(time.Second)
			if m := h.tt.Menu(); len(m) == 0 || m[0] != "The conversation already ends at this turn" {
				t.Fatalf("the last turn's menu %q doesn't say why it can't rewind", m)
			}
			if err := h.tt.ChooseMenuItem("Rewind conversation to here"); err == nil || !strings.Contains(err.Error(), "is disabled") {
				t.Fatalf("choosing the last turn's rewind: %v, want it disabled", err)
			}
			h.tt.Advance(time.Second)
			h.click("No file changes")
			h.tt.Advance(time.Second)
			item, head, row := "Rewind conversation to here", "Rewind the conversation to this turn?", "Rewound the conversation"
			if files {
				item, head, row = "Rewind conversation and files to here", "Rewind the conversation and files to this turn?", "Rewound the conversation and restored 1 file"
			}
			if m := h.tt.Menu(); slices.ContainsFunc(m, func(s string) bool { return strings.HasSuffix(s, "this turn") }) {
				t.Fatalf("the first turn's menu refuses: %q", m)
			}
			if err := h.tt.ChooseMenuItem(item); err != nil {
				t.Fatalf("%v; the menu has %q", err, h.tt.Menu())
			}
			h.tt.Advance(time.Second)
			if !h.tt.HasText(head) {
				t.Fatal("no alert asks first")
			}
			h.click("Rewind")
			h.until("the row saying so", func() bool { h.tt.Frame(); return h.tt.HasText(row) && h.tt.HasText("Portable") })
			h.click("1 file")
			h.tt.Advance(time.Second)
			if m := h.tt.Menu(); len(m) == 0 || m[0] != "A rewind took this turn out of the conversation" {
				t.Fatalf("the dropped turn's menu %q doesn't say why it can't rewind to it", m)
			}
			if err := h.tt.ChooseMenuItem("Rewind conversation to here"); err == nil || !strings.Contains(err.Error(), "is disabled") {
				t.Fatalf("choosing the dropped turn's rewind: %v, want it disabled", err)
			}
			h.tt.Advance(time.Second)
			_, err := os.Stat(notes)
			if files != os.IsNotExist(err) {
				t.Fatalf("notes.txt: %v; the files went back %v", err, files)
			}
			h.a.draft = "Pick it up from there"
			h.click("Send")
			h.until("the turn after the rewind", func() bool {
				th = h.a.current()
				return !th.View.Live() && th.View.LastTurn != nil && th.View.LastTurn.Outcome == api.OutcomeCompleted && len(th.Transcript.Checkpoints) >= 4
			})
		})
	}
}
