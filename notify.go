package main

import (
	"slices"
	"strings"
	"time"

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
	if !a.settings.notifications || a.notify == nil || a.watching(t) {
		return
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
	a.notify(mygo.NotificationOptions{
		ID:    "thread:" + t.id().String(), // a later turn's replaces it
		Title: t.View.Title,
		Body:  body,
		Group: "threads",
	})
}

// watching is whether the user looks at the thread: the chat shows it, in
// the window in front.
func (a *app) watching(t thread) bool {
	return a.selected == t.id() && a.win != nil && a.win.IsFocused()
}

// notifyReported tells the user, as notifyFinished does, of t's last
// report, when it says a child waits on them: a request ends no turn, so
// nothing else tells of it. A child done or failed ended its turn, which
// notifyFinished told of already. t's transcript holds the report when it
// has caught up; else the manager reads it.
func (a *app) notifyReported(t thread) {
	if !a.settings.notifications || a.notify == nil || a.watching(t) {
		return
	}
	at := t.View.Reported
	if r, ok := reportAt(t.Transcript, at); ok {
		a.notifyBlocked(t, r)
		return
	}
	a.backend.Report(t.env, t.id(), at, func(r api.ChildReported) { a.notifyBlocked(t, r) })
}

// notifyBlocked tells of r, a report to t, when it says its child waits on
// the user. Clicking it opens t, where the report is. Neither t nor the
// child is one they're watching.
func (a *app) notifyBlocked(t thread, r api.ChildReported) {
	child := thread{t.env, t.env.Threads[r.Child]}
	if r.Kind != api.ReportBlocked || child.Thread == nil || !a.settings.notifications || a.notify == nil || a.watching(t) || a.watching(child) {
		return
	}
	a.notify(mygo.NotificationOptions{
		ID:    "thread:" + t.id().String(),
		Title: t.View.Title,
		Body:  title(child) + ": Needs you",
		Group: "threads",
	})
}

// reportAt is the report at at in tr, when tr holds it.
func reportAt(tr *client.Transcript, at time.Time) (api.ChildReported, bool) {
	if tr != nil {
		for _, e := range slices.Backward(tr.Entries) {
			if r, ok := e.(*client.ReportEntry); ok && !r.At.After(at) {
				return r.ChildReported, r.At.Equal(at)
			}
		}
	}
	return api.ChildReported{}, false
}

// showNotification shows a desktop notification. Showing may wait on the
// user allowing notifications: off the main thread.
func showNotification(o mygo.NotificationOptions) {
	n := mygo.NewNotification(o)
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
