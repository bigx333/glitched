package main

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
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
	steps int
}

// turnsOf is a thread's turns: each message the user sent, with the
// steps the agent took for it. It reads no message's text: turnReply
// does, for the one turn a card shows. rowsOf keeps them as the thread
// grows.
func turnsOf(es []entry) []turnInfo {
	turns, _ := addTurns(nil, 0, es, 0)
	return turns
}

// markerAt is a turn's marker: its message's key, and the turn's index
// when it was drawn.
type markerAt struct {
	key  string
	turn int
}

// markerState is what a thread's markers keep from frame to frame: the
// marker that had the focus last.
type markerState struct {
	focus   markerAt
	focused bool
}

// markerTicks are the ticks the window's markers built last, of a thread,
// in order, with a handle to each to ask whether it has the focus. The
// handles are bound again from frame to frame, whichever thread shows, as
// many as ticks are built.
type markerTicks struct {
	thread  api.ID
	built   []markerAt
	handles []*ui.Handle
}

// markerPitch is how far apart the ticks are: a tick's hit area, its
// padding above and below a 3 DIP tick, and the gap to the next.
const markerPitch = 3 + 2*3 + markerGap

// markersShown is how many of n ticks a window h DIPs tall can show. The
// column centers its ticks while they fit and starts them at its top when
// they don't, as MyGo's flex layout gives an overflowing column no free
// room: the ticks past the window's height never show, and leaving them
// out leaves it overflowing. One more is built for Tab to move to.
func markersShown(n int, h float32) int {
	return min(n, int(h/markerPitch)+2)
}

// turnMarkers draws the thread's markers over the chat's left edge, for
// its list l of what v lists. With fewer than two turns, there's nothing
// to navigate.
// Only the ticks the window can show are built, and, past them, where
// they would be, below the window, the tick that held the focus and its
// neighbours, and the last tick: Tab and Shift-Tab walk every tick, as
// with all of them built.
func (a *app) turnMarkers(c *ui.Context, id api.ID, rs rows, l *ui.ListState, v timelineView) {
	es, turns := rs.es, rs.turns
	if len(turns) < 2 {
		return
	}
	// The turn being read: the last that starts at or above the top of
	// what shows, or the last when the end shows.
	// Past the items, as the rows shrink while scrolled up, it's the last.
	top, _ := l.Visible()
	switch {
	case top >= v.len():
		top = len(es)
	case top >= 0:
		top = v.anchor(top)
	}
	active, found := slices.BinarySearchFunc(turns, top, func(tr turnInfo, row int) int { return cmp.Compare(tr.entry, row) })
	if !found {
		active = max(0, active-1)
	}
	if a.atEnd(id) {
		active = len(turns) - 1
	}

	col := ui.Column(c).Key("markers").Absolute().Left(markerLeft).Top(0).Bottom(composerRoom - 40).
		Width(markerColumnW).Justify(ui.Center).AlignItems(ui.Start).Gap(markerGap).Opacity(transcriptK(a.dockK))
	// Where the pointer is along the column, if over it: the ticks rise
	// toward it.
	_, py, over := col.PointerPosition()
	colBox := col.Bounds()
	_, h := c.Size()
	n := len(turns)
	shown := markersShown(n, h)
	// Where the focus is. A key moves it among the ticks the last frame
	// built, before this one builds, so they're asked, whether or not the
	// window has the keyboard. When none has it, the last that had it is
	// built still, for the focus to come back to, as from another thread.
	m := a.markers[id]
	if m == nil {
		m = &markerState{}
		a.markers[id] = m
	}
	ts := &a.markerTicks
	for i, at := range ts.built {
		if ts.thread == id && ts.handles[i].FocusWithin(c) {
			m.focus, m.focused = at, true
			break
		}
	}
	// The ticks built: those that show, then, past them, two either side
	// of the focus, for keys that come before a frame does, and the last
	// three, which Shift-Tab comes to first.
	built := [][2]int{{0, shown}}
	if m.focused {
		// Where it was, unless messages came before it; gone with its
		// message.
		f := m.focus.turn
		if f >= n || es[turns[f].entry].key != m.focus.key {
			f = slices.IndexFunc(turns, func(tr turnInfo) bool { return es[tr.entry].key == m.focus.key })
			m.focus.turn, m.focused = f, f >= 0
		}
		if f >= shown-2 {
			built = append(built, [2]int{max(shown, f-2), min(n, f+3)})
		}
	}
	built = append(built, [2]int{max(shown, n-3), n})
	ts.thread, ts.built = id, ts.built[:0]
	col.Children(func() {
		pointed, pointedY := -1, float32(0)
		nearest := float32(math.MaxFloat32)
		tick := func(k int) {
			tr := turns[k]
			key := es[tr.entry].key
			if len(ts.built) == len(ts.handles) {
				ts.handles = append(ts.handles, &ui.Handle{})
			}
			h := ts.handles[len(ts.built)]
			ts.built = append(ts.built, markerAt{key, k})
			b := ui.ButtonBase(c.Key(key)).Padding(3, 6).Cursor(ui.CursorPointer).
				Label("Go to: " + preview(es[tr.entry])).Bind(h)
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
				if j := v.pos(tr.entry); j >= 0 {
					l.ScrollTo(j, ui.Start)
				}
			}
		}
		k := 0
		for _, r := range built {
			if r[0] > k {
				// The ticks skipped, with the gaps around them but one.
				ui.Box(c.Key(fmt.Sprint("skipped", r[0]))).Height(float32((r[0]-k)*markerPitch - markerGap))
				k = r[0]
			}
			for ; k < r[1]; k++ {
				tick(k)
			}
		}
		if over && pointed >= 0 {
			a.turnCard(c, es, turns, pointed, pointedY)
		}
	})
}

// turnCard previews a turn beside its tick, as Zeron's does: the message,
// the start of its reply, and where it is among the turns. It follows the
// pointer from tick to tick, sliding, and comes and goes as a popover.
func (a *app) turnCard(c *ui.Context, es []entry, turns []turnInfo, k int, tickY float32) {
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
	a.cover(coverKey{kind: "turn"}, card)
	card.Children(func() {
		ui.Text(c, es[tr.entry].text).FontSize(13.5).FontWeight(500).LineHeight(1.4).MaxLines(3).TextColor(palette.text)
		if reply := turnReply(es, tr); reply != "" {
			ui.Text(c, reply).FontSize(12.5).LineHeight(1.45).MaxLines(4).TextColor(palette.muted)
		}
		meta := fmt.Sprintf("Turn %d of %d", k+1, len(turns))
		if tr.steps > 0 {
			meta += fmt.Sprintf(" · %d steps", tr.steps)
		}
		ui.Text(c, meta).FontSize(11.5).TextColor(palette.faint)
	})
}

// turnReply is the start of a turn's reply: its first that says
// something as prose.
func turnReply(es []entry, tr turnInfo) string {
	for _, e := range es[tr.entry+1:] {
		if e.kind == entryUser {
			break
		}
		if e.kind == entryAssistant {
			if p := plainText(e.text); p != "" {
				return p
			}
		}
	}
	return ""
}

// preview is a message's first line.
func preview(e entry) string {
	if s := firstLine(e.text); s != "" {
		return s
	}
	return "Message"
}
