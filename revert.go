package main

import (
	"cmp"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
)

// revertAsk is a revert the alert asks about: of t's files to to, the
// checkpoint before a turn when undoTurn; or a rewind of its conversation
// to to's run, with its files too when files.
type revertAsk struct {
	t        thread
	to       api.CheckpointRef
	undoTurn bool
	rewind   bool
	files    bool
}

// whyNoRevert is why t's files can't be reverted now, "" when they can,
// as far as the client sees what the engine refuses for. The engine checks
// again when the revert commits.
func (a *app) whyNoRevert(t thread) string {
	switch {
	case !t.View.Parent.IsZero():
		return "A child thread keeps no checkpoints; revert its parent instead"
	case t.View.Branch == "":
		return "Revert needs this thread to run in its own worktree"
	case t.View.Live():
		return "Stop the thread before reverting it"
	case working(a.below(t)) > 0:
		return "Stop every thread under this one before reverting its files"
	case t.Transcript != nil && !t.Transcript.Reverting.IsZero():
		return "A revert is under way"
	}
	if other := a.inWorktree(t); other != "" {
		return fmt.Sprintf("The thread %q runs in this worktree too", other)
	}
	return ""
}

// inWorktree is the title of another thread whose folder is in t's
// worktree or around it, "" when none is: one the revert would put the
// files back under, as the engine refuses. The worktree is the folder
// named after t that holds its workspace; a thread under t is t's to stop.
func (a *app) inWorktree(t thread) string {
	dir := t.View.Workspace
	for dir != filepath.Dir(dir) && filepath.Base(dir) != strings.ToLower(t.id().String()) {
		dir = filepath.Dir(dir)
	}
	if dir == filepath.Dir(dir) {
		dir = t.View.Workspace
	}
	in := func(dir, path string) bool {
		rel, err := filepath.Rel(dir, path)
		return path != "" && err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	}
	mine := map[api.ID]bool{t.id(): true}
	for _, n := range a.below(t) {
		mine[n.t.id()] = true
	}
	var titles []string
	for id, other := range t.env.Threads {
		if !mine[id] && !other.View.Deleted && (in(dir, other.View.Workspace) || in(other.View.Workspace, dir)) {
			titles = append(titles, other.View.Title)
		}
	}
	slices.Sort(titles)
	if len(titles) == 0 {
		return ""
	}
	return titles[0]
}

// whyNoRewind is why t's conversation can't be rewound to run now, "" when
// it can.
func (a *app) whyNoRewind(t thread, run api.ID) string {
	switch {
	case t.View.Live():
		return "Stop the thread before reverting it"
	case !t.Transcript.Reverting.IsZero():
		return "A revert is under way"
	case t.Transcript.Dropped(run):
		return "A rewind took this turn out of the conversation"
	case !t.Transcript.Followed(run):
		return "The conversation already ends at this turn"
	}
	return ""
}

// checkpointChip is a top-level run's chip on its work row: how many files
// the run changed, as its checkpoint saw them, or that they weren't saved
// and why, with a menu that puts them back as the run left them or as they
// were before it, or rewinds the conversation to the run, with its files
// or without. The chip's tooltip says what the chip can do and why it
// can't do the rest, and the menu leads with the same reasons.
func (a *app) checkpointChip(c *ui.Context, t thread, run api.ID) {
	if !t.View.Parent.IsZero() || t.Transcript == nil {
		return
	}
	at, before, saved, hasBefore := t.Transcript.Checkpoint(run)
	rewindWhy := a.whyNoRewind(t, run)
	if t.View.Branch != "" && !saved && rewindWhy != "" {
		return
	}
	// Each action's reason, once, for the tooltip and the menu alike: the
	// thread's, or the checkpoint's the action puts back.
	why := a.whyNoRevert(t)
	hereWhy, undoWhy := why, why
	var details []string
	if why == "" && at.Status != api.CheckpointReady {
		hereWhy = "This turn's files weren't saved"
		details = append(details, strings.TrimSuffix(hereWhy+": "+at.Reason, ": "))
	}
	if why == "" && (!hasBefore || before.Status != api.CheckpointReady) {
		undoWhy = "The files before this turn weren't saved"
		details = append(details, strings.TrimSuffix(undoWhy+": "+before.Reason, ": "))
	}
	bothWhy := cmp.Or(rewindWhy, hereWhy)
	reasons := slices.Compact(slices.DeleteFunc([]string{hereWhy, undoWhy, rewindWhy}, func(s string) bool { return s == "" }))
	if why != "" {
		details = []string{why}
	}
	if rewindWhy != "" && rewindWhy != why {
		details = append(details, rewindWhy)
	}
	files, rewind := hereWhy == "" || undoWhy == "", rewindWhy == ""
	var tip []string
	switch {
	case files && rewind:
		tip = []string{"Revert this turn's files or conversation"}
	case files:
		tip = []string{"Revert this turn's files"}
	case rewind:
		tip = []string{"Rewind the conversation to this turn"}
	}
	tip = append(tip, details...)
	label := ""
	switch {
	case t.View.Branch == "":
	case at.Status != api.CheckpointReady:
		label = "Not saved"
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
	a.tip(c, b, strings.Join(tip, "\n"))
	b.Menu(func(m *ui.Menu) {
		// A system menu may show no subtitle on an item it greys out, so
		// each reason the menu refuses for leads it, on a line of its own.
		// A menu is built as it opens: one open keeps the reasons it had.
		for _, r := range reasons {
			m.Item(r).Disabled(true)
		}
		if len(reasons) > 0 {
			m.Separator()
		}
		here := m.Item("Restore files to here").Disabled(hereWhy != "")
		undo := m.Item("Undo this turn's files").Disabled(undoWhy != "")
		m.Separator()
		rewind := m.Item("Rewind conversation to here").Disabled(rewindWhy != "")
		both := m.Item("Rewind conversation and files to here").Disabled(bothWhy != "")
		to := api.CheckpointRef{Phase: api.CheckpointRun, ID: run}
		switch {
		case here.Chosen():
			a.reverting, a.revertOpen = revertAsk{t: t, to: to}, true
		case undo.Chosen():
			a.reverting, a.revertOpen = revertAsk{t: t, to: api.CheckpointRef{Phase: before.Phase, ID: before.Run}, undoTurn: true}, true
		case rewind.Chosen():
			a.reverting, a.revertOpen = revertAsk{t: t, to: to, rewind: true}, true
		case both.Chosen():
			a.reverting, a.revertOpen = revertAsk{t: t, to: to, rewind: true, files: true}, true
		}
	})
}

// rewindBody is what a rewind of the conversation does, as its alert says.
const rewindBody = "The agent's next turn follows this one, and it forgets the turns after it, which stay in the timeline. Where its provider can't cut its own conversation, it starts a new one that's handed this turn and the ones before it."

// revertAlert asks before the files are put back, or the conversation is
// rewound: what goes back, that a checkpoint of the files as they are
// comes first, and that commits stay.
func (a *app) revertAlert(c *ui.Context) {
	r := a.reverting
	if r.t.Thread == nil {
		return
	}
	head := "Restore the files to this turn?"
	body := "The worktree's files go back to how this turn left them."
	files := " Changes made since, by the agent or by hand, go back too. The files as they are now are saved first, so Undo brings them back.\n\nFiles created since then, by the agent or by hand, are removed; Undo brings them back.\n\nCommits stay: git status shows the restored files as changes."
	ok := "Restore files"
	switch {
	case r.rewind && r.files:
		head, body, ok = "Rewind the conversation and files to this turn?", rewindBody+"\n\nThe worktree's files go back to how this turn left them too.", "Rewind"
	case r.rewind:
		head, body, files, ok = "Rewind the conversation to this turn?", rewindBody+" The files stay as they are.", "", "Rewind"
	case r.undoTurn:
		head, body = "Undo this turn's files?", "The worktree's files go back to how they were before this turn."
	}
	switch i := a.alert(c, &a.revertOpen, head, body+files, "Cancel", ok); {
	case i == 1 && r.rewind:
		a.backend.Rewind(r.t.env, r.t.id(), r.to, r.files)
		a.reverting = revertAsk{}
	case i == 1:
		a.backend.Revert(r.t.env, r.t.id(), r.to)
		a.reverting = revertAsk{}
	case i == 0, i == ui.AlertDismissed:
		a.reverting = revertAsk{}
	}
}

// revertDivider is the row es[i] of the timeline: how a revert of the
// files or a rewind of the conversation ended, with an Undo while no
// message, run or revert came after it. Undo puts back the files the
// revert saved first, after one that failed too, for files its rollback
// couldn't put back; after a rewind, it brings back the conversation as it
// was before it too, the files only where the rewind kept them. Where the
// conversation can't come back, as after an Undo of a rewind or a rewind
// that failed, it says before the click that it puts back the files only.
// An Undo of a rewind that failed changed nothing, so its row puts back
// the files the rewind saved, as the rewind's own Undo would have.
// A rewind says whether the agent kept its own conversation or was handed
// a new one, as the run after it took it up once one has, and its Undo
// says before the click which of the two it will be: native only after a
// rewind the agent forked at once, as Codex's, and portable after one it
// would branch only at its next run, as Claude's, or couldn't at all. Its
// sentence wraps rather than clip, so the badge and the Undo keep their
// width.
func (a *app) revertDivider(c *ui.Context, t thread, es []entry, i int) {
	r := es[i].revert
	last := !slices.ContainsFunc(es[i+1:], func(e entry) bool {
		return e.kind == entryWork || e.kind == entryRevert || e.kind == entryAssistant || e.kind == entryUser
	})
	unrewind := r.Rewound() && len(r.Restored) == 0
	back, saved := r.Revert, r.Recovery != ""
	if !saved && r.To.Phase == api.CheckpointRecovery && r.Conversation == api.RewindFailed {
		back, saved = r.To.ID, slices.ContainsFunc(es[:i], func(e entry) bool {
			return e.kind == entryRevert && e.revert.Revert == r.To.ID && e.revert.Recovery != ""
		})
	}
	undo := func() {
		if !last || !saved && !unrewind {
			return
		}
		why, label, tip := a.whyNoRevert(t), "Undo", ""
		native := r.Conversation == api.RewindNative && r.ThreadReverted.At == ""
		switch {
		case unrewind && r.Recovery == "":
			why, tip = "", "Native: brings back the agent's own conversation as it was before the rewind"
			if !native {
				tip = "Portable: hands the conversation's turns, the ones the rewind took out among them, to a new conversation"
			}
			if t.View.Live() {
				why = "Stop the thread before reverting it"
			} else if !t.Transcript.Reverting.IsZero() {
				why = "A revert is under way"
			}
		case unrewind && native:
			tip = "Native: brings back the agent's own conversation and the files as they were before the rewind"
		case unrewind:
			tip = "Portable: brings back the files as they were before the rewind, and hands the conversation's turns to a new conversation"
		case r.Rewound() || r.Conversation == api.RewindFailed:
			label, tip = "Undo files only", "Puts back the files; the conversation stays as it is"
		}
		b := ghost(c, "undo", false).Padding(2, 6).Shrink(0).Children(func() { ui.Text(c, label).FontSize(11.5) })
		if why != "" {
			a.tip(c, b.TextColor(palette.faint), why)
			return
		}
		a.tip(c, b, tip)
		switch {
		case !b.Clicked():
		case unrewind:
			a.backend.Rewind(t.env, t.id(), api.CheckpointRef{Phase: api.CheckpointRecovery, ID: r.Revert}, r.Recovery != "")
		default:
			a.backend.Revert(t.env, t.id(), api.CheckpointRef{Phase: api.CheckpointRecovery, ID: back})
		}
	}
	ui.Row(c).Key(es[i].key).Gap(10).AlignItems(ui.Center).Padding(4, 12).Transition(riseIn).Children(func() {
		ui.Box(c).Height(1).Grow(1).Background(palette.border)
		switch {
		case r.Conversation == api.RewindFailed:
			ui.Text(c, "Couldn't rewind the conversation: "+r.Error).FontSize(11.5).TextColor(palette.red).Shrink(1).MinWidth(0)
			undo()
		case r.Rewound():
			text, restored := "Rewound the conversation", " and restored "
			kind, tip := "Native", "The agent's own conversation, cut after this turn"
			portable := "A new conversation, handed the turns up to here"
			if len(r.Restored) > 0 {
				text, restored = "Brought back the conversation", " and put back "
				tip, portable = "The agent's own conversation, as it was before the rewind", "A new conversation, handed the turns the rewind took out"
			}
			if r.Status == api.RevertRestored {
				text += restored + plural(r.Changed, "file")
			}
			ui.Text(c, text).FontSize(11.5).TextColor(palette.faint).TextAlign(ui.Center).Shrink(1)
			if cmp.Or(r.Took, r.Conversation) == api.RewindPortable {
				kind, tip = "Portable", portable
			}
			a.tip(c, ui.Box(c).Label(kind).Padding(1, 6).Radius(6).Border(1, palette.border).Shrink(0).Children(func() {
				ui.Text(c, kind).FontSize(10.5).TextColor(palette.faint).AccessHidden(true)
			}), tip)
			undo()
		case r.Status == api.RevertRestored:
			text := "Restored " + plural(r.Changed, "file")
			if r.To.Phase == api.CheckpointRecovery {
				text = "Put back " + plural(r.Changed, "file") + " as they were before a revert"
			}
			ui.Text(c, text).FontSize(11.5).TextColor(palette.faint).TextAlign(ui.Center).Shrink(1)
			undo()
		case r.Status == api.RevertUnchanged:
			ui.Text(c, "The files were already as they were then").FontSize(11.5).TextColor(palette.faint).TextAlign(ui.Center).Shrink(1)
		default:
			ui.Text(c, "Couldn't revert the files: "+r.Error).FontSize(11.5).TextColor(palette.red).Shrink(1).MinWidth(0)
			undo()
		}
		ui.Box(c).Height(1).Grow(1).Background(palette.border)
	})
}
