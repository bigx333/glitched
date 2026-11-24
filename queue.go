package main

import (
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/plugins"
	"glitched/internal/plugins/widget"
)

// The queue over the composer lists the messages of yours that wait their
// turn, first first, the order they go in: each a row with what can be
// done with it, Send now, Edit, Move up, Move down and Remove, as T3 Code
// and Zeron give a queued row: icons but for Send now, which keeps its
// label while the messages have room for it. A row is a copy of
// composer.queue, which the built-in Queue draws from its natives.
// Glitched keeps the queue's card, its order and where it sits.

// waiting is the messages of the user's that wait their turn in t, in the
// order they take it: queued, or held by a stop or as a Defer. One on its
// way into the run waits no more.
func waiting(t thread) []*client.UserEntry {
	if t.Transcript == nil {
		return nil
	}
	var q []*client.UserEntry
	for _, u := range t.Transcript.Queue {
		if waits(u) {
			q = append(q, u)
		}
	}
	return q
}

// queueEdit is the queued message being edited in its row, and its words
// as edited so far.
type queueEdit struct {
	message api.ID
	text    string
}

// queueRow is the context of composer.queue: which message a copy is,
// where it is in the queue of count, whether a stop or a Defer holds it,
// and whether it's being edited. Never what it says.
type queueRow struct {
	Key     string `json:"key"`
	Index   int    `json:"index"`
	Count   int    `json:"count"`
	Held    bool   `json:"held,omitempty"`
	Editing bool   `json:"editing,omitempty"`
}

// queueRowNode is the built-in Queue's drawing of the row r, as its
// main.js draws it: the message, then its actions.
func queueRowNode(queueRow) *widget.Node {
	return &widget.Node{Type: "row", Gap: 2, Align: "center", Grow: true, Children: []*widget.Node{
		{Type: "native", ID: "text", Name: "queue.text", Grow: true},
		{Type: "native", ID: "steer", Name: "queue.steer"},
		{Type: "native", ID: "edit", Name: "queue.edit"},
		{Type: "native", ID: "up", Name: "queue.up"},
		{Type: "native", ID: "down", Name: "queue.down"},
		{Type: "native", ID: "remove", Name: "queue.remove"},
	}}
}

// canSteer is whether Send now has somewhere to send to: the agent's turn,
// which takes a message while it starts, runs or waits on the user, or
// the thread's next turn while none runs.
func canSteer(v api.Thread) bool {
	if !v.Live() {
		return v.Disposition.State != api.Settled
	}
	return !v.Silent() && (v.Run == api.RunStarting || v.Run == api.RunRunning || v.Run == api.RunWaiting)
}

// queueGap is the room between the queue and the composer card under it;
// queueMaxH the most it grows to, its rows scrolling past it.
const (
	queueGap  = 8
	queueMaxH = 168
)

// queueRoom is the room the queue takes over t's composer, as it was last
// laid out, for the timeline to leave under its end.
func (a *app) queueRoom(t thread) float32 {
	if len(waiting(t)) == 0 {
		return 0
	}
	return a.queueH + queueGap
}

// queueStrip draws the messages that wait their turn in t over the
// composer. An edit of one that went in meanwhile ends with it.
func (a *app) queueStrip(c *ui.Context, t thread) {
	q := waiting(t)
	if !slices.ContainsFunc(q, func(u *client.UserEntry) bool { return u.Message == a.queueEditing.message }) {
		a.queueEditing = queueEdit{}
	}
	if len(q) == 0 {
		return
	}
	ids := make([]api.ID, len(q))
	widest := float32(0)
	for i, u := range q {
		ids[i] = u.Message
		w, _ := c.MeasureText(0, ui.Span{Text: oneLine(u.Text), Size: 12})
		widest = max(widest, w)
	}
	moved := !slices.Equal(ids, a.queueOrder)
	a.queueOrder = ids
	// Send now keeps its label only while every message shows whole
	// beside it: the slot the messages had last frame, as it would be
	// with Send now an icon, less the label's width.
	label, _ := c.MeasureText(0, ui.Span{Text: "Send now", Size: 11})
	label += 4
	slot := a.queueSlotW
	if !a.queueCompact {
		slot += label
	}
	if compact := slot-label < widest; compact != a.queueCompact {
		a.queueCompact = compact
		c.Invalidate()
	}
	stateW := float32(0)
	for _, s := range []string{"Queued", "Held"} {
		w, _ := c.MeasureText(0, ui.Span{Text: s, Size: 11})
		stateW = max(stateW, w)
	}
	box := ui.Column(c).Key("queue").Radius(14).Border(1, palette.border).Padding(4).Margin(0, 0, queueGap).Label("Queued messages")
	if h := box.Bounds().H; h > 0 {
		a.queueH = h
	}
	a.frosted(c, box).Children(func() {
		ui.Scroll(c).Key("rows").Gap(2).MinHeight(0).MaxHeight(queueMaxH).Children(func() {
			for i, u := range q {
				a.queueRow(c, t, q, i, u, moved, stateW)
			}
		})
	})
}

// queueRow is the copy of composer.queue for q[i]. With the focus in it,
// it scrolls into view as the queue's order moved; its state, Queued or
// Held, takes stateW, so that the messages line up.
func (a *app) queueRow(c *ui.Context, t thread, q []*client.UserEntry, i int, u *client.UserEntry, moved bool, stateW float32) {
	editing := a.queueEditing.message == u.Message
	r := queueRow{Key: u.Message.String(), Index: i, Count: len(q), Held: u.State == client.Held, Editing: editing}
	move := func(before api.ID) { a.backend.MoveQueued(t.env, t.id(), u.Message, before) }
	a.pluginComponent(c, "composer.queue", &t, componentCopy{
		key: r.Key,
		box: func() ui.Element {
			e := ui.Row(c).Key(r.Key).AlignItems(ui.Center).Padding(2, 2, 2, 10).MinWidth(0)
			if moved && e.FocusWithin() {
				e.ScrollIntoView()
			}
			return e
		},
		context: func(plugins.ThreadRecord) any { return r },
		action: func(n *widget.Node) {
			switch n.Name {
			case "queue.text":
				a.queueText(c, t, u, r, stateW)
			case "queue.steer":
				steer := action
				if a.queueCompact {
					steer = a.queueIcon
				}
				if steer(c, "steer", icArrowUp, "Send now", !canSteer(t.View)).Clicked() {
					a.backend.SteerQueued(t.env, t.id(), u.Message)
				}
			case "queue.edit":
				if editing {
					if action(c, "save", icCheck, "Save", strings.TrimSpace(a.queueEditing.text) == "").Clicked() {
						a.saveQueueEdit(t, u)
					}
					if action(c, "cancel", icX, "Cancel", false).Clicked() {
						a.endQueueEdit(u)
					}
					break
				}
				edit := a.queueIcon(c, "edit", icPencil, "Edit", false)
				if a.queueRefocus == u.Message {
					edit.Bind(&a.queueEditButton)
					a.queueEditButton.Focus()
					a.queueRefocus = api.ID{}
				}
				if edit.Clicked() {
					a.queueEditing = queueEdit{message: u.Message, text: u.Text}
					a.queueArea.Focus()
				}
			case "queue.up":
				if i == 0 {
					queueSlot(c, "up", icChevronUp)
				} else if a.queueIcon(c, "up", icChevronUp, "Move up", false).Clicked() {
					move(q[i-1].Message)
				}
			case "queue.down":
				if i == len(q)-1 {
					queueSlot(c, "down", icChevronD)
				} else if a.queueIcon(c, "down", icChevronD, "Move down", false).Clicked() {
					var before api.ID
					if i+2 < len(q) {
						before = q[i+2].Message
					}
					move(before)
				}
			case "queue.remove":
				if a.queueIcon(c, "remove", icX, "Remove", false).Clicked() {
					a.backend.CancelQueued(t.env, t.id(), u.Message)
				}
			}
		},
		absent: func(n *widget.Node) bool {
			return editing && n.Name != "queue.text" && n.Name != "queue.edit"
		},
		builtin: func() *widget.Node { return queueRowNode(r) },
	})
}

// queueText is a queued message's words, on a line, after whether a stop
// holds it; or, being edited, the area its new words are written in,
// which ⌘⏎ saves and Escape leaves as they were.
func (a *app) queueText(c *ui.Context, t thread, u *client.UserEntry, r queueRow, stateW float32) {
	ui.Row(c).Key("text").Gap(8).AlignItems(ui.Center).Grow(1).Shrink(1).MinWidth(0).Children(func() {
		state := "Queued"
		if r.Held {
			state = "Held"
		}
		ui.Text(c, state).FontSize(11).TextColor(palette.faint).Shrink(0).MinWidth(stateW)
		if !r.Editing {
			text := ui.Text(c, oneLine(u.Text)).FontSize(12).TextColor(palette.text).SingleLine().Grow(1).Shrink(1).MinWidth(0)
			if w := text.Bounds().W; w > 0 && w != a.queueSlotW {
				a.queueSlotW = w
				c.Invalidate()
			}
			return
		}
		e := &a.queueEditing
		ui.TextArea(c.Key("edit"), &e.text).Bind(&a.queueArea).Label("Edit queued message").FontSize(12).Padding(4, 6).Grow(1).Shrink(1).MinWidth(0).MaxHeight(120)
		a.queueArea.OnShortcut(c, ui.Cmd, ui.KeyEnter, func() { a.saveQueueEdit(t, u) })
		a.queueArea.OnShortcut(c, 0, ui.KeyEscape, func() { a.endQueueEdit(u) })
	})
}

// oneLine is a queued message's words as its row shows them, on a line.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// queueIcon is one of a row's actions drawn as its icon alone, named
// label for assistive technology and in its tooltip.
func (a *app) queueIcon(c *ui.Context, key any, icon *ui.SVG, label string, disabled bool) ui.Element {
	return a.tip(c, ghost(c, key, false).Padding(2, 6).Label(label).Disabled(disabled), label).Children(func() {
		ui.Icon(c, icon).FontSize(11)
	})
}

// queueSlot keeps the room of a row's Move up or Move down where it has
// nowhere to move to, so that the actions line up across the rows.
func queueSlot(c *ui.Context, key any, icon *ui.SVG) {
	ui.Box(c.Key(key)).Padding(2, 6).Children(func() {
		ui.Icon(c, icon).FontSize(11).Opacity(0)
	})
}

// saveQueueEdit gives u the words written for it, if they say anything
// new, and ends the edit.
func (a *app) saveQueueEdit(t thread, u *client.UserEntry) {
	text := a.queueEditing.text
	a.endQueueEdit(u)
	if strings.TrimSpace(text) != "" && text != u.Text {
		a.backend.EditQueued(t.env, t.id(), u.Message, text)
	}
}

// endQueueEdit ends u's edit, and gives the focus back to its Edit, as
// a dialog gives it back to what opened it.
func (a *app) endQueueEdit(u *client.UserEntry) {
	a.queueEditing = queueEdit{}
	a.queueRefocus = u.Message
}
