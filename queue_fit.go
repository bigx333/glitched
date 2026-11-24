package main

import (
	"github.com/egoist/mygo/ui"

	"glitched/internal/client"
)

// queueCompact is whether this frame draws the queue's Send nows as their
// icon, named and with its tooltip. They keep their label only in
// Glitched's own row in its own composer, whose widths are known before
// it's laid out, and only while every message being shown fits beside
// it: in the row's room this frame, less its state, its actions with Send
// now labelled, and the gaps between them. A row being edited shows no
// message and no Send now. A plugin's row, or the built-in's in a
// plugin's composer, lays itself out as it likes, so Send now is an icon
// there.
func (a *app) queueCompact(c *ui.Context, q []*client.UserEntry, stateW float32) bool {
	if !a.ownLayout("composer.queue") || !a.ownLayout("composer") {
		return true
	}
	label, _ := c.MeasureText(0, ui.Span{Text: "Send now", Size: actionSize})
	actions := 5*queueIconW + actionGap + label + 5*queueActionGap
	room := a.queueWidth() - 2*(queueBorder+queuePad) - queueRowPadL - queueRowPadR - stateW - queueTextGap - actions
	for _, u := range q {
		if u.Message == a.queueEditing.message {
			continue
		}
		if w, _ := c.MeasureText(0, ui.Span{Text: oneLine(u.Text), Size: queueTextSize}); w > room {
			return true
		}
	}
	return false
}

// ownLayout is whether Glitched lays out the component name: no plugin
// of the user's replaces it, only a built-in one, which draws it as
// Glitched does.
func (a *app) ownLayout(name string) bool {
	if a.plugins == nil {
		return true
	}
	for _, pl := range a.plugins.Replacers(name) {
		if s := a.plugins.App(pl.Plugin); s != nil && !s.Builtin {
			return false
		}
	}
	return true
}

// queueWidth is the queue's width in this frame: the composer's, in the
// room the chat has in this frame, as composer lays it out.
func (a *app) queueWidth() float32 {
	frame := min(a.contentInner(a.chatRoom())-a.composerLeft()-columnRight, a.composerMaxW())
	return frame - a.composerAround.w
}
