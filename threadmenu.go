package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/providerlog"
)

// A thread row's context menu holds what is rarely done to a thread, so
// the row itself keeps to snoozing and settling. Rename edits the title
// in the row. What touches the workspace, its branch and the editor, is
// offered only for a thread on this machine, where the workspace is.

func (a *app) threadMenu(c *ui.Context, m *ui.Menu, t thread, cat category) {
	if m.Item("Rename").Chosen() {
		a.renaming, a.renameDraft, a.renameFocused = t.id(), title(t), false
	}
	working := t.View.Working()
	switch cat {
	case catInbox, catWorking:
		m.Submenu("Snooze", func(m *ui.Menu) {
			for _, s := range snoozeTimes {
				if m.Item(s.when).Chosen() {
					a.snooze(c, t, s.when, s.until(a.now), cat)
				}
			}
		}).Disabled(working)
		if m.Item("Mark settled").Disabled(working).Chosen() {
			a.settle(c, t, cat)
		}
	default:
		if m.Item("Back to inbox").Chosen() {
			a.backend.SetDisposition(t.env, t.id(), api.Disposition{})
			a.gag(c, t.id(), gagUndo, cat)
		}
	}
	m.Separator()
	none := t.View.LastTurn == nil
	if m.Item("Fork").Disabled(none).Chosen() {
		a.fork(t, api.ID{}, t.View.Model)
	}
	// The providers the picker knows are this machine's engine's, where a
	// fork of a thread here runs.
	if t.local() {
		others := slices.DeleteFunc(a.modelFamilies(), func(f family) bool { return f.provider == t.View.Model.Provider })
		m.Submenu("Continue on", func(m *ui.Menu) {
			for _, f := range others {
				if m.Item(f.name).Chosen() {
					a.fork(t, api.ID{}, api.ModelRef{Provider: f.provider})
				}
			}
		}).Disabled(none || len(others) == 0)
	}
	if t.local() {
		m.Separator()
		if m.Item("Copy branch name").Chosen() {
			if b, err := branchOf(projectDir(t.View.Workspace)); err != nil {
				c.AddToast(ui.Toast{Title: "No branch to copy", Description: err.Error(), Type: "info"})
			} else {
				c.WriteClipboard(b)
				c.AddToast(ui.Toast{Title: "Copied", Description: b})
			}
		}
		if ed := editorCLI(); ed != "" && m.Item("Open in editor").Chosen() {
			a.openInEditor(c, ed, projectDir(t.View.Workspace))
		}
		if path := a.providerLog(t); path != "" && m.Item("Reveal provider log").Chosen() {
			revealFile(path)
		}
	}
	a.pluginMenuItems(m, "thread.menu", func(plugin string) map[string]any {
		return map[string]any{"thread": a.plugins.Record(plugin, a.threadRecord(t))}
	})
	m.Separator()
	// The user stops a thread's own agent before deleting it, an uncertain
	// run too, as on main; the engine would end it unasked.
	if m.Item("Delete…").Disabled(t.View.Live()).Chosen() {
		a.askDelete(t)
	}
}

// revealFile shows the file at path in the file manager.
var revealFile = mygo.Shell.ShowItemInFolder

// providerLog is the current file of the provider log of t, a thread on
// this machine: "" while it has none.
func (a *app) providerLog(t thread) string {
	dir := a.backend.Machine().Config.Dir
	if dir == "" {
		return ""
	}
	path := providerlog.File(providerlog.Under(dir), t.id())
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	return path
}

// renameInput is the row's title while it's being renamed: Enter keeps
// what was typed, as the focus going elsewhere does; Escape goes back.
func (a *app) renameInput(c *ui.Context, t thread) {
	in := ui.TextInput(c.Key("rename"), &a.renameDraft).AutoFocus().FontSize(13).Grow(1).Shrink(1).
		Padding(2, 6).Radius(4).Label("Thread title")
	if in.Focused() {
		a.renameFocused = true
	}
	switch {
	case in.Shortcut(0, ui.KeyEscape):
		a.renaming = api.ID{}
	case in.Submitted(), a.renameFocused && !in.Focused():
		if s := strings.TrimSpace(a.renameDraft); s != "" && s != title(t) {
			a.backend.Rename(t.env, t.id(), s)
		}
		a.renaming = api.ID{}
	}
}

// settle puts the thread with the settled ones, done with, and every
// thread under it too: the engine stops those working.
func (a *app) settle(c *ui.Context, t thread, from category) {
	a.settleTree(t)
	a.backend.MarkRead(t.Thread)
	a.gag(c, t.id(), gagSettle, from)
	c.AddToast(ui.Toast{Title: "Settled. Phew.", Description: title(t)})
}

// askDelete deletes the thread and the threads under it, asking first
// while the settings say to.
func (a *app) askDelete(t thread) {
	if a.settings.confirmDelete {
		a.deleting, a.deleteOpen = t, true
		a.countDeleteChanges(a.worktreesIn(t))
		return
	}
	a.deleteTree(t, false)
}

// countDeleteChanges counts the changes in own, the worktrees a delete
// would remove, for the alert, and keeps which they are, so the alert
// counts again when they change under it.
func (a *app) countDeleteChanges(own []thread) {
	a.deleteChanges, a.deleteCounted = 0, nil
	for _, w := range own {
		a.deleteCounted = append(a.deleteCounted, w.id())
		n := changesIn(w)
		if n < 0 || a.deleteChanges < 0 {
			a.deleteChanges = -1
			continue
		}
		a.deleteChanges += n
	}
}

// deleteAlert asks before a thread is deleted, when the settings say to.
// Deleting leaves a tombstone, not erasure, so it promises no more.
func (a *app) deleteAlert(c *ui.Context) {
	if a.deleting.Thread == nil {
		return
	}
	head, body := "Delete “"+title(a.deleting)+"”?",
		"The thread is removed from every list and can't be brought back. Its history, messages included, stays in the engine's log."
	// Every thread under it goes with it, its children's children too, so
	// the count is of those, not of its children; those working stop first.
	if tree := a.below(a.deleting); len(tree) > 0 {
		under := "the thread under it"
		if len(tree) > 1 {
			under = fmt.Sprintf("the %d threads under it", len(tree))
		}
		head = "Delete “" + title(a.deleting) + "” and " + under + "?"
		body = "They're removed from every list and can't be brought back. Their history stays in the engine's log."
		if n := working(tree); n > 0 {
			body = fmt.Sprintf("%d of them %s still working and will be stopped. ", n, isAre(n)) + body
		}
	}
	// The buttons after Cancel, and whether each removes the worktree. A
	// thread's worktree stays unless the user says otherwise, and a
	// button says when what is in it goes too.
	buttons, removes := []string{"Delete"}, []bool{false}
	// The worktrees that go are the deleted thread's own and those of the
	// threads under it that have their own, as the engine removes them.
	if tree, own := append(a.below(a.deleting), node{a.deleting, 0}), a.worktreesIn(a.deleting); len(own) > 0 {
		ids := make([]api.ID, len(own))
		for i, w := range own {
			ids[i] = w.id()
		}
		if !slices.Equal(ids, a.deleteCounted) {
			a.countDeleteChanges(own)
		}
		one := len(own) == 1
		w, it, its := "worktree", "it", "It has"
		switch {
		case one && own[0].id() == a.deleting.id():
			body += "\n\nIts worktree on " + a.deleting.View.Branch + " stays unless you delete it too. The branch stays while it has commits of its own."
		case one:
			body += "\n\nOne thread under it has a worktree on " + own[0].View.Branch + ", which stays unless you delete it too. The branch stays while it has commits of its own."
		default:
			w, it, its = "worktrees", "them", "They have"
			body += fmt.Sprintf("\n\nThe %d worktrees of threads in it stay unless you delete them too. Each branch stays while it has commits of its own.", len(own))
		}
		buttons = []string{"Keep " + w}
		if working(tree) > 0 {
			body += " To delete the " + w + ", stop the work first."
		} else {
			remove := "Delete " + w + " and changes"
			body += " Deleting " + it + " deletes everything in " + it + ", files git ignores included."
			switch n := a.deleteChanges; {
			case n == 0:
				remove = "Delete " + w
			case n < 0:
				body += " What is in " + it + " can't be counted."
			default:
				body += " " + its + " " + plural(n, "uncommitted change") + "."
			}
			buttons, removes = []string{remove, "Keep " + w}, []bool{true, false}
		}
	}
	switch i := a.alert(c, &a.deleteOpen, head, body, append([]string{"Cancel"}, buttons...)...); {
	case i > 0:
		a.deleteTree(a.deleting, removes[i-1])
		a.deleting = thread{}
	case i == 0, i == ui.AlertDismissed:
		a.deleting = thread{}
	}
}

// branchOf is the branch checked out in a workspace, as git says.
var branchOf = func(dir string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "branch", "--show-current").Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) && len(exit.Stderr) > 0 {
		return "", errors.New(strings.TrimSpace(string(exit.Stderr)))
	}
	if err != nil {
		return "", err
	}
	b := strings.TrimSpace(string(out))
	if b == "" {
		return "", errors.New("the workspace is on no branch")
	}
	return b, nil
}

// editors are the editors Open in editor uses, the first found: their
// command-line launchers, which open a folder or a file.
var editors = []string{"cursor", "code", "zed"}

// editorCLI is the launcher of the first editor installed, or "" when none
// is, and the app offers no Open in editor.
var editorCLI = sync.OnceValue(func() string {
	for _, e := range editors {
		if p, err := exec.LookPath(e); err == nil {
			return p
		}
	}
	return ""
})

// startEditor opens path in the editor whose launcher is ed.
var startEditor = func(ed, path string) error { return exec.Command(ed, path).Start() }

func (a *app) openInEditor(c *ui.Context, ed, path string) {
	if err := startEditor(ed, path); err != nil {
		c.AddToast(ui.Toast{Title: "Couldn't open the editor", Description: err.Error(), Type: "info"})
	}
}
