package main

import (
	"slices"
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
// t needs them: its report says it waits on a request, which ends no turn,
// so nothing else tells of it. A child done or failed ended its turn, which
// notifyFinished told of already. Clicking it opens t, where the report
// is. Neither t nor the child is one they're watching. kids are t's
// children; asked, the requests they had pending at t's report before.
func (a *app) notifyReported(t thread, kids []thread, asked map[api.ID]bool) {
	if !a.settings.notifications || a.notify == nil || a.watching(t) {
		return
	}
	child, ok := blockedReport(t, kids, asked)
	if !ok || a.watching(child) {
		return
	}
	a.notify(mygo.NotificationOptions{
		ID:    "thread:" + t.id().String(),
		Title: t.View.Title,
		Body:  title(child) + ": Needs you",
		Group: "threads",
	})
}

// blockedReport is the child of t whose report to it, the last, says it
// waits on the user. The transcript says, when it's open and caught up.
// Else it's the child changed last of those with a request that wasn't
// pending at t's report before: a report of done or failed brings none.
func blockedReport(t thread, kids []thread, asked map[api.ID]bool) (thread, bool) {
	if tr := t.Transcript; tr != nil {
		for i := len(tr.Entries) - 1; i >= 0; i-- {
			if r, ok := tr.Entries[i].(*client.ReportEntry); ok {
				if !r.At.Equal(t.View.Reported) {
					break // the transcript is behind the view
				}
				th := t.env.Threads[r.Child]
				if th == nil || r.Kind != api.ReportBlocked {
					return thread{}, false
				}
				return thread{t.env, th}, true
			}
		}
	}
	var last thread
	for _, k := range kids {
		fresh := slices.ContainsFunc(k.View.Requests, func(r api.Request) bool { return !asked[r.ID] })
		if fresh && (last.Thread == nil || k.View.UpdatedAt.After(last.View.UpdatedAt)) {
			last = k
		}
	}
	return last, last.Thread != nil
}

// pending is the requests kids have pending.
func pending(kids []thread) map[api.ID]bool {
	var ids map[api.ID]bool
	for _, k := range kids {
		for _, r := range k.View.Requests {
			if ids == nil {
				ids = map[api.ID]bool{}
			}
			ids[r.ID] = true
		}
	}
	return ids
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
