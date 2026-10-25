package main

import (
	"time"

	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
)

// The Agents strip is Cursor's, over the composer of a thread with
// children: closed, a small Agents pill; open, a compact panel of the
// threads under this one, a dot of each one's run, and, pointed at, + for
// a child of its own and … for the thread menu. Past five, More shows the
// rest.
//
// The pill and the panel are one element, which grows from the pill up
// out of the composer's edge into the panel, as wide and as round as the
// composer card: the pill's label dissolves as it starts, then the head and
// the rows rise in, one after another; closing folds it back the other way.
// The pill sits in the layout; once it starts growing it's an overlay held
// to the pill's place, floating over the chat with a shadow that deepens
// as it opens, which Escape, a press outside it, or going to another
// thread closes.

// agentsShown is how many rows the agents panel shows before More.
const agentsShown = 5

// The agents strip's sizes: the pill's height and a guess at its width
// before its label is measured; the panel's rows and head, its padding,
// and its radius, the composer card's, whose edges it shares.
const (
	agentsPillH     = 26
	agentsPillGuess = 76
	agentsRowH      = 26
	agentsHeadH     = 28
	agentsPad       = 6
	agentsRadius    = 20
	// agentsInset puts the panel's text where the composer's starts: its
	// border, its padding and this make the composer text's 16.
	agentsInset = 16 - 1 - agentsPad
)

var (
	// agentsGrow grows the Agents pill into its panel; agentsFold folds it
	// back, a little quicker, as what goes away should.
	agentsGrow = ui.Animation{Duration: 240 * time.Millisecond, Ease: easeOut}
	agentsFold = ui.Animation{Duration: 180 * time.Millisecond, Ease: easeOut}
)

// stagger is the progress of the i-th of n items through a reveal at k,
// each starting a little after the one before: in order on the way in,
// the last first on the way out.
func stagger(k float32, i, n int) float32 {
	const step = 0.07
	span := 1 - step*float32(min(n, 6))
	return min(1, max(0, (k-step*float32(min(i, 6)))/span))
}

// agentsPanelH is the open panel's height for t's rows, More counted.
func (a *app) agentsPanelH(t thread) float32 {
	rows := len(a.shown(t))
	if !a.agentsAll[t.id()] && rows > agentsShown {
		rows = agentsShown + 1
	}
	return 2 + 2*agentsPad + agentsHeadH + float32(rows)*agentsRowH
}

// agentsMorph moves the agents strip on its way between pill and panel,
// on host, for the frame: open grows it, close folds it back, and either,
// mid-way, turns it round from where it is. More grows the panel the same
// way. With less motion asked for, it's there at once (AnimateTo).
func (a *app) agentsMorph(host ui.Element, t thread) {
	key := t.id().String()
	if t.id() != a.agentsFor {
		// Another thread, however it came to be shown: the panel closes as
		// the user leaves, and is closed, not folding, when they come back.
		clear(a.agentsOpen)
		clear(a.agentsAll)
		a.agentsFor = t.id()
		a.agentsK = host.Jump("agents:"+key, 0)
		a.agentsH = host.Jump("agents-h:"+key, a.agentsPanelH(t))
		return
	}
	target, anim := float32(0), agentsFold
	if a.agentsOpen[t.id()] {
		target, anim = 1, agentsGrow
	}
	a.agentsK = host.AnimateTo("agents:"+key, target, anim)
	a.agentsH = host.AnimateTo("agents-h:"+key, a.agentsPanelH(t), agentsGrow)
}

// agentsRoom is the room the agents strip takes over the composer, which
// the timeline leaves under its end: the pill's, open or not. The panel
// floats over the chat, which neither moves nor scrolls as it opens.
func (a *app) agentsRoom(t thread) float32 {
	if len(a.shown(t)) == 0 {
		return 0
	}
	return 8 + agentsPillH
}

// agents is the strip, in the composer's frame frameW wide. fade is how
// far in it is, as the composer morphs: the overlay, out of the strip's
// flow, fades and slides with it, and takes no presses while it's mostly
// gone.
func (a *app) agents(c *ui.Context, t thread, frameW, fade float32) {
	tree := a.shown(t)
	if len(tree) == 0 {
		return
	}
	id := t.id()
	open, k := a.agentsOpen[id], a.agentsK
	pillW := a.agentsPillW
	if pillW == 0 {
		pillW = agentsPillGuess
	}
	full := max(frameW, pillW)
	if frameW == 0 {
		full = contentWidth
	}
	lerp := func(from, to float32) float32 { return from + (to-from)*k }
	// fill builds the strip, as far as it is from pill to panel, in box.
	fill := func(box ui.Element) {
		box.Width(lerp(pillW, full)).Height(lerp(agentsPillH, a.agentsH)).
			Radius(lerp(agentsPillH/2, agentsRadius)).Border(1, palette.border).TextColor(palette.text).Clip()
		a.pane(box, glass.Glass{})
		if k > 0.01 {
			// Floating: the menus' shadow, coming in as it grows.
			box.Shadow(0, 12*k, 32*k, 0, ui.RGBA(0, 0, 0, 0.35*k)).Shadow(0, 2*k, 6*k, 0, ui.RGBA(0, 0, 0, 0.18*k))
		}
		// Closed, or folding, it's the pill: a press opens it, or turns it
		// back round.
		if !open {
			box.Label("Agents").Tooltip(plural(len(tree), "child thread")).Cursor(ui.CursorPointer)
			if k == 0 && box.Hovered() {
				box.Shadow(0, 0, 0, 1, palette.text.Alpha(0.12))
			}
		}
		box.Children(func() {
			// Open, it's a pane of the composer's family: the same glowing
			// edge while the agent works, in place of its hairline.
			if g := workingGlow(c, box, open && t.View.Working(), lerp(agentsPillH/2, agentsRadius)); g > 0 {
				box.Border(1, palette.border.Alpha(1-g))
			}
			// The pill's label, at the composer's edge, gone a quarter of
			// the way in.
			if fade := 1 - 4*k; fade > 0 {
				label := ui.Row(c).Key("pill").Absolute().Left(0).Bottom(0).Height(agentsPillH-2).Padding(0, 11).
					Gap(6).AlignItems(ui.Center).Opacity(fade).PassThrough()
				label.Children(func() {
					// Lifted by its margin, not a Translate, which would
					// lose its ClearType.
					ui.Text(c, "Agents").FontSize(12).Margin(0, 0, 2*agentsTextLift)
					if needsYou(tree) {
						ui.Box(c).Size(6, 6).Radius(3).Background(palette.amber).Translate(0, agentsDotDrop)
					}
				})
				if w := label.Bounds().W; w > 0 {
					a.agentsPillW = w + 2
				}
			}
			if k == 0 {
				return
			}
			// The panel at its full height, held to the growing top edge, and
			// as wide as the strip is now: the head, its ✕ riding the right
			// edge, comes first, and the rows are uncovered under it in the
			// order they rise in.
			panel := ui.Column(c).Key("panel").Absolute().Left(0).Top(0).Width(lerp(pillW, full) - 2).Height(a.agentsH - 2).
				Padding(agentsPad)
			all := a.agentsAll[id] || len(tree) <= agentsShown
			shown := len(tree) + 1
			if !all {
				shown = agentsShown + 2
			}
			// The panel's content comes in only once the pill's label has
			// gone, and goes before it comes back: one "Agents" at a time.
			panel.Opacity(min(1, max(0, (k-0.25)/0.2)))
			panel.Children(func() {
				h := stagger(k, 0, shown)
				ui.Row(c).Key("head").Height(agentsHeadH).Padding(0, 0, 0, agentsInset).AlignItems(ui.Center).
					Opacity(h).Translate(0, 6*(1-h)).Children(func() {
					ui.Text(c, "Agents").FontSize(12).TextColor(palette.muted).Grow(1)
					// Stopping the thread stops only its own agent: these
					// stop here, all at once.
					if n := working(tree); n > 0 {
						if ghost(c, "stop-all", false).Padding(2, 8).Label("Stop all agents").Children(func() {
							ui.Box(c).Size(8, 8).Radius(2).Background(palette.text)
							ui.Text(c, "Stop all").FontSize(11.5)
						}).Clicked() {
							a.stopAll(tree)
							c.AddToast(ui.Toast{Title: "Stopped", Description: plural(n, "child thread"), Type: "info"})
						}
					}
					if iconButton(c, "close", icX, "Close agents", false).Padding(4).Clicked() {
						a.agentsOpen[id], a.agentsAll[id] = false, false
					}
				})
				for i, n := range tree {
					s := stagger(k, i+1, shown)
					if !all && i == agentsShown {
						more := ghost(c, "more", false).Height(agentsRowH).Padding(0, 8).Opacity(s).Translate(0, 6*(1-s))
						if more.Children(func() { ui.Text(c, "More").FontSize(12.5) }).Clicked() {
							a.agentsAll[id] = true
						}
						break
					}
					a.agentRow(c, n, s)
				}
			})
		})
		if !open && box.Clicked() {
			a.agentsOpen[id] = true
		}
	}
	ui.Row(c).Key("agents-strip").Children(func() {
		if k == 0 {
			fill(ui.Column(c).Key("agents"))
			return
		}
		// The pill's place, kept, for the chat's room and as the anchor.
		place := ui.Box(c).Key("agents-place").Size(pillW, agentsPillH)
		if fade == 0 {
			// Faded out with the strip, as the composer folds: nothing to
			// show, nor to keep the pointer from the chat under it.
			return
		}
		ui.Overlay(c, func() {
			box := ui.Column(c).Key("agents").AttachTo(place, ui.AnchorBottomStart, ui.AnchorBottomStart)
			// As the strip it grew from: the overlay escapes the strip's
			// fade and clip, so it takes them itself; before it's built,
			// for what's in it. Not its slide, which its place, as
			// attached to, takes it along on.
			if fade < 1 {
				faded(box, fade)
			}
			fill(box)
			// Escape, or a press outside, closes it; not while it's mostly
			// faded out, as it's closed by neither folded.
			if open && fade >= shownAt && (box.OverlayShortcut(0, ui.KeyEscape) || box.PressedOutside()) {
				a.agentsOpen[id], a.agentsAll[id] = false, false
			}
		})
	})
}

// agentRow is a thread in the agents panel, s of the way into its place:
// what it's doing, its title, and how long ago it changed; pointed at, +
// for a child of its own, while it may have one, and … for its menu.
func (a *app) agentRow(c *ui.Context, n node, s float32) {
	k := n.t
	r := row(c, k.id(), false).Height(agentsRowH).Padding(0, 4, 0, agentsInset+float32(n.depth-1)*14).Gap(8).
		Label(title(k)).Opacity(s).Translate(0, 6*(1-s))
	r.ContextMenu(func(m *ui.Menu) { a.threadMenu(c, m, k, a.categoryOf(k)) })
	hovered := r.Hovered()
	if r.Children(func() {
		statusDot(c, a.statusOf(k), failedLast(k))
		ui.Text(c, title(k)).FontSize(12.5).TextColor(palette.text).SingleLine().Shrink(1)
		ui.Spacer(c)
		if !hovered {
			ui.Text(c, ago(k.View.UpdatedAt, a.now)).FontSize(11).TextColor(palette.faint).Shrink(0)
			return
		}
		if a.depth(k) < maxDepth && !a.duplicate(k.env) {
			if iconButton(c, "new-child", icPlus, "New child thread", false).Padding(3).Clicked() {
				a.startChild(k)
			}
		}
		iconButton(c, "menu", icDots, "Thread actions", false).Padding(3).
			Menu(func(m *ui.Menu) { a.threadMenu(c, m, k, a.categoryOf(k)) })
	}).Clicked() {
		a.select_(k)
	}
}

// failedLast is whether the thread's last turn failed.
func failedLast(t thread) bool {
	return t.View.LastTurn != nil && t.View.LastTurn.Outcome == api.OutcomeFailed && !t.View.Live()
}

// statusDot is a thread's run as a dot: blue and breathing while it works,
// amber waiting on an approval, red failed, green done and unread, grey
// idle.
func statusDot(c *ui.Context, s status, failed bool) ui.Element {
	col, tip := palette.faint, "Idle"
	switch {
	case s == statusWorking:
		col, tip = palette.blue, "Working"
	case s == statusApproval:
		col, tip = palette.amber, "Pending approval"
	case failed:
		col, tip = palette.red, "Failed"
	case s == statusCompleted:
		col, tip = palette.green, "Done"
	}
	d := ui.Box(c).Size(7, 7).Radius(4).Background(col).Shrink(0).Tooltip(tip)
	if s == statusWorking {
		d.Opacity(0.4 + 0.6*d.Loop("breathe", 1400*time.Millisecond, pingPong))
	}
	return d
}
