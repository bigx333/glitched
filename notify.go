package main

import (
	"strings"

	"github.com/egoist/mygo"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/plain"
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

// notifyAsked tells the user, as notifyFinished does, that child waits on
// them for a request: that ends no turn, so nothing else tells of it. A
// child of a parent is told of on the parent, where its report is, as
// Needs you; a thread of the user's, a zero parent, on itself, with what
// it asks. Clicking it opens that thread. None is one they're watching.
func (a *app) notifyAsked(e *client.Env, parent, child api.ID) {
	c := e.Threads[child]
	if c == nil || !a.settings.notifications || a.notify == nil || a.watching(thread{e, c}) {
		return
	}
	if parent.IsZero() {
		if n := len(c.View.Requests); n > 0 {
			a.notify(mygo.NotificationOptions{
				ID:    "thread:" + child.String(),
				Title: c.View.Title,
				Body:  askBody(agentName(thread{e, c}), c.View.Requests[n-1]),
				Group: "threads",
			})
		}
		return
	}
	p := e.Threads[parent]
	if p == nil || a.watching(thread{e, p}) {
		return
	}
	a.notify(mygo.NotificationOptions{
		ID:    "thread:" + parent.String(),
		Title: p.View.Title,
		Body:  title(thread{e, c}) + ": Needs you",
		Group: "threads",
	})
}

// askBody is what a request asks, in a notification's line, its agent's
// words as printable ASCII: "Claude asks: …", or what it wants to do.
func askBody(agent string, r api.Request) string {
	line := func(s string) string {
		s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
		if r := []rune(s); len(r) > 120 {
			s = string(r[:117]) + "..."
		}
		s, _ = plain.Text(s, false)
		return s
	}
	if q, err := api.ParseQuestions(r.Body); err == nil {
		return agent + " asks: " + line(q.Questions[0].Text)
	}
	b, ok := askOf(r.Body)
	switch {
	case !ok:
		return agent + " needs you"
	case b.Command != "":
		return agent + " wants to run " + line(b.Command)
	}
	return strings.TrimSpace(agent + " wants to " + line(b.Operation) + " " + line(b.Path))
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
