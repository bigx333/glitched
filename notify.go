package main

import (
	"strings"

	"github.com/egoist/mygo"

	"glitched/internal/api"
	"glitched/internal/client"
)

// notifyFinished tells the user, with a desktop notification, that the
// agent finished a thread they're not looking at: another thread, or the
// window in the background. Clicking it opens the thread. It needs the
// Notifications setting on, and an app MyGo packaged (mygo dev and mygo
// build), as notifications do on macOS. It runs on the main thread.
func (a *app) notifyFinished(t thread) {
	if !a.settings.notifications || a.win == nil {
		return
	}
	if a.selected == t.id() && a.win.IsFocused() {
		return // they're watching it
	}
	body := "Done. Your turn."
	if tr := t.Transcript; tr != nil {
		for i := len(tr.Entries) - 1; i >= 0; i-- {
			if e, ok := tr.Entries[i].(*client.AssistantEntry); ok && e.Text != "" {
				body = firstLine(e.Text)
				break
			}
		}
	}
	n := mygo.NewNotification(mygo.NotificationOptions{
		ID:    "thread:" + t.id().String(), // a later turn's replaces it
		Title: t.View.Title,
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
	s, ok := strings.CutPrefix(id, "thread:")
	thread, err := api.ParseID(s)
	if !ok || err != nil || a.win == nil {
		return
	}
	a.win.Update(func() {
		if t, ok := a.find(thread); ok {
			a.select_(t)
		}
	})
	// The click is on the main thread: a hide waiting on full screen goes
	// now, not in the update above, which would run after the show.
	a.cancelHide()
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

// plainText is Markdown as prose on one line, for a preview: its marks
// out, code blocks left out, lines joined by spaces.
func plainText(md string) string {
	var out []string
	fence := false
	for _, l := range strings.Split(md, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			fence = !fence
			continue
		}
		if fence || strings.HasPrefix(strings.TrimSpace(l), "|") {
			continue
		}
		l = strings.TrimSpace(strings.Trim(l, "#>*-` "))
		l = strings.NewReplacer("**", "", "`", "", "*", "").Replace(l)
		if l != "" {
			out = append(out, l)
		}
	}
	return strings.Join(out, " ")
}
