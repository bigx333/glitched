package main

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"
)

// Pointing at a message shows what can be done with it, in a toolbar
// that floats under it: copying it, sending it again edited, retrying the
// reply, or forking the thread there. The toolbar takes no room, so
// pointing at a message moves nothing.

// messageActions is the toolbar under the message at i, at the edge the
// message is on, while the message is pointed at or the toolbar itself
// is. It touches the message, so the pointer can reach it. Changing the
// thread waits while the agent works.
func (a *app) messageActions(c *ui.Context, t *thread, i int, e entry, pointed bool) {
	busy := t.status == statusWorking
	bar := ui.Row(c).Key("actions").Absolute().Bottom(-22).Padding(2, 0, 0).Gap(2)
	if e.kind == entryUser {
		bar.Right(0)
	} else {
		bar.Left(0)
	}
	if !pointed && !bar.Hovered() {
		bar.Invisible()
	}
	bar.Children(func() {
		if action(c, "copy", icCopy, "Copy", false).Clicked() {
			c.WriteClipboard(e.text)
			c.AddToast(ui.Toast{Title: "Copied"})
		}
		switch e.kind {
		case entryUser:
			if action(c, "edit", icPencil, "Edit & resend", busy).Clicked() {
				a.draft = e.text
				t.entries = t.entries[:i]
			}
		case entryAssistant:
			if action(c, "retry", icRotate, "Retry", busy).Clicked() {
				a.retry(t, i)
			}
		}
		if action(c, "fork", icFork, "Fork here", busy).Clicked() {
			a.fork(t, i)
		}
	})
}

func action(c *ui.Context, key any, icon *ui.SVG, label string, disabled bool) ui.Element {
	return ghost(c, key, false).Padding(2, 6).Gap(4).Disabled(disabled).Children(func() {
		ui.Icon(c, icon).FontSize(11)
		ui.Text(c, label).FontSize(11)
	})
}

// retry drops the reply at i and the work before it, back to the message
// it answered, and has the agent answer that again.
func (a *app) retry(t *thread, i int) {
	for i > 0 && t.entries[i-1].kind != entryUser {
		i--
	}
	t.entries = t.entries[:i]
	a.run(t)
}

// fork starts a thread of the thread's messages up to i, in its project,
// and opens it.
func (a *app) fork(t *thread, i int) {
	p, _ := a.current()
	f := &thread{
		id:      fmt.Sprintf("%s-%s", t.id, time.Now().Format("150405.000")),
		title:   t.title + " (fork)",
		age:     "now",
		branch:  t.branch,
		entries: append([]entry(nil), t.entries[:i+1]...),
	}
	p.threads = append([]*thread{f}, p.threads...)
	a.select_(f.id)
}
