package main

import (
	"cmp"
	"slices"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
)

// revertAsk is a revert the alert asks about: of t's files to to, the
// checkpoint before a turn when undoTurn.
type revertAsk struct {
	t        thread
	to       api.CheckpointRef
	undoTurn bool
}

// whyNoRevert is why t's files can't be reverted now, "" when they can.
func (a *app) whyNoRevert(t thread) string {
	switch {
	case !t.View.Parent.IsZero():
		return "A child thread keeps no checkpoints; revert its parent instead"
	case t.View.Branch == "":
		return "Revert needs this thread to run in its own worktree"
	case t.View.Live():
		return "Stop the thread before reverting its files"
	case working(a.below(t)) > 0:
		return "Stop every thread under this one before reverting its files"
	case t.Transcript != nil && !t.Transcript.Reverting.IsZero():
		return "A revert of the files is under way"
	}
	return ""
}

// checkpointChip is a top-level run's chip on its work row: how many files
// the run changed, as its checkpoint saw them, or that they weren't saved
// and why, with a menu that puts them back as the run left them or as they
// were before it. Where the thread can't revert, the chip's tooltip says
// why, and so does the menu, above what it can't do.
func (a *app) checkpointChip(c *ui.Context, t thread, run api.ID) {
	if !t.View.Parent.IsZero() || t.Transcript == nil {
		return
	}
	at, before, saved, hasBefore := t.Transcript.Checkpoint(run)
	why, label, tip := a.whyNoRevert(t), "", ""
	switch {
	case t.View.Branch == "":
		tip = why
	case !saved:
		return
	case at.Status != api.CheckpointReady:
		label, tip = "Not saved", "This turn's files weren't saved: "+at.Reason
	case at.Changed == 0:
		label = "No file changes"
	default:
		label = plural(at.Changed, "file")
	}
	b := ghost(c, "checkpoint", false).Padding(2, 6).Gap(4).Children(func() {
		ui.Icon(c, icUndo).FontSize(11)
		if label != "" {
			ui.Text(c, label).FontSize(11)
		}
	})
	if label == "" {
		b.Label("Checkpoint")
	}
	a.tip(c, b, cmp.Or(why, tip, "Revert this turn's files"))
	b.Menu(func(m *ui.Menu) {
		hereWhy, undoWhy := why, why
		if hereWhy == "" && at.Status != api.CheckpointReady {
			hereWhy = "This turn's files weren't saved"
		}
		if undoWhy == "" && (!hasBefore || before.Status != api.CheckpointReady) {
			undoWhy = "The files before this turn weren't saved"
		}
		// A system menu may show no subtitle on an item it greys out, so
		// each reason the menu refuses for leads it, on a line of its own,
		// once, however many items it refuses.
		reasons := slices.Compact(slices.DeleteFunc([]string{hereWhy, undoWhy}, func(s string) bool { return s == "" }))
		for _, r := range reasons {
			m.Item(r).Disabled(true)
		}
		if len(reasons) > 0 {
			m.Separator()
		}
		here := m.Item("Restore files to here").Disabled(hereWhy != "")
		undo := m.Item("Undo this turn's files").Disabled(undoWhy != "")
		if here.Chosen() {
			a.reverting, a.revertOpen = revertAsk{t: t, to: api.CheckpointRef{Phase: api.CheckpointRun, ID: run}}, true
		}
		if undo.Chosen() {
			a.reverting, a.revertOpen = revertAsk{t: t, to: api.CheckpointRef{Phase: before.Phase, ID: before.Run}, undoTurn: true}, true
		}
	})
}

// revertAlert asks before the files are put back: what goes back, that a
// checkpoint of them as they are comes first, and that commits stay.
func (a *app) revertAlert(c *ui.Context) {
	r := a.reverting
	if r.t.Thread == nil {
		return
	}
	head := "Restore the files to this turn?"
	body := "The worktree's files go back to how this turn left them."
	if r.undoTurn {
		head, body = "Undo this turn's files?", "The worktree's files go back to how they were before this turn."
	}
	body += " Changes made since, by the agent or by hand, go back too. The files as they are now are saved first, so Undo brings them back.\n\nCommits stay: git status shows the restored files as changes."
	switch i := a.alert(c, &a.revertOpen, head, body, "Cancel", "Restore files"); {
	case i == 1:
		a.backend.Revert(r.t.env, r.t.id(), r.to)
		a.reverting = revertAsk{}
	case i == 0, i == ui.AlertDismissed:
		a.reverting = revertAsk{}
	}
}

// revertDivider is the row es[i] of the timeline: how a revert of the
// files ended, with an Undo that puts back the files it saved first, while
// no message, run or revert came after it: after one that failed too, for
// files its rollback couldn't put back.
func (a *app) revertDivider(c *ui.Context, t thread, es []entry, i int) {
	r := es[i].revert
	last := !slices.ContainsFunc(es[i+1:], func(e entry) bool {
		return e.kind == entryWork || e.kind == entryRevert || e.kind == entryAssistant || e.kind == entryUser
	})
	undo := func() {
		if !last || r.Recovery == "" {
			return
		}
		why := a.whyNoRevert(t)
		b := ghost(c, "undo", false).Padding(2, 6).Children(func() { ui.Text(c, "Undo").FontSize(11.5) })
		if why != "" {
			a.tip(c, b.TextColor(palette.faint), why)
		} else if b.Clicked() {
			a.backend.Revert(t.env, t.id(), api.CheckpointRef{Phase: api.CheckpointRecovery, ID: r.Revert})
		}
	}
	ui.Row(c).Key(es[i].key).Gap(10).AlignItems(ui.Center).Padding(4, 12).Transition(riseIn).Children(func() {
		ui.Box(c).Height(1).Grow(1).Background(palette.border)
		switch r.Status {
		case api.RevertRestored:
			text := "Restored " + plural(r.Changed, "file")
			if r.To.Phase == api.CheckpointRecovery {
				text = "Put back " + plural(r.Changed, "file") + " as they were before a revert"
			}
			ui.Text(c, text).FontSize(11.5).TextColor(palette.faint).SingleLine()
			undo()
		case api.RevertUnchanged:
			ui.Text(c, "The files were already as they were then").FontSize(11.5).TextColor(palette.faint).SingleLine()
		default:
			ui.Text(c, "Couldn't revert the files: "+r.Error).FontSize(11.5).TextColor(palette.red).Shrink(1)
			undo()
		}
		ui.Box(c).Height(1).Grow(1).Background(palette.border)
	})
}
