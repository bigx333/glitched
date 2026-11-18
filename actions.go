package main

import (
	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
)

// Pointing at a message shows what can be done with it, in a toolbar
// that floats under it: copying it, putting it back in the composer to
// edit and send again, or having the agent answer the message again. The
// toolbar takes no room, so pointing at a message moves nothing.

// messageActions is the toolbar under the message es[i], at the edge the
// message is on, while the message is pointed at or the toolbar itself
// is. It touches the message, so the pointer can reach it.
func (a *app) messageActions(c *ui.Context, t thread, es []entry, i int, pointed bool) {
	e := es[i]
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
			if action(c, "edit", icPencil, "Edit & resend", false).Clicked() {
				a.draft, a.code, a.composerOpened = e.text, codeBlock{}, true
			}
		case entryAssistant:
			asked, ok := askedBefore(es, i)
			if action(c, "retry", icRotate, "Retry", !ok || t.View.Live()).Clicked() {
				a.backend.Send(t.env, t.id(), asked, api.SteerActive)
				a.listFor(t.id()).ScrollToEnd()
			}
		}
	})
}

func action(c *ui.Context, key any, icon *ui.SVG, label string, disabled bool) ui.Element {
	return ghost(c, key, false).Padding(2, 6).Gap(4).Disabled(disabled).Children(func() {
		ui.Icon(c, icon).FontSize(11)
		ui.Text(c, label).FontSize(11)
	})
}

// askedBefore is the message the reply es[i] answers: the last one sent
// before it.
func askedBefore(es []entry, i int) (string, bool) {
	for ; i >= 0; i-- {
		if es[i].kind == entryUser && es[i].failed.Err == nil {
			return es[i].text, true
		}
	}
	return "", false
}
