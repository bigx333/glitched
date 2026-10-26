package main

import (
	"errors"
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
	m.Separator()
	// The engine deletes no live thread, an uncertain run's neither.
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

// settle puts the thread with the settled ones, done with.
func (a *app) settle(c *ui.Context, t thread, from category) {
	a.backend.SetDisposition(t.env, t.id(), api.Disposition{State: api.Settled})
	a.backend.MarkRead(t.Thread)
	a.gag(c, t.id(), gagSettle, from)
	c.AddToast(ui.Toast{Title: "Settled. Phew.", Description: title(t)})
}

// askDelete deletes the thread, asking first while the settings say to.
func (a *app) askDelete(t thread) {
	if a.settings.confirmDelete {
		a.deleting, a.deleteOpen = t, true
		return
	}
	a.delete(t)
}

// delete deletes the thread. The chat shows it until the delete lands, so
// a refused one leaves it shown with why, and then shows a new thread in
// its workspace.
func (a *app) delete(t thread) {
	a.backend.Delete(t.env, t.id())
	if a.selected == t.id() {
		a.fresh = draft{t.env, t.View.Workspace}
	}
}

// deleteAlert asks before a thread is deleted, when the settings say to.
// Deleting leaves a tombstone, not erasure, so it promises no more.
func (a *app) deleteAlert(c *ui.Context) {
	if a.deleting.Thread == nil {
		return
	}
	switch ui.AlertDialog(c, &a.deleteOpen, "Delete “"+title(a.deleting)+"”?",
		"The thread is removed from every list and can't be brought back. Its history, messages included, stays in the engine's log.", "Cancel", "Delete") {
	case 1:
		a.delete(a.deleting)
		a.deleting = thread{}
	case 0, ui.AlertDismissed:
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
