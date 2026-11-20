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
// its list l. With fewer than two turns, there's nothing to navigate.
// Only the ticks the window can show are built, and, past them, where
// they would be, below the window, the tick that held the focus and its
// neighbours, and the last tick: Tab and Shift-Tab walk every tick, as
// with all of them built.
func (a *app) turnMarkers(c *ui.Context, id api.ID, rs rows, l *ui.ListState) {
	es, turns := rs.es, rs.turns
	if len(turns) < 2 {
		return
	}
	// The turn being read: the last that starts at or above the top of
	// what shows, or the last when the end shows.
	first, _ := l.Visible()
	active, found := slices.BinarySearchFunc(turns, first, func(tr turnInfo, row int) int { return cmp.Compare(tr.entry, row) })
	if !found {
		active = max(0, active-1)
	}
	if l.AtEnd() {
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
	// The ticks built: those that show, then, past them, the ones
	// around the focus and the last, which Shift-Tab comes to first.
	built := [][2]int{{0, shown}}
	if f, ok := a.markerFocus[id]; ok && f >= shown-1 && f < n {
		built = append(built, [2]int{max(shown, f-1), min(n, f+2)})
	}
	built = append(built, [2]int{max(shown, n-1), n})
	delete(a.markerFocus, id)
	col.Children(func() {
		pointed, pointedY := -1, float32(0)
		nearest := float32(math.MaxFloat32)
		tick := func(k int) {
			tr := turns[k]
			b := ui.ButtonBase(c.Key(es[tr.entry].key)).Padding(3, 6).Cursor(ui.CursorPointer).
				Label("Go to: " + preview(es[tr.entry]))
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
			if b.Focused() {
				a.markerFocus[id] = k
			}
			if b.Clicked() {
				l.ScrollTo(tr.entry, ui.Start)
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
