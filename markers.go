package main

import (
	"time"

	"github.com/egoist/mygo/ui"
)

// Turn markers run down the chat's edge, as Codex's and T3 Code's do: a
// tick for each message the user sent, the one being read brighter.
// Pointing at a tick widens it and shows the message; a click scrolls
// the timeline to it. They're used often, so they move little: a hover's
// widening, 150ms, and none for the highlight.

const (
	markerW, markerWide = 18, 26
	markerGap           = 10
)

// turnMarkers draws the thread's markers over the chat's left edge, for
// its list l. With fewer than two turns, there's nothing to navigate.
func (a *app) turnMarkers(c *ui.Context, t *thread, l *ui.ListState) {
	var turns []int // the entries the user sent
	for i, e := range t.entries {
		if e.kind == entryUser {
			turns = append(turns, i)
		}
	}
	if len(turns) < 2 {
		return
	}
	// The turn being read: the last that starts at or above the top of
	// what shows, or the last when the end shows.
	active := 0
	first, last := l.Visible()
	for k, i := range turns {
		if i <= first || (last >= first && i <= last && k == 0) {
			active = k
		}
	}
	if l.AtEnd() {
		active = len(turns) - 1
	}
	ui.Column(c).Key("markers").Absolute().Left(4).Top(headerHeight).Bottom(composerRoom - 40).
		Justify(ui.Center).AlignItems(ui.Start).Gap(markerGap).Children(func() {
		for k, i := range turns {
			e := t.entries[i]
			b := ui.ButtonBase(c.Key(i)).Padding(3, 6).Cursor(ui.CursorPointer).Label("Go to: "+preview(e)).
				Tooltip(preview(e))
			hovered := b.Hovered()
			b.Children(func() {
				w := float32(markerW)
				if hovered {
					w = markerWide
				}
				tick := ui.Box(c).Height(3).Radius(2)
				tick.Width(tick.AnimateTo("w", w, ui.Animation{Duration: 150 * time.Millisecond, Ease: easeOut}))
				switch {
				case k == active:
					tick.Background(palette.text.Alpha(0.85))
				case hovered:
					tick.Background(palette.text.Alpha(0.5))
				default:
					tick.Background(palette.text.Alpha(0.18))
				}
			})
			if b.Clicked() {
				l.ScrollTo(i, ui.Start)
			}
		}
	})
}

// preview is a marker's tooltip: the message's first line, or its
// attachments'.
func preview(e entry) string {
	if s := firstLine(e.text); s != "" {
		return s
	}
	if len(e.attachments) > 0 {
		return e.attachments[0].name
	}
	return "Message"
}
