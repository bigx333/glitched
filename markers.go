package main

import (
	"fmt"
	"math"
	"time"

	"github.com/egoist/mygo/ui"
)

// Turn markers run down the chat's edge, as Codex's and T3 Code's do: a
// tick for each message the user sent, the one being read brighter. As in
// T3 Code, the ticks rise toward the pointer, the nearest widest, its
// neighbours less the farther they are; as in Zeron, pointing at one
// shows a card beside it, the message and the start of its reply, and a
// click scrolls the timeline to it.

const (
	markerW, markerMax = 14, 34 // a tick's width at rest, and at the pointer
	markerReach        = 56     // how far from the pointer ticks rise, DIPs
	markerGap          = 9      // between the ticks' hit areas
	markerCardW        = 340    // the preview card's width
	markerLeft         = 4      // the column's left edge, in the pane
	markerColumnW      = markerMax + 12
)

type turnInfo struct {
	entry int // the user's message
	reply string
	steps int
}

// turnsOf is the thread's turns: each message the user sent, with the
// first reply after it and the steps the agent took for it.
func turnsOf(t *thread) []turnInfo {
	var turns []turnInfo
	for i, e := range t.entries {
		switch {
		case e.kind == entryUser:
			turns = append(turns, turnInfo{entry: i})
		case len(turns) == 0:
		case e.kind == entryAssistant && turns[len(turns)-1].reply == "":
			turns[len(turns)-1].reply = plainText(e.text)
		case e.kind == entryWork:
			turns[len(turns)-1].steps += len(e.work)
		}
	}
	return turns
}

// turnMarkers draws the thread's markers over the chat's left edge, for
// its list l. With fewer than two turns, there's nothing to navigate.
func (a *app) turnMarkers(c *ui.Context, t *thread, l *ui.ListState) {
	turns := turnsOf(t)
	if len(turns) < 2 {
		return
	}
	// The turn being read: the last that starts at or above the top of
	// what shows, or the last when the end shows.
	active := 0
	first, _ := l.Visible()
	for k, tr := range turns {
		if tr.entry <= first {
			active = k
		}
	}
	if l.AtEnd() {
		active = len(turns) - 1
	}

	col := ui.Column(c).Key("markers").Absolute().Left(markerLeft).Top(0).Bottom(composerRoom - 40).
		Width(markerColumnW).Justify(ui.Center).AlignItems(ui.Start).Gap(markerGap)
	// Where the pointer is along the column, if over it: the ticks rise
	// toward it.
	_, py, over := col.PointerPosition()
	colBox := col.Bounds()
	col.Children(func() {
		pointed, pointedY := -1, float32(0)
		nearest := float32(math.MaxFloat32)
		for k, tr := range turns {
			b := ui.ButtonBase(c.Key(tr.entry)).Padding(3, 6).Cursor(ui.CursorPointer).
				Label("Go to: " + preview(t.entries[tr.entry]))
			box := b.Bounds()
			cy := box.Y + box.H/2 - colBox.Y // the tick's middle, in the column
			w := float32(markerW)
			if over {
				d := float32(math.Abs(float64(cy - py)))
				if d < nearest {
					nearest, pointed, pointedY = d, k, cy
				}
				f := max(0, 1-d/markerReach)
				w += (markerMax - markerW) * f * f
			}
			b.Children(func() {
				tick := ui.Box(c).Height(3).Radius(2)
				tick.Width(tick.AnimateTo("w", w, ui.Animation{Duration: 120 * time.Millisecond, Ease: easeOut}))
				switch {
				case k == active:
					tick.Background(palette.text.Alpha(0.85))
				case over && k == pointed:
					tick.Background(palette.text.Alpha(0.6))
				default:
					tick.Background(palette.text.Alpha(0.2))
				}
			})
			if b.Clicked() {
				l.ScrollTo(tr.entry, ui.Start)
			}
		}
		if over && pointed >= 0 {
			a.turnCard(c, t, turns, pointed, pointedY)
		}
	})
}

// turnCard previews a turn beside its tick, as Zeron's does: the message,
// the start of its reply, and where it is among the turns. It follows the
// pointer from tick to tick, sliding, and comes and goes as a popover.
func (a *app) turnCard(c *ui.Context, t *thread, turns []turnInfo, k int, tickY float32) {
	tr := turns[k]
	card := ui.Column(c).Key("card").Absolute().Left(markerColumnW+6).Top(tickY-34).Width(markerCardW).
		Padding(14, 16).Gap(8).Radius(16).Background(palette.surface).Border(1, palette.border).
		Shadow(0, 12, 32, 0, ui.RGBA(0, 0, 0, 0.35)).PassThrough().
		TransformOrigin(0, 0.5).
		Transition(ui.ElementTransition{
			Position: true, Colors: true,
			Duration: 150 * time.Millisecond, Ease: easeOut,
			Enter: &ui.Motion{Scale: 0.97, X: -4},
			Exit:  &ui.Motion{Scale: 0.97, X: -4},
		})
	card.Children(func() {
		e := t.entries[tr.entry]
		msg := e.text
		if msg == "" {
			msg = preview(e)
		}
		ui.Text(c, msg).FontSize(13.5).FontWeight(500).LineHeight(1.4).MaxLines(3).TextColor(palette.text)
		if tr.reply != "" {
			ui.Text(c, tr.reply).FontSize(12.5).LineHeight(1.45).MaxLines(4).TextColor(palette.muted)
		}
		meta := fmt.Sprintf("Turn %d of %d", k+1, len(turns))
		if tr.steps > 0 {
			meta += fmt.Sprintf(" · %d steps", tr.steps)
		}
		ui.Text(c, meta).FontSize(11.5).TextColor(palette.faint)
	})
}

// preview is a message's first line, or its attachments'.
func preview(e entry) string {
	if s := firstLine(e.text); s != "" {
		return s
	}
	if len(e.attachments) > 0 {
		return e.attachments[0].name
	}
	return "Message"
}
