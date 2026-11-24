package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/client"
)

// Each reason a turn's chip refuses an action for reaches the person where
// they meet it: the chip's tooltip, which says what the chip can still do,
// and the chip's menu, above the choices it greys out, as a line of its own
// that a system menu shows whether or not it shows a disabled item's
// subtitle. Each choice stays in the menu, greyed out where it's refused,
// and one that isn't refused still asks.
func TestEveryRevertRefusalSaysWhy(t *testing.T) {
	const (
		restore = "Restore files to here"
		undo    = "Undo this turn's files"
		rewind  = "Rewind conversation to here"
		both    = "Rewind conversation and files to here"
		ends    = "The conversation already ends at this turn"
	)
	all := []string{restore, undo, rewind, both}
	asks := map[string]string{
		restore: "Restore the files to this turn?",
		undo:    "Undo this turn's files?",
		rewind:  "Rewind the conversation to this turn?",
		both:    "Rewind the conversation and files to this turn?",
	}
	for name, tc := range map[string]struct {
		as      func(h *harness, th thread)
		why     []string // the menu's reason lines
		tip     []string // what the chip's tooltip says, the reasons by default
		refused []string
		chip    string // the chip's label, the second turn's by default
	}{
		"while the thread runs": {
			as:      func(_ *harness, th thread) { th.View.Run = api.RunRunning },
			why:     []string{"Stop the thread before reverting it"},
			chip:    "No file changes",
			refused: all,
		},
		"while a thread under it runs": {
			as: func(_ *harness, th thread) {
				kid := api.NewID()
				th.env.Threads[kid] = &client.Thread{View: api.Thread{ID: kid, Parent: th.id(), Title: "A child", Workspace: th.View.Workspace, Run: api.RunRunning}}
				th.env.Membership++
			},
			why:     []string{"Stop every thread under this one before reverting its files", ends},
			refused: all,
		},
		"while another thread runs in its worktree": {
			as: func(h *harness, th thread) {
				sub := filepath.Join(th.View.Workspace, "overlap")
				if err := os.Mkdir(sub, 0o755); err != nil {
					t.Fatal(err)
				}
				id := h.a.backend.Create(th.env, sub, "Other writer", th.View.Model, th.View.Access, nil)
				h.until("the engine has the other thread", func() bool {
					o := th.env.Threads[id]
					return o != nil && len(o.Pending) == 0 && o.View.Workspace == sub
				})
			},
			why:     []string{`The thread "Other writer" runs in this worktree too`, ends},
			refused: all,
		},
		"while a revert runs": {
			as:      func(_ *harness, th thread) { th.Transcript.Reverting = api.NewID() },
			why:     []string{"A revert is under way"},
			refused: all,
		},
		"when the turn's files weren't saved": {
			as: func(_ *harness, th thread) {
				th.Transcript.Checkpoints[2].Status, th.Transcript.Checkpoints[2].Reason = api.CheckpointSkipped, "repository over 256 MiB"
			},
			chip:    "Not saved",
			why:     []string{"This turn's files weren't saved", ends},
			tip:     []string{"Revert this turn's files", "This turn's files weren't saved: repository over 256 MiB", ends},
			refused: []string{restore, rewind, both},
		},
		"when the files before the turn weren't saved": {
			as: func(_ *harness, th thread) {
				th.Transcript.Checkpoints[1].Status, th.Transcript.Checkpoints[1].Reason = api.CheckpointFailed, "git failed"
			},
			why:     []string{"The files before this turn weren't saved", ends},
			tip:     []string{"Revert this turn's files", "The files before this turn weren't saved: git failed", ends},
			refused: []string{undo, rewind, both},
		},
		// A turn whose checkpoint never came still rewinds the conversation.
		"when the turn's files were never saved": {
			as: func(_ *harness, th thread) {
				th.Transcript.Checkpoints = slices.Delete(th.Transcript.Checkpoints, 1, 2)
			},
			chip:    "Not saved",
			why:     []string{"This turn's files weren't saved", "The files before this turn weren't saved"},
			tip:     []string{"Rewind the conversation to this turn", "This turn's files weren't saved", "The files before this turn weren't saved"},
			refused: []string{restore, undo, both},
		},
		// The first turn's chip, as the second's would show the first's
		// files unsaved too; its conversation can still rewind.
		"when neither the turn's files nor those before it were saved": {
			as: func(_ *harness, th thread) {
				th.Transcript.Checkpoints[1].Status, th.Transcript.Checkpoints[1].Reason = api.CheckpointSkipped, "repository over 256 MiB"
				th.Transcript.Checkpoints[0].Status, th.Transcript.Checkpoints[0].Reason = api.CheckpointFailed, "git failed"
			},
			chip:    "Not saved",
			why:     []string{"This turn's files weren't saved", "The files before this turn weren't saved"},
			tip:     []string{"Rewind the conversation to this turn", "This turn's files weren't saved: repository over 256 MiB", "The files before this turn weren't saved: git failed"},
			refused: []string{restore, undo, both},
		},
	} {
		t.Run(name, func(t *testing.T) {
			h, th, _ := twoTurnsInAWorktree(t, nil)
			tc.as(h, th)
			h.tt.Frame()
			h.tt.Advance(time.Second)
			label := "1 file"
			if tc.chip != "" {
				label = tc.chip
			}
			chip, ok := h.tt.Find(label)
			if !ok {
				t.Fatalf("no chip %q", label)
			}
			h.tt.Move(chip.X+chip.W/2, chip.Y+chip.H/2)
			h.tt.Advance(2 * time.Second)
			h.tt.Frame()
			tip := tc.tip
			if tip == nil {
				tip = tc.why
			}
			for _, want := range tip {
				if !h.tt.HasText(want) {
					t.Errorf("the chip's tooltip doesn't say %q", want)
				}
			}
			h.tt.Move(0, 0)
			h.tt.Advance(time.Second)
			h.tt.ClickAt(chip.X+chip.W/2, chip.Y+chip.H/2)
			h.tt.Advance(time.Second)
			if got, want := h.tt.Menu(), slices.Concat(tc.why, []string{"-", restore, undo, "-", rewind, both}); !slices.Equal(got, want) {
				t.Fatalf("the chip's menu is %q, want %q", got, want)
			}
			for _, item := range append(slices.Clone(tc.why), tc.refused...) {
				if err := h.tt.ChooseMenuItem(item); err == nil || !strings.Contains(err.Error(), "is disabled") {
					t.Fatalf("choosing %q: %v, want it disabled", item, err)
				}
			}
			for _, item := range all {
				if slices.Contains(tc.refused, item) {
					continue
				}
				if err := h.tt.ChooseMenuItem(item); err != nil {
					t.Fatalf("choosing %q: %v", item, err)
				}
				h.tt.Advance(time.Second)
				if !h.tt.HasText(asks[item]) {
					t.Fatalf("choosing %q doesn't ask %q", item, asks[item])
				}
			}
		})
	}
}
