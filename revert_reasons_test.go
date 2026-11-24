package main

import (
	"cmp"
	"slices"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/client"
)

// Each reason a turn's chip refuses its files for reaches the person where
// they meet it: the chip's tooltip, for one that refuses both, and the
// chip's menu, above the choices it greys out, as a line of its own that a
// system menu shows whether or not it shows a disabled item's subtitle.
func TestEveryRevertRefusalSaysWhy(t *testing.T) {
	for name, tc := range map[string]struct {
		as      func(h *harness, th thread)
		why     string
		chipTip bool
		refused []string
		chip    string // the chip's label, the second turn's by default
	}{
		"while the thread runs": {
			as:      func(_ *harness, th thread) { th.View.Run = api.RunRunning },
			why:     "Stop the thread before reverting its files",
			chipTip: true,
			chip:    "No file changes",
			refused: []string{"Restore files to here", "Undo this turn's files"},
		},
		"while a thread under it runs": {
			as: func(_ *harness, th thread) {
				kid := api.NewID()
				th.env.Threads[kid] = &client.Thread{View: api.Thread{ID: kid, Parent: th.id(), Title: "A child", Workspace: th.View.Workspace, Run: api.RunRunning}}
				th.env.Membership++
			},
			why:     "Stop every thread under this one before reverting its files",
			chipTip: true,
			refused: []string{"Restore files to here", "Undo this turn's files"},
		},
		"while a revert runs": {
			as:      func(_ *harness, th thread) { th.Transcript.Reverting = api.NewID() },
			why:     "A revert of the files is under way",
			chipTip: true,
			refused: []string{"Restore files to here", "Undo this turn's files"},
		},
		"when the turn's files weren't saved": {
			as: func(_ *harness, th thread) {
				th.Transcript.Checkpoints[2].Status, th.Transcript.Checkpoints[2].Reason = api.CheckpointSkipped, "repository over 256 MiB"
			},
			chip:    "Not saved",
			why:     "This turn's files weren't saved",
			refused: []string{"Restore files to here"},
		},
		"when the files before the turn weren't saved": {
			as: func(_ *harness, th thread) {
				th.Transcript.Checkpoints[1].Status, th.Transcript.Checkpoints[1].Reason = api.CheckpointFailed, "git failed"
			},
			why:     "The files before this turn weren't saved",
			refused: []string{"Undo this turn's files"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			h, th, _ := twoTurnsInAWorktree(t, nil)
			tc.as(h, th)
			h.tt.Frame()
			h.tt.Advance(time.Second)
			chip, ok := h.tt.Find(cmp.Or(tc.chip, "1 file"))
			if !ok {
				t.Fatalf("no chip %q", cmp.Or(tc.chip, "1 file"))
			}
			if tc.chipTip {
				h.tt.Move(chip.X+chip.W/2, chip.Y+chip.H/2)
				h.tt.Advance(2 * time.Second)
				h.tt.Frame()
				if !h.tt.HasText(tc.why) {
					t.Errorf("the chip's tooltip doesn't say %q", tc.why)
				}
				h.tt.Move(0, 0)
				h.tt.Advance(time.Second)
			}
			h.tt.ClickAt(chip.X+chip.W/2, chip.Y+chip.H/2)
			h.tt.Advance(time.Second)
			m := h.tt.Menu()
			if len(m) < 2 || m[0] != tc.why || m[1] != "-" {
				t.Fatalf("the chip's menu %q doesn't lead with %q", m, tc.why)
			}
			if slices.Index(m[1:], tc.why) >= 0 {
				t.Errorf("the menu %q says %q twice", m, tc.why)
			}
			for _, item := range append([]string{tc.why}, tc.refused...) {
				if err := h.tt.ChooseMenuItem(item); err == nil {
					t.Fatalf("%q could be chosen", item)
				}
			}
		})
	}
}
