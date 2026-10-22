package main

import (
	"strings"

	"github.com/egoist/mygo/ui"
)

// A thread row's context menu holds what is rarely done to a thread, so
// the row itself keeps to snoozing and settling. Rename edits the title
// in the row.

func (a *app) threadMenu(c *ui.Context, m *ui.Menu, t *thread, cat category) {
	if m.Item("Rename").Chosen() {
		a.renaming, a.renameDraft, a.renameFocused = t.id, t.title, false
	}
	switch cat {
	case catInbox, catWorking:
		working := cat == catWorking
		m.Submenu("Snooze", func(m *ui.Menu) {
			for _, s := range snoozeTimes {
				if m.Item(s.when).Chosen() {
					a.snooze(c, t, s.when, cat)
				}
			}
		}).Disabled(working)
		if m.Item("Mark settled").Disabled(working).Chosen() {
			a.settle(c, t, cat)
		}
	default:
		if m.Item("Back to inbox").Chosen() {
			t.settled, t.snoozed = false, ""
			a.gag(c, t, gagUndo, cat)
		}
	}
	m.Separator()
	if m.Item("Copy branch name").Chosen() {
		c.WriteClipboard(t.branch)
		c.AddToast(ui.Toast{Title: "Copied", Description: t.branch})
	}
	if m.Item("Open in editor").Chosen() {
		c.AddToast(ui.Toast{Title: "Opened in Cursor", Description: t.branch})
	}
	m.Separator()
	if m.Item("Archive").Chosen() {
		a.archive(t)
		c.AddToast(ui.Toast{Title: "Archived", Description: t.title})
	}
	if m.Item("Delete…").Chosen() {
		if a.settings.confirmDelete {
			a.deleting, a.deleteOpen = t, true
		} else {
			a.drop(t)
		}
	}
}

// renameInput is the row's title while it's being renamed: Enter keeps
// what was typed, as the focus going elsewhere does; Escape goes back.
func (a *app) renameInput(c *ui.Context, t *thread) {
	in := ui.TextInput(c.Key("rename"), &a.renameDraft).AutoFocus().FontSize(13).Grow(1).Shrink(1).
		Padding(2, 6).Radius(4).Label("Thread title")
	if in.Focused() {
		a.renameFocused = true
	}
	switch {
	case in.Shortcut(0, ui.KeyEscape):
		a.renaming = ""
	case in.Submitted(), a.renameFocused && !in.Focused():
		if s := strings.TrimSpace(a.renameDraft); s != "" {
			t.title = s
		}
		a.renaming = ""
	}
}

// settle puts the thread with the settled ones, done with.
func (a *app) settle(c *ui.Context, t *thread, from category) {
	t.settled, t.unread = true, false
	a.gag(c, t, gagSettle, from)
	c.AddToast(ui.Toast{Title: "Settled. Phew.", Description: t.title})
}

// archive moves the thread out of its project, to the archived ones,
// which remember where it came from.
func (a *app) archive(t *thread) {
	t.home, t.age = a.drop(t), "now"
	a.archived = append([]*thread{t}, a.archived...)
}

// drop takes the thread out of its project, which it returns, and ends
// any turn the agent was taking on it. Was it the one open, the first
// thread left opens, or a new one where none is.
func (a *app) drop(t *thread) *project {
	t.turn.Add(1)
	var home *project
	for _, p := range a.projects {
		if n := len(p.threads); n > 0 {
			p.threads = remove(p.threads, t)
			if len(p.threads) < n {
				home = p
			}
		}
	}
	if a.selected != t.id {
		return home
	}
	for _, p := range a.projects {
		if len(p.threads) > 0 {
			a.select_(p.threads[0].id)
			return home
		}
	}
	a.startThread(home)
	return home
}

// deleteAlert asks before a thread is deleted, when the settings say to.
func (a *app) deleteAlert(c *ui.Context) {
	if a.deleting == nil {
		return
	}
	switch ui.AlertDialog(c, &a.deleteOpen, "Delete “"+a.deleting.title+"”?",
		"The thread and its messages go away. You can't undo this.", "Cancel", "Delete") {
	case 1:
		a.drop(a.deleting)
		a.deleting = nil
	case 0, ui.AlertDismissed:
		a.deleting = nil
	}
}
