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

// notifyReported tells the user, as notifyFinished does, that a child of
// t reported to it: done, failed, or needing them. Clicking it opens t,
// where the report is. Neither t nor the child is one they're watching.
func (a *app) notifyReported(t thread) {
	if !a.settings.notifications || a.notify == nil || a.watching(t) {
		return
	}
	child, kind, ok := a.lastReport(t)
	if !ok || a.watching(child) {
		return
	}
	state := map[api.ReportKind]string{api.ReportDone: "Done", api.ReportFailed: "Failed", api.ReportBlocked: "Needs you"}[kind]
	a.notify(mygo.NotificationOptions{
		ID:    "thread:" + t.id().String(),
		Title: t.View.Title,
		Body:  title(child) + ": " + state,
		Group: "threads",
	})
}

// lastReport is the child of t that reported to it last, and how. The
// transcript says, when it's open and caught up; else it's the child
// changed last of those that could have reported: one waiting on the user,
// or one whose turn ended by the report.
func (a *app) lastReport(t thread) (thread, api.ReportKind, bool) {
	if tr := t.Transcript; tr != nil {
		for i := len(tr.Entries) - 1; i >= 0; i-- {
			if r, ok := tr.Entries[i].(*client.ReportEntry); ok {
				if !r.At.Equal(t.View.Reported) {
					break // the transcript is behind the view
				}
				th := t.env.Threads[r.Child]
				if th == nil {
					return thread{}, 0, false
				}
				return thread{t.env, th}, r.Kind, true
			}
		}
	}
	var last thread
	for _, k := range a.kidsOf()[t.id()] {
		v := k.View
		could := len(v.Requests) > 0 || (!v.Live() && v.LastTurn != nil && !v.LastTurn.Ended.After(t.View.Reported))
		if could && (last.Thread == nil || v.UpdatedAt.After(last.View.UpdatedAt)) {
			last = k
		}
	}
	switch {
	case last.Thread == nil:
		return thread{}, 0, false
	case len(last.View.Requests) > 0:
		return last, api.ReportBlocked, true
	case last.View.LastTurn.Outcome == api.OutcomeFailed:
		return last, api.ReportFailed, true
	}
	return last, api.ReportDone, true
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
