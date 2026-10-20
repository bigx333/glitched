package main

import (
	"strings"

	"github.com/egoist/mygo"
)

// notifyFinished tells the user, with a desktop notification, that the
// agent finished a thread they're not looking at: another thread, or the
// window in the background. Clicking it opens the thread. It needs the
// Notifications setting on, and an app MyGo packaged (mygo dev and mygo
// build), as notifications do on macOS. It runs on the main thread.
func (a *app) notifyFinished(t *thread) {
	if !a.settings.notifications || a.win == nil {
		return
	}
	if a.selected == t.id && a.win.IsFocused() {
		return // they're watching it
	}
	body := "Done. Your turn."
	for i := len(t.entries) - 1; i >= 0; i-- {
		if e := t.entries[i]; e.kind == entryAssistant && e.text != "" {
			body = firstLine(e.text)
			break
		}
	}
	n := mygo.NewNotification(mygo.NotificationOptions{
		ID:    "thread:" + t.id, // a later turn's replaces it
		Title: t.title,
		Body:  body,
		Group: "threads",
	})
	// Showing may wait on the user allowing notifications: off the main
	// thread.
	go func() {
		if mygo.NotificationsSupported() {
			_ = n.Show()
		}
	}()
}

// onNotificationClick opens the thread a notification is about.
func (a *app) onNotificationClick(id string) {
	threadID, ok := strings.CutPrefix(id, "thread:")
	if !ok || a.win == nil {
		return
	}
	a.win.Update(func() {
		for _, p := range a.projects {
			for _, t := range p.threads {
				if t.id == threadID {
					t.unread = false
					a.select_(t.id)
				}
			}
		}
	})
	a.win.Show()
	a.win.Focus()
}

// firstLine is a reply's first line of prose, without Markdown's marks,
// short enough for a notification.
func firstLine(md string) string {
	for _, l := range strings.Split(md, "\n") {
		l = strings.TrimSpace(strings.Trim(l, "#>*-` "))
		if l == "" {
			continue
		}
		l = strings.NewReplacer("**", "", "`", "", "*", "").Replace(l)
		if len(l) > 120 {
			l = l[:117] + "…"
		}
		return l
	}
	return ""
}
