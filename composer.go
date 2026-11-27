package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/plugins"
	"glitched/internal/plugins/widget"
)

// composerMorph moves the composer on its way between compact and full,
// on host, for the frame: getting to the end of the thread opens it,
// scrolling up folds it, and either, mid-way, turns it round from where it
// is. Jump to latest comes and goes with the end. With less motion asked
// for, both are there at once (AnimateTo).
func (a *app) composerMorph(host ui.Element, t thread, laid bool) {
	id := t.id()
	if !a.composerSeen[id] {
		if !laid && !a.listFor(id).AtEnd() {
			a.composerRest, a.composerK, a.jumpK = false, 1, 0
			host.Jump("composer:"+id.String(), 1)
			host.Jump("jump:"+id.String(), 0)
			return
		}
		a.composerSeen[id] = true
	}
	target, anim := float32(1), composerOpen
	if a.composerCompact(t) {
		target, anim = 0, composerClose
	}
	// Held to [0, 1]: turned round near an end, the spring carries on past
	// it for a moment before it comes back.
	k := clamp01(host.AnimateTo("composer:"+id.String(), target, anim))
	a.composerRest, a.composerK = k == a.composerK && (k == 0 || k == 1), k
	target, anim = 0, composerClose
	if !a.listFor(id).AtEnd() {
		target, anim = 1, composerOpen
	}
	a.jumpK = clamp01(host.AnimateTo("jump:"+id.String(), target, anim))
}

// fadeIn is the progress through [from, from+span] of k, held to [0, 1].
func fadeIn(k, from, span float32) float32 { return clamp01((k - from) / span) }

// shownAt is the opacity from which what fades in and out with the
// composer's morph takes presses: below it, mostly gone, it lets them be.
const shownAt = 0.5

// disabledOpacity is how much of its opacity MyGo paints a disabled
// element at (ui/paint.go).
const disabledOpacity = 0.5

// faded shows e x of the way in and, below shownAt, disables it: MyGo has
// no other way to keep the presses off a whole subtree that still shows.
// It sets the opacity that, with the dimming of what's disabled, paints e
// at x either side of shownAt, rather than at half that below it.
func faded(e ui.Element, x float32) ui.Element {
	o, off := fadedOpacity(x)
	return e.Opacity(o).Disabled(off)
}

// fadedOpacity is the opacity faded sets for x, and whether it disables.
func fadedOpacity(x float32) (opacity float32, disabled bool) {
	if x < shownAt {
		return min(1, x/disabledOpacity), true
	}
	return x, false
}

// clamp01 holds v to [0, 1].
func clamp01(v float32) float32 { return min(1, max(0, v)) }

// composerCompact reports whether the composer is compact: away from the
// end of the thread, not clicked open, holding nothing.
func (a *app) composerCompact(t thread) bool {
	if !a.composerSeen[t.id()] || a.listFor(t.id()).AtEnd() {
		a.composerOpened = false
		return false
	}
	return !a.composerOpened && !a.drafting()
}

// drafting reports whether the composer holds anything: a draft, a code
// block, empty or not, or attachments.
func (a *app) drafting() bool {
	return strings.TrimSpace(a.draft) != "" || a.code.on || len(a.attachments) > 0
}

// compactWidth is the composer's width while compact.
const compactWidth = 520

// composerBarBottom is the room the composer leaves under it.
const composerBarBottom = 14

// cardLook is what the composer card's heights hang on: the font size and
// the pane's border.
type cardLook struct {
	fontSize float64
	border   float32
}

func (a *app) cardLook() cardLook { return cardLook{a.settings.fontSize, a.paneBorder()} }

// cardHeights are the composer card's heights, compact and full, as last
// measured under the settings in force, or a guess before: compact, a line
// of text measured in the font size, its padding and the card's border,
// which the first frame of a fold takes before it's measured. A change of
// font size or of Liquid Glass drops the compact height measured under the
// old ones; the full one, which the font size doesn't change, it keeps, but
// for the border Liquid Glass takes away or gives back.
func (a *app) cardHeights(c *ui.Context) (compactH, fullH float32) {
	if l := a.cardLook(); l != a.cardFor {
		if a.cardFullH != 0 {
			a.cardFullH += 2 * (l.border - a.cardFor.border)
		}
		a.cardFor, a.cardCompactH = l, 0
	}
	compactH, fullH = a.cardCompactH, a.cardFullH
	if compactH == 0 {
		// The line, a line of text high, in 11 above and below.
		_, lineH := c.MeasureText(0, ui.Span{Text: "Ask a follow-up…"})
		compactH = 22 + lineH + 2*a.paneBorder()
	}
	if fullH == 0 {
		// Two lines of text area and the controls, in the border.
		fullH = 124 + 2*a.paneBorder()
	}
	return compactH, fullH
}

// composerHeight is the height of the composer, the card with what's over
// and under it, as it is in this frame: k of the way from compact to full,
// as composer lays it out, rather than as it was laid out last frame.
func (a *app) composerHeight(c *ui.Context, t thread) float32 {
	k := a.composerK
	compactH, fullH := a.cardHeights(c)
	h := compactH + (fullH-compactH)*k
	if a.hasAbove(t) {
		h += a.aboveH * k
	}
	return h + a.envH*k + a.composerAround.h
}

// hasAbove is whether anything sits over t's composer card: the agents
// strip, or the messages that wait their turn.
func (a *app) hasAbove(t thread) bool { return len(a.shown(t)) > 0 || len(waiting(t)) > 0 }

// composer is the card the user writes in, over the end of the chat.
//
// While the user reads back up the thread, it steps out of the way:
// narrower, one line, its controls folded away. It comes back to its full
// size at the end of the thread, as it's clicked, and whenever it holds a
// draft or attachments, so it never hides work.
//
// It morphs between the two on a spring, k of the way from compact to
// full, turning round mid-way from where it is: the card eases its width,
// height and radius from the one line's to the full composer's, the line's
// placeholder giving way to the text area's, then the controls, the row
// below and the agents strip above, which fade and slide in after it,
// going first as it folds.
// Mid-way, what isn't in the card's flow is held to its top, the card
// clipping it.
func (a *app) composer(c *ui.Context, t thread, rs rows) {
	// The bar lets the pointer through, but for the composer in it, to
	// what is around it, as the pill taking the user to the end.
	// d is how far it is from the hero to its dock (hero.go). Docked, it's
	// in the transcript's column, its edges the column's; the hero, with
	// no transcript nor turn markers, is in the middle of the pane, the
	// marker column's room coming in as the transcript does.
	d := a.dockK
	left := a.composerLeft()
	bar := ui.Column(c).Key("composer-bar").Absolute().Bottom(a.heroLift()*(1-d)).Left(0).Right(0).AlignItems(ui.Center).
		Padding(0, columnRight, composerBarBottom, left).PassThrough()
	// The full composer's width at rest, as the last frame had room for:
	// the full card's, in its border, or 0 before there's been a frame.
	var fullW float32
	if w := bar.Bounds().W - left - columnRight; w > 0 {
		fullW = max(0, min(w, contentWidth)-2*a.paneBorder())
	}
	bar.Children(func() {
		frame := ui.Column(c).Key("composer-frame").WidthPercent(100).MaxWidth(a.composerMaxW()).PassThrough().
			Bind(&a.composerH)
		frame.Children(func() {
			// The composer plugin's layout, around the native
			// composer.input, which is the card and what comes and goes
			// with it; Glitched's own when no plugin draws it.
			a.composerIn = func(c *ui.Context) {
				// In a plugin's layout, the card is as wide as the layout
				// leaves it, and the room it takes over and under the
				// card is the composer's too, as Jump to latest rides it.
				in := ui.Column(c).Key("composer-input").PassThrough()
				if b, f := in.Bounds(), frame.Bounds(); b.W > 0 {
					a.composerAround.w, a.composerAround.h = f.W-b.W, f.H-b.H
				}
				in.Children(func() { a.composerInput(c, t, rs, max(0, fullW-a.composerAround.w), in.Bounds().W) })
			}
			box := func() ui.Element { return ui.Column(c).Key("composer-plugin").PassThrough() }
			if !a.pluginComponent(c, "composer", &t, componentCopy{key: "composer", box: box, context: func(r plugins.ThreadRecord) any { return composerContext{r} }}) {
				a.composerAround.w, a.composerAround.h = 0, 0
				a.composerInput(c, t, rs, fullW, frame.Bounds().W)
			}
		})
	})
}

// composerLeft is the composer bar's room at its left, from the hero's to
// the dock's, as the transcript's column has it docked.
func (a *app) composerLeft() float32 {
	return columnRight + (columnLeft-columnRight)*transcriptK(a.dockK)
}

// composerMaxW is the most the composer's frame takes, from the compact
// line's to the full card's.
func (a *app) composerMaxW() float32 {
	return compactWidth + (contentWidth-compactWidth)*a.composerK
}

// composerContext is the context of composer.
type composerContext struct {
	Thread plugins.ThreadRecord `json:"thread"`
}

// composerInput is the native composer.input: the hero's heading, the
// agents strip over the card, the card the user writes in, and the row
// below it, in the composer's frame, frameW wide. fullW is the full card's
// width at rest, 0 before there's been a frame.
func (a *app) composerInput(c *ui.Context, t thread, rs rows, fullW, frameW float32) {
	compact := a.composerCompact(t)
	k := a.composerK
	morphing := k > 0 && k < 1
	// Chosen, the thread's composer takes the focus in the editor this
	// build makes, the line only once the card is compact: a request for
	// a control not built would wait, and take the focus later from
	// whatever had it by then. Folding, the focus goes on to the line
	// only from the area, still focused, rather than from something
	// clicked since.
	if a.chosen != (api.ID{}) && a.chosen == t.id() {
		want := &a.draftArea
		if k == 0 {
			want = &a.draftLine
		}
		if a.asked == nil || a.asked != want && a.asked.Focused(c) {
			want.Focus()
			a.asked = want
		}
		if k == 0 && compact || k == 1 && !compact {
			a.chosen, a.asked = api.ID{}, nil
		}
	}
	lerp := func(from, to float32) float32 { return from + (to-from)*k }
	compactH, fullH := a.cardHeights(c)
	d := a.dockK
	a.heroHeading(c, d)
	// The agents strip and the queue sit over the card, the room they
	// take going with them as they slide down into it.
	if k > 0 && a.hasAbove(t) {
		s := fadeIn(k, 0.45, 0.55)
		above := ui.Column(c).Key("above").Justify(ui.End).PassThrough()
		if morphing {
			above.Height(a.aboveH * k).ClipY()
		}
		above.Children(func() {
			in := faded(ui.Column(c).Key("in").Padding(0, 0, 8).Shrink(0).PassThrough().
				Translate(0, 8*(1-s)), s)
			if h := in.Bounds().H; h > 0 {
				a.aboveH = h
			}
			in.Children(func() {
				a.queueStrip(c, t)
				a.agents(c, t, frameW, s)
			})
		})
	}
	// What the last frame measured was laid out under the settings
	// in force, or it isn't to be taken.
	fresh := a.cardLaid == a.cardFor
	a.cardLaid = a.cardFor
	card := ui.Column(c).Key("composer").Radius(lerp(min(20, compactH/2), 20))
	if morphing {
		a.cardH = lerp(compactH, fullH)
		card.Height(a.cardH)
	} else if d < 1 {
		// The hero's card is larger, settling to its own height as
		// it docks; it isn't the full card's height to measure.
		card.MinHeight(heroCardH * (1 - d))
	} else if a.composerRest && fresh {
		if h := card.Bounds().H; h > 0 && k == 0 {
			a.cardCompactH = h
		} else if h > 0 {
			a.cardFullH = h
		}
	} else if a.composerRest {
		// Laid out under other settings: measured in the next
		// frame, which nothing else may ask for.
		c.AnimationFrame()
	}
	if compact && card.Clicked() {
		a.composerOpened = true
	}
	if card.FocusWithin() {
		card.Shadow(0, 0, 0, 3, palette.accent.Alpha(0.22))
	}
	a.accessEffects(card, a.shownAccess(t))
	a.autocomplete(c, card)
	// Files go to the agent as their paths, which a server's
	// agent can't read.
	dragOver := t.local() && a.dropZone(card)
	a.frosted(c, card).Shadow(0, 12, 32, 0, ui.RGBA(0, 0, 0, 0.28)).Clip().Children(func() {
		workingGlow(c, card, t.View.Working(), 20)
		if k < 1 {
			// A line to type a follow-up into, which a text area,
			// at least two lines high, can't be; the first key
			// typed brings the full composer back, its text with it.
			line := ui.Column(c).Key("line")
			if morphing {
				line.Absolute().Top(0).Left(0).Right(0).Opacity(1 - fadeIn(k, 0, 0.3))
				// The compact card is as high as the line in its
				// border: before it's been at rest, the line says
				// how high.
				if h := line.Bounds().H; h > 0 && a.cardCompactH == 0 && fresh {
					a.cardCompactH = h + 2*a.paneBorder()
				}
			}
			line.Children(func() {
				ui.TextInput(c.Key("draft-compact"), &a.draft).Bind(&a.draftLine).Placeholder("Ask a follow-up…").
					Background(ui.Transparent).Border(0, ui.Transparent).FocusRing(false).Padding(11, 18).
					OnPaste(a.onPaste)
			})
		}
		// Compact but holding something now, typed, pasted or
		// dropped in this frame, the composer opens from the next:
		// laid out unseen in this one, the full composer is measured
		// before the first frame of the morph, rather than in it.
		// Invisible, it takes neither the pointer nor the focus, and
		// isn't there to assistive technology.
		ahead := k == 0 && (a.composerOpened || a.drafting())
		if k == 0 && !ahead {
			// Compact, there's no Send to stop the agent with: ⌘⏎
			// still does, as it does from the full composer, but not
			// a spent chord's, whose key it takes all the same.
			if t.View.Stoppable() && a.message(t) == "" && c.Shortcut(ui.Cmd, ui.KeyEnter) && !a.spent {
				a.backend.Interrupt(t.env, t.id())
			}
			return
		}
		full := ui.Column(c).Key("full")
		// In the hero's larger card, the text area takes the room.
		large := d < 1 && !morphing && !ahead
		if large {
			full.Grow(1)
		}
		if morphing || ahead {
			// Out of the card's flow, it's as wide as it is at rest,
			// rather than the card's width on its way there, the
			// card clipping it: its text wraps as it will, never
			// reflowing as the card widens. So it's as high as it
			// is at rest, and the full card is that in its border.
			full.Absolute().Top(0).Left(0)
			if fullW > 0 {
				full.Width(fullW)
			} else {
				full.Right(0)
			}
			if ahead {
				full.Invisible()
			}
			if h := full.Bounds().H; h > 0 && fresh {
				a.cardFullH = h + 2*a.paneBorder()
			}
			// The card takes the heights as just updated, from the
			// last frame's layout, the unseen pass's included,
			// rather than as they were before this frame took them.
			if morphing {
				compactH, fullH = a.cardHeights(c)
				a.cardH = lerp(compactH, fullH)
				card.Height(a.cardH)
			}
		}
		full.Children(func() {
			in := fadeIn(k, 0.25, 0.5)
			if len(a.attachments) > 0 {
				faded(ui.Column(c).Key("attachments").Padding(12, 12, 0), in).Children(func() {
					a.attachmentChips(c, a.attachments, t, func(i int) {
						a.attachments = append(a.attachments[:i:i], a.attachments[i+1:]...)
					})
				})
			}
			placeholder := "Ask anything, @tag files/folders, or use / to show available commands"
			switch r := waitingRequest(t, rs); {
			case r != nil && r.Kind == api.KindQuestion:
				placeholder = "Reply to steer, or answer the questions above"
			case r != nil && r.Kind == api.KindCredentials:
				placeholder = "Reply to steer, or fill in the credentials above"
			case r != nil:
				placeholder = "Reply to steer, or answer the approval above"
			}
			// Unseen, the text area is one of its own, which never
			// takes the focus: the one that shows is new as it shows,
			// taking the focus from the line with the caret at the
			// end of the draft, rather than where it was left as the
			// unseen one was laid out, what the line took since
			// having gone in after it.
			key := "draft"
			if ahead {
				key = "draft-ahead"
			}
			a.typedFence()
			a.draftAreas(c, t, key, placeholder, ahead, large, in)
			in = fadeIn(k, 0.35, 0.5)
			faded(ui.Row(c).Key("controls").Padding(6, 8, 8).Gap(2).AlignItems(ui.Center).
				Translate(0, 6*(1-in)), in).Children(func() {
				a.threadControls(t, func() { a.composerControls(c, t) })
				ui.Spacer(c)
				if t.local() && a.iconButton(c, "attach", icPaperclip, "Attach files", false).Shrink(0).Clicked() {
					a.pickFiles()
				}
				a.sendControl(c, t, !ahead)
			})
		})
		if dragOver {
			dropRing(c)
		}
	})
	if k == 0 {
		return
	}
	// The row below the card, which rises up under it as it folds.
	e := fadeIn(k, 0.45, 0.55)
	below := ui.Column(c).Key("below").PassThrough()
	if morphing {
		below.Height(a.envH * k).ClipY()
	}
	below.Children(func() {
		env := faded(ui.Row(c).Key("env-row").Gap(4).AlignItems(ui.Center).Padding(8, 6, 0).Shrink(0).
			Translate(0, -6*(1-e)), e)
		if h := env.Bounds().H; h > 0 {
			a.envH = h
		}
		env.Children(func() { a.workspaceControl(c, t) })
	})
}

// workspaceControl is where the thread runs, Glitched's button or a
// plugin's that replaces it (composer.workspace). A plugin draws the
// button; a draft's menu of where it could start is Glitched's, at the end
// of the plugin's menu. A plugin's that fails gives way to Glitched's.
func (a *app) workspaceControl(c *ui.Context, t thread) {
	box := func() ui.Element { return ui.Row(c).Key("workspace-plugin").AlignItems(ui.Center) }
	w := composerWorkspace{Workspace: a.projectOf(t).name(), Worktree: a.worktreeLabel(t), Draft: t.id().IsZero()}
	if len(a.backend.Envs()) > 1 && t.env != nil {
		w.Machine = t.env.Name
	}
	if w.Draft {
		w.menu = func(m *ui.Menu) { a.workspaceMenu(m, t) }
	}
	ctx := func(r plugins.ThreadRecord) any {
		w.Thread = r
		return w
	}
	// The key is what the button says, with the workspace's whole path, so
	// that a drawing of another workspace's never stands for this one's.
	key := composerKey(t) + "\x00" + t.View.Workspace + "\x00" + w.Machine + "\x00" + w.Worktree
	a.pluginComponent(c, "composer.workspace", &t, componentCopy{key: key, box: box, context: ctx, native: func(*widget.Node) { a.workspaceButton(c, t) }})
}

// composerWorkspace is the context of composer.workspace.
type composerWorkspace struct {
	Thread plugins.ThreadRecord `json:"thread"`
	// Workspace is the folder's name, and Machine the machine's, only
	// while there is more than one.
	Workspace string `json:"workspace"`
	Machine   string `json:"machine,omitempty"`
	// Worktree is the branch of a thread in a worktree of its own, or
	// the worktree a draft asks for; "" in its folder.
	Worktree string `json:"worktree,omitempty"`
	// Draft is whether the composer is a new thread's, whose workspace the
	// user chooses from the menu, which menu ends.
	Draft bool `json:"draft"`
	menu  func(*ui.Menu)
}

// workspaceButton is where the thread runs: its workspace, and its machine
// when there are several. A draft's is a menu of where it could start.
func (a *app) workspaceButton(c *ui.Context, t thread) {
	several := len(a.backend.Envs()) > 1
	b := ghost(c, "workspace", false).Padding(3, 6).Label("Workspace").Children(func() {
		ui.Icon(c, icFolder).FontSize(12)
		ui.Text(c, a.projectOf(t).name()).FontSize(11.5)
		if several && t.env != nil {
			ui.Text(c, "on "+t.env.Name).FontSize(11.5).TextColor(palette.faint)
		}
		if w := a.worktreeLabel(t); w != "" {
			ui.Icon(c, icBranch).FontSize(12)
			ui.Text(c, w).FontSize(11.5).TextColor(palette.faint)
		}
		if t.id().IsZero() {
			ui.Icon(c, icChevronD).FontSize(10)
		}
	})
	if !t.id().IsZero() {
		return
	}
	b.Menu(func(m *ui.Menu) { a.workspaceMenu(m, t) })
}

// workspaceMenu is the draft t's menu of where it could start, ended with
// Add project and Remove project.
func (a *app) workspaceMenu(m *ui.Menu, t thread) {
	several := len(a.backend.Envs()) > 1
	for _, e := range a.backend.Envs() {
		if a.duplicate(e) {
			continue
		}
		for _, p := range a.workspacesOn(e) {
			label := p.name()
			if several {
				label += " on " + e.Name
			}
			if m.Item(label).Checked(t.env == e && t.View.Workspace == p.key.workspace).Chosen() {
				a.fresh, a.repos = draft{e, p.key.workspace}, nil
			}
		}
	}
	a.worktreeItems(m, t)
	a.workspaceItems(m)
}

// workspacesOn are where a draft on e could start: No project on this
// machine, or home on a server, then e's projects, those with no thread
// too.
func (a *app) workspacesOn(e *client.Env) []*project {
	first := a.newProject(e, keyOf(e, a.scratchRoot()))
	if e.Kind == client.Remote {
		first = a.newProject(e, keyOf(e, "~"))
	}
	ps := []*project{first}
	for _, p := range a.projects {
		if p.env == e && p.key != first.key {
			ps = append(ps, p)
		}
	}
	return ps
}

// sendButton presses in as it's pressed; its color says whether there is
// anything to send. While the agent works, a message steers it, and with
// none the button stops it, as it ends a run left uncertain. Holding a
// message, the composer keeps a Stop of its own beside it, so a draft
// never hides the only way to stop a run. Without keys, it takes no ⌘⏎:
// laid out unseen, it's no more there to the keyboard than to the pointer.
func (a *app) sendButton(c *ui.Context, t thread, keys bool) {
	empty := a.message(t) == ""
	live := t.View.Stoppable()
	working := live && empty
	label := "Send"
	if working {
		label = "Stop"
	}
	send := ui.ButtonBase(c.Key("send")).Label(label).Size(30, 30).Radius(15).Center().Shrink(0).
		Cursor(ui.CursorPointer).Disabled(empty && !working)
	bg := palette.text
	if empty && !working {
		bg = palette.faint.Alpha(0.5)
	}
	send.Background(send.AnimateColor("bg", bg, ui.Animation{Duration: 150 * time.Millisecond, Ease: ease})).TextColor(palette.bg)
	pose := ui.Pose{}
	if send.Pressed() {
		pose.Scale = 0.97
	}
	send.AnimatePose(pose, press)
	send.Children(func() {
		if working {
			ui.Box(c).Size(10, 10).Radius(2).Background(palette.bg)
		} else {
			ui.Icon(c, icArrowUp).FontSize(16)
		}
	})
	clicked := send.Clicked()
	if clicked || keys && c.Shortcut(ui.Cmd, ui.KeyEnter) {
		a.sendOrStop(t, clicked)
	}
}

// sendOrStop is what Send does, clicked or by ⌘⏎: it sends the draft, or
// with none stops the agent at work. While the agent works, ⌘⏎ queues the
// draft for after its turn where Send and Enter steer it, as T3 Code's
// alternate send does; while it waits on a request, the draft steers, as
// a send does in T3 Code with an approval shown, since a queued message
// would wait for an answer. ⌘⏎ is how a code block sends: with a block
// open, it steers, and doesn't stop the agent. A ⌘⏎ that sent or queued
// the draft spends its chord: held on, it answers no card, and sends,
// queues or stops nothing more, nor does a ⌘⏎ of its hold taken once ⌘
// is up, until ⌘ goes down again.
func (a *app) sendOrStop(t thread, clicked bool) {
	if !clicked && a.spent {
		return
	}
	m := a.message(t)
	switch {
	case a.queues(t) && m != "" && !clicked:
		a.queue(t)
	case !t.View.Stoppable() || m != "":
		a.send(t)
	case clicked || !a.code.on:
		a.backend.Interrupt(t.env, t.id())
	}
	if !clicked && m != "" && a.message(t) == "" {
		a.spent = true
	}
}

// resumeButton starts what a stop held: the messages queued behind the
// run, a Defer, or a child's report waiting for the agent. Beside Send,
// Glitched's or a plugin's, so no timeline drawing can hide it.
func (a *app) resumeButton(c *ui.Context, t thread) {
	r := a.tip(c, ui.ButtonBase(c.Key("resume")).Label("Resume"), "Resume the queue").Height(30).Radius(15).Padding(0, 12).
		Cursor(ui.CursorPointer).Border(1, palette.border).Margin(0, 4, 0, 0).Shrink(0)
	if r.Hovered() {
		r.Background(palette.hover)
	}
	if r.Children(func() {
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, icRotate).FontSize(12)
			ui.Text(c, "Resume").FontSize(12)
		})
	}).Clicked() {
		a.backend.Resume(t.env, t.id())
	}
}

// activeTurn is whether the thread has a live agent turn the user can queue
// behind or steer: a normal working turn, or a quiet Manager wake (whose
// Working() stays false so the sidebar stays quiet). Silent idle compaction
// and uncertain runs return false.
func activeTurn(v api.Thread) bool {
	return v.Run != api.RunIdle && v.Run != api.RunUncertain && (!v.Silent() || v.Turn != nil && v.Turn.Dismissed)
}

// queues is whether ⌘⏎ queues the draft rather than steering it: while
// the agent works (including a quiet Manager wake), not waiting on a
// request, with no code block open. A run left uncertain is never at work:
// it doesn't end on its own.
func (a *app) queues(t thread) bool {
	return activeTurn(t.View) && t.View.Run != api.RunWaiting && !a.code.on
}

// queueButton sends the draft for after the agent's turn, rather than
// into it, beside Send while the agent works.
func (a *app) queueButton(c *ui.Context, t thread) {
	tip := "Send after this turn"
	if a.queues(t) {
		tip += "  ⌘⏎"
	}
	q := a.tip(c, ui.ButtonBase(c.Key("queue")).Label("Queue"), tip).Height(30).Radius(15).Padding(0, 12).
		Cursor(ui.CursorPointer).Border(1, palette.border).Margin(0, 4, 0, 0).Shrink(0)
	if q.Hovered() {
		q.Background(palette.hover)
	}
	if q.Children(func() {
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, icClock).FontSize(12)
			ui.Text(c, "Queue").FontSize(12)
		})
	}).Clicked() {
		a.queue(t)
	}
}

// stopButton stops the agent at work, beside Send while the composer holds
// a message, or beside a plugin's Send whatever that draws.
func (a *app) stopButton(c *ui.Context, t thread) ui.Element {
	stop := a.tip(c, ui.ButtonBase(c.Key("stop")).Label("Stop"), "Stop the agent").Size(30, 30).Radius(15).Center().
		Cursor(ui.CursorPointer).Border(1, palette.border).Margin(0, 4, 0, 0)
	if stop.Hovered() {
		stop.Background(palette.hover)
	}
	if stop.Children(func() { ui.Box(c).Size(10, 10).Radius(2).Background(palette.text) }).Clicked() {
		a.backend.Interrupt(t.env, t.id())
	}
	return stop
}

// sendControl is Send, Glitched's or a plugin's that replaces it. Beside a
// plugin's, Glitched keeps its own Stop while the agent works, and ⌘⏎
// sends and stops as it does with Glitched's: a plugin that draws nothing
// never takes away the way to stop a run, and one that fails gives way to
// Glitched's Send.
func (a *app) sendControl(c *ui.Context, t thread, keys bool) {
	if a.message(t) == "" && continuable(t) {
		a.continueButton(c, t)
	}
	if a.message(t) == "" && limited(t) {
		a.limitControl(c, t, time.Now())
	}
	// The plugin's gives up its room first: however wide it draws, Stop
	// keeps its own.
	box := func() ui.Element {
		return ui.Row(c).Key("send-plugin").AlignItems(ui.Center).Shrink(1).MinWidth(0).ClipX()
	}
	ctx := func(r plugins.ThreadRecord) any {
		return composerSend{Thread: r, Draft: a.message(t) != "", Working: t.View.Stoppable()}
	}
	if t.View.Resumable {
		a.resumeButton(c, t)
	}
	// The native circular Send is itself Stop with nothing to send, as it
	// is in Glitched's fallback and the built-in's native drawing. Only a
	// replacement of someone else's needs Glitched's own Stop then; with a
	// draft, Stop always stands beside what Send does. Decided inside
	// pluginComponent right before the copy draws so this frame uses its
	// own winner with no stale frame lookup or one-frame flicker.
	before := func(by copyDrawn) {
		nativeStop := by.plugin == "" || by.builtin
		if t.View.Stoppable() && (a.message(t) != "" || !nativeStop) {
			if a.message(t) != "" && activeTurn(t.View) {
				a.queueButton(c, t)
			}
			a.stopButton(c, t).Shrink(0)
		}
	}
	if !a.pluginComponent(c, "composer.send", &t, componentCopy{key: composerKey(t), box: box, builtinBox: func() ui.Element {
		return ui.Row(c).Key("send-plugin").AlignItems(ui.Center).Shrink(0)
	}, context: ctx, before: before}) {
		a.sendButton(c, t, keys)
		return
	}
	if keys && c.Shortcut(ui.Cmd, ui.KeyEnter) {
		a.sendOrStop(t, false)
	}
}

// composerSend is the context of composer.send.
type composerSend struct {
	Thread plugins.ThreadRecord `json:"thread"`
	// Draft is whether the composer holds something to send.
	Draft bool `json:"draft"`
	// Working is whether the agent runs a turn, which Send stops when
	// there's nothing to send.
	Working bool `json:"working"`
}

// composerKey keys the composer's components by its thread's id, or the
// draft's.
func composerKey(t thread) string {
	if t.id().IsZero() {
		return "draft"
	}
	return t.id().String()
}

// submitEffect is composer.submit: it sends what the open composer holds,
// as Send does, in the thread it names, or with none in the new thread's
// draft. It never sends what the plugin wrote: only the user's draft.
func (a *app) submitEffect(args json.RawMessage) (any, error) {
	var ref struct{ Thread string }
	if err := json.Unmarshal(args, &ref); err != nil {
		return nil, err
	}
	t := a.current()
	if a.router.Path() != "/" || ref.Thread != t.id().String() && !(ref.Thread == "" && t.id().IsZero()) {
		return nil, fmt.Errorf("the composer of %q isn't open", ref.Thread)
	}
	if a.message(t) == "" {
		return nil, errors.New("the composer holds nothing to send")
	}
	a.send(t)
	return nil, nil
}

// draftKey takes the draft's keys before the text area does, as T3 Code's
// composer does: the suggestions' first, then Enter sends what Send
// would, the attachments with the draft; with Shift it breaks the line,
// and with another modifier no shortcut takes, as Ctrl on macOS, it
// doesn't send either. With nothing to send, Enter breaks no line and
// never stops the agent as ⌘⏎ does. An input method's keys are its own,
// so the Enter that commits what it composes sends nothing.
// Like OnTextKey, it runs between frames: it sends what was typed, which
// may be ahead of the draft, and clears the text area only once it went.
func (a *app) draftKey(t thread) func(ui.TextKey) bool {
	return func(k ui.TextKey) bool {
		if a.suggestKey(k) {
			return true
		}
		if own, take := a.codeKey(k); own {
			return take
		}
		if k.Key != ui.KeyEnter || k.Mods != 0 {
			return false
		}
		text := k.Text()
		a.draft = text
		if a.send(t); a.draft != text {
			// Sent: the text area clears with the draft. Unsent, it's
			// left as it is, its selection and its redo with it.
			k.SetText(a.draft)
		}
		return true
	}
}

func (a *app) composerControls(c *ui.Context, t thread) {
	a.modelPicker(c, t)
	ui.Box(c).Size(1, 16).Background(palette.border).Margin(0, 4)

	a.accessControl(c, t)
}

// send sends the draft in the thread, creating it first when it's the
// draft. A snoozed thread comes back to the inbox with it, as the engine
// brings a settled one back with any message.
func (a *app) send(t thread) { a.sendDraft(t, api.SteerActive) }

// queue sends the draft as send does, for after the agent's turn.
func (a *app) queue(t thread) { a.sendDraft(t, api.AfterActive) }

func (a *app) sendDraft(t thread, mode api.DispatchMode) {
	about := a.said(t)
	if ct := codeTitle(about); a.code.on && ct != "" {
		about = ct
	}
	if !a.sendAs(t, a.message(t), about, mode) {
		return
	}
	if a.code.on {
		// Sent from a piece of the draft, which goes with it, the focus
		// comes back to the draft.
		a.focusPane(0, 0)
	}
	a.draft, a.code = "", codeBlock{}
	a.attachments = slices.DeleteFunc(a.attachments, func(at attachment) bool { return at.held(t) == "" })
}

// sendText sends text in the thread, creating it first when it's the draft,
// titled from about, or text without it, and reports whether it went.
func (a *app) sendText(t thread, text, about string) bool {
	return a.sendAs(t, text, about, api.SteerActive)
}

// sendAs is sendText in mode: a steer reaches a working agent now, an
// AfterActive waits for its turn to end.
func (a *app) sendAs(t thread, text, about string, mode api.DispatchMode) bool {
	if text == "" || t.env == nil {
		return false
	}
	id := t.id()
	if id.IsZero() {
		ws := t.View.Workspace
		if a.folderless(t) {
			dir, err := a.scratchFolder(text)
			if err != nil {
				a.pluginNotice(ui.Toast{Title: "Couldn't make the thread's folder", Description: err.Error()})
				return false
			}
			ws = dir
		}
		title := threadTitle(text, about)
		if id = a.backend.Create(t.env, ws, title, a.newThreadRef(t), accessOf[a.access], a.newWorktree(t)); id.IsZero() {
			return false
		}
		if a.followsCLI && !a.cliDecides(t) {
			a.guessed[id] = choice{Model: a.model, Effort: a.effort}
		}
		a.rememberProject(t.env, a.threadKey(t).workspace)
		a.worktree, a.repos = worktreeChoice{}, nil
		if hero(t) {
			a.sentFromHero(id)
		}
		a.selected = id
		a.follow()
	} else {
		// Either may be what the engine ends with: the view's, if what
		// was chosen here fails, or what was chosen, which the view
		// shows once it comes.
		if t.View.Disposition.State == api.Snoozed || disposition(t).State == api.Snoozed {
			a.backend.SetDisposition(t.env, id, api.Disposition{})
		}
		// A thread runs at the effort its picker shows: one its model
		// doesn't run at, as since its engine stopped listing it, is
		// set to that one before the turn. One that names no model
		// leaves its effort to its provider's program too.
		m, _ := configured(t)
		model, _ := a.modelOf(keyOfThread(m.Provider, m.Model))
		if e := effortFor(model, m.Effort); m.Model != "" && !strings.EqualFold(e, m.Effort) {
			a.backend.ConfigureThread(t.env, id, api.ThreadConfigure{Effort: &e})
		}
	}
	// One to a run left uncertain replaces it.
	var cmd api.ID
	if continuable(t) {
		cmd = a.backend.Replace(t.env, id, text)
	} else {
		cmd = a.backend.Send(t.env, id, text, mode)
	}
	if cmd.IsZero() {
		return false
	}
	a.steers[cmd] = steer{t.env, id}
	a.listFor(id).ScrollToEnd()
	return true
}

// steer is where a message sent from the composer went.
type steer struct {
	env    *client.Env
	thread api.ID
}

// takeBack puts a message the engine refused to steer in, as it does while
// the agent stops, back in the composer, and says why; but only in its own
// thread's composer, shown and empty, as the composer is one. Otherwise it
// stays in the timeline, not sent, with Retry and Dismiss. Sending it once
// the run ends is the user's call.
func (a *app) takeBack(c *ui.Context, t thread) {
	for cmd, s := range a.steers {
		var p client.Pending
		if th := s.env.Threads[s.thread]; th != nil {
			if i := slices.IndexFunc(th.Pending, func(p client.Pending) bool { return p.Command == cmd }); i >= 0 {
				p = th.Pending[i]
			}
		}
		if !p.Command.IsZero() && p.Err == nil {
			continue // on its way
		}
		delete(a.steers, cmd)
		var rej *api.RejectedError
		if !errors.As(p.Err, &rej) || s.env != t.env || s.thread != t.id() || a.message(t) != "" || a.code.on {
			continue // landed, or kept in the timeline
		}
		a.backend.Dismiss(s.env, s.thread, cmd)
		a.draft, a.code = p.Body.(api.MessageDispatch).Text, codeBlock{}
		c.AddToast(ui.Toast{Title: "Not sent", Description: rej.Reason, Type: "info"})
	}
}

// message is what goes to t's agent: the quotes on the composer, as
// blockquotes, then what the draft says (said).
func (a *app) message(t thread) string {
	// Quotes first, as what the draft is about.
	var parts []string
	for _, at := range a.attachments {
		if at.quote {
			parts = append(parts, blockquote(at.text))
		}
	}
	if said := a.said(t); said != "" {
		parts = append(parts, said)
	}
	return strings.Join(parts, "\n\n")
}

// said is the draft and what else is attached to it that goes to t's
// agent, but the quotes: files as their paths, as the composer's @ puts
// them, and pasted text after.
func (a *app) said(t thread) string {
	draft := strings.TrimSpace(a.draft)
	if a.code.on {
		draft = a.codeMessage()
	}
	parts := []string{draft}
	for _, at := range a.attachments {
		if at.quote || at.held(t) != "" {
			continue
		}
		switch {
		case at.path != "":
			parts = append(parts, "@"+at.path)
		case at.text != "":
			parts = append(parts, at.text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

// cliDecides is whether the picker in the draft t names no model for the
// default it follows: t's engine is another machine's, whose program and
// default this machine's engine doesn't know.
func (a *app) cliDecides(t thread) bool { return a.followsCLI && t.id().IsZero() && !t.local() }

// newThreadRef is the model a new thread in the draft t starts on: the
// pickers' (modelRef), but none where the default follows its provider's
// program, which then runs its own where the thread runs, as its settings
// there say; and no effort there but the one the user chose, as they
// chose it, whatever model the picker expects. The picker's
// name for that default is the one the engine expects, until the
// program's report of what it ran replaces it.
func (a *app) newThreadRef(t thread) api.ModelRef {
	ref := a.modelRef()
	if a.followsCLI && t.id().IsZero() {
		ref.Model, ref.Effort = "", a.composerFile.Effort
		if m, _ := a.modelOf(a.model); strings.EqualFold(effortFor(m, ref.Effort), ref.Effort) {
			ref.Effort = effortFor(m, ref.Effort)
		}
	}
	return ref
}

// cliEffort is whether the picker in the draft t names no effort: it
// names no model (cliDecides), and the user chose no effort.
func (a *app) cliEffort(t thread) bool { return a.cliDecides(t) && a.composerFile.Effort == "" }

// modelRef is the model the pickers chose, for a new thread, at the
// effort they show (effortFor).
func (a *app) modelRef() api.ModelRef {
	m, _ := a.modelOf(a.model)
	ref := api.ModelRef{Provider: a.model.Provider, Model: a.model.ID, Effort: effortFor(m, a.effort)}
	ref.SetSpeed(a.speed())
	return ref
}

var accessOf = map[string]api.Access{accessAsk: api.AccessAsk, accessAuto: api.AccessAutoEdits, accessFull: api.AccessFull}
