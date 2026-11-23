package main

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
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
		if a.ownsWorktree(t) {
			a.deleteChanges = changesIn(t)
		}
		return
	}
	a.deleteTree(t, false)
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
	if tree := append(a.below(a.deleting), node{a.deleting, 0}); a.ownsWorktree(a.deleting) {
		body += "\n\nIts worktree on " + a.deleting.View.Branch + " stays unless you delete it too. The branch stays while it has commits of its own."
		buttons = []string{"Keep worktree"}
		if working(tree) > 0 {
			body += " To delete the worktree, stop the work first."
		} else {
			remove := "Delete worktree and changes"
			body += " Deleting it deletes everything in it, files git ignores included."
			switch n := a.deleteChanges; {
			case n == 0:
				remove = "Delete worktree"
			case n < 0:
				body += " What is in it can't be counted."
			default:
				body += " It has " + plural(n, "uncommitted change") + "."
			}
			buttons, removes = []string{remove, "Keep worktree"}, []bool{true, false}
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
