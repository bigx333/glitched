package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
	"github.com/egoist/mygo/ui/markdown/parser"

	"glitched/internal/api"
	"glitched/internal/client"
)

const (
	contentWidth = 800
	headerHeight = 52
	// composerRoom is the room left under the timeline for the composer
	// floating over it.
	composerRoom = 200
	// columnLeft and columnRight are the room the chat's column leaves
	// either side of it, the turn markers' on the left: the transcript and
	// the composer over it share them, and so their edges.
	columnLeft  = markerColumnW + 12
	columnRight = 24
)

// focused is whether the window has the keyboard, as the Tester's does.
func (a *app) focused() bool { return a.win == nil || a.win.IsFocused() }

func (a *app) chatPage(c *ui.Context) {
	t := a.current()
	a.takeBack(c, t)
	// What the chat shows is read, up to the last turn its transcript
	// has, while the window has focus.
	if a.focused() {
		a.backend.MarkShown(t.Thread)
	}
	es := entriesOf(t)
	// The layout makes room for a panel at once, so the chat rewraps
	// once; the panel itself slides a little in from its edge as it fades.
	panel := func(x, y float32) ui.ElementTransition {
		if a.panelsJump {
			return ui.ElementTransition{}
		}
		return slideFrom(x, y)
	}
	ui.Row(c).Fill().AlignItems(ui.Stretch).Children(func() {
		chat := ui.Column(c).Key("chat").Grow(1).Shrink(1).MinWidth(minChatWidth)
		a.chatWidth = chat.Bounds().W
		chat.Children(func() {
			// The header is glass, the window's vibrancy as the sidebar's;
			// the chat is below it, on the background, and doesn't scroll
			// under it.
			a.header(c, t, es)
			pane := a.contentCard(ui.Box(c).Grow(1))
			a.paneAt = pane.Bounds()
			// Before the timeline and the composer, which it stages.
			a.dockMorph(pane, t)
			// Before the timeline, which leaves the strip's room as it moves.
			a.agentsMorph(pane, t)
			a.windBackground(c, pane)
			pane.Children(func() {
				if a.windDraw != nil && a.settings.wind {
					ui.Box(c).Key("wind").Absolute().Top(0).Left(0).Right(0).Bottom(0).PassThrough().Draw(a.windDraw)
				}
				a.ambient(c)
				a.timeline(c, t, es)
				// After the timeline, which says whether the end shows.
				a.composerMorph(pane, t)
				// What scrolls fades out under the composer.
				ui.Box(c).Absolute().Bottom(0).Left(0).Right(0).Height(composerRoom - 40).PassThrough().
					Material(glass.ScrollEdge{Bottom: true, Background: palette.bg})
				// Over the edge, which would take its clicks.
				a.jumpToLatest(c, t)
				// Before the composer, whose ⌘⏎ would send or stop.
				a.approvalKeys(c, pane, t, es)
				a.composer(c, t, es)
			})
			// A server's workspace isn't here for a shell. The diff panel,
			// opened from an edit, says its files are there.
			if a.showTerminal && t.local() {
				drawer := ui.Column(c).Key("terminal").Height(220).Transition(panel(0, 16))
				drawer.Children(func() { a.terminal(c, t.View.Workspace) })
				a.terminalFocused = drawer.FocusWithin()
			}
		})
		switch {
		case a.showDiff:
			ui.Column(c).Key("diff").Width(a.diffWidth()).Transition(panel(16, 0)).Children(func() { a.diffPanel(c, t, es) })
		case a.showPreview:
			ui.Column(c).Key("preview").Width(a.previewWidth()).Transition(panel(16, 0)).Children(func() { a.previewPanel(c) })
		case a.showPluginTab:
			ui.Column(c).Key("plugin-tab").Width(a.diffWidth()).Transition(panel(16, 0)).Children(func() { a.pluginTabPanel(c, t) })
		}
	})
}

// windBackground draws the wind's field of glyphs behind the chat: the
// pointer warms the air along its path and a click blows a ripple, while
// the chat's own controls still take them; typing keeps it blowing.
func (a *app) windBackground(c *ui.Context, pane ui.Element) {
	if !a.settings.wind {
		return
	}
	a.wind.SetStill(c.Preferences().ReduceMotion)
	if a.draft != a.typed {
		a.typed = a.draft
		a.wind.Wake()
	}
	dark := c.Theme().Dark
	// Drawn by a child, which the pane's rounded corner clips, as it
	// would not its own drawing.
	a.windDraw = func(p *ui.Painter, r ui.Rect) { a.wind.Draw(p, r, dark) }
	pane.HandleInput(func(ev ui.InputEvent) bool {
		switch ev.Kind {
		case ui.InputPointerMove:
			// A frame, to wake the texture as it rests.
			return a.wind.Move(ev.X, ev.Y)
		case ui.InputPointerDown:
			if ev.Button == 0 {
				a.wind.Tap(ev.X, ev.Y)
			}
		}
		return false
	})
}

// ambient is a faint glow of the accent at the top of the window, which
// the glass of the sidebar and the composer frost and bend. It mixes in
// sRGB: one color fading out looks the same in Oklab, which costs a power
// per pixel each time the hero composer's glow repaints over it.
func (a *app) ambient(c *ui.Context) {
	ui.Box(c).Absolute().Top(0).Left(0).Right(0).Height(420).PassThrough().LinearGradient(ui.LinearGradient{
		Stops: []ui.GradientStop{
			{Color: palette.accent.Alpha(0.16), At: 0},
			{Color: palette.accent.Alpha(0.04), At: 0.55},
			{Color: palette.accent.Alpha(0), At: 1},
		},
		Angle: 180,
	})
}

func (a *app) header(c *ui.Context, t thread, es []entry) {
	bar := c.TitleBar()
	// Clear of the traffic lights as the sidebar slides away from them.
	left := 16 + bar.Left*(1-a.sidebarOpen)
	// Narrow, beside a panel, the header keeps to icons, and never draws
	// past the chat's edge.
	narrow := a.chatWidth < 700
	// Clear of the window buttons on the right (Windows, Linux) unless a
	// panel takes that edge.
	right := float32(12)
	if !a.showDiff && !a.showPreview {
		right += bar.Right
	}
	head := ui.Row(c).Height(max(bar.Height, headerHeight)).Shrink(0).
		Padding(0, right, 0, left).Gap(8).AlignItems(ui.Center).ClipX()
	head.DragWindow().Children(func() {
		if !a.showSidebar {
			if a.iconButton(c, "show-sidebar", icPanelLeft, "Show sidebar  ⌘B", false).Clicked() {
				a.toggleSidebar(false)
			}
		}
		// A child's way back up, before its title.
		a.breadcrumb(c, t, narrow)
		ui.Text(c, title(t)).FontWeight(600).SingleLine().Shrink(1).MinWidth(80)
		if !narrow {
			pill(c, filepath.Base(t.View.Workspace)).Shrink(0)
			if len(a.backend.Envs()) > 1 && t.env != nil {
				a.machinePill(c, t.env)
			}
			a.usagePill(c, t)
		}
		if !t.id().IsZero() {
			a.pluginSlot(c, "thread.header", t.id().String(), &t)
		}
		ui.Spacer(c)
		a.tabButtons(c)
		if !t.local() {
			a.previewButton(c)
			return
		}

		a.tip(c, headerButton(c, "open").Shrink(0).Label("Open in"), "Open in").Children(func() {
			ui.Icon(c, icCode).FontSize(14)
			if !narrow {
				ui.Text(c, "Open").SingleLine()
			}
			ui.Icon(c, icChevronD).FontSize(11)
		}).Menu(func(m *ui.Menu) {
			m.Item("Cursor")
			m.Item("VS Code")
			m.Item("Zed")
			m.Separator()
			m.Item("Finder")
		})
		if a.tip(c, headerButton(c, "commit").Shrink(0).Label("Commit & push"), "Commit & push").Children(func() {
			ui.Icon(c, icUpload).FontSize(14)
			if !narrow {
				ui.Text(c, "Commit & push").SingleLine()
			}
		}).Clicked() {
			c.AddToast(ui.Toast{Title: "Pushed to origin", Description: filepath.Base(t.View.Workspace) + " · 2 files, +23 −2"})
		}
		ui.Row(c).Padding(2).Gap(2).Radius(10).Shrink(0).Children(func() {
			if a.iconButton(c, "terminal", icTerminal, "Toggle terminal  ⌘J", a.showTerminal).Clicked() {
				a.showTerminal, a.panelsJump = !a.showTerminal, false
			}
			// The diff's button counts the files the agent changed.
			ui.Box(c).Children(func() {
				if a.iconButton(c, "diff", icDiff, "Toggle diff  ⌘D", a.showDiff).Clicked() {
					a.showDiff, a.panelsJump = !a.showDiff, false
					if a.showDiff {
						a.showPreview = false
					}
				}
				if n := len(changedFiles(es)); n > 0 {
					ui.Textf(c, "%d", n).Absolute().Top(1).Right(1).FontSize(9).FontWeight(600).LineHeight(1.4).
						Padding(0, 4).Radius(999).Background(palette.accent).TextColor(ui.Hex("#ffffff")).PassThrough()
				}
			})
			a.previewButton(c)
		})
	})
}

func (a *app) previewButton(c *ui.Context) {
	if a.iconButton(c, "preview", icGlobe, "Toggle preview  ⇧⌘P", a.showPreview).Clicked() {
		a.togglePreview()
		a.panelsJump = false
	}
}

// headerButton is a button of the header's glass bar: a hairline pill,
// over the window's vibrancy rather than glass over the chat.
func headerButton(c *ui.Context, key any) ui.Element {
	return ghost(c, key, false).Padding(5, 11).Radius(999).Border(1, palette.border).TextColor(palette.text)
}

// glassButton is a pill of interactive glass, which swells as it's pressed.
func (a *app) glassButton(c *ui.Context, key any) ui.Element {
	b := ui.ButtonBase(c.Key(key)).Gap(6).AlignItems(ui.Center).Padding(6, 12).Radius(999).
		TextColor(palette.text).Cursor(ui.CursorPointer)
	return a.pane(b, glass.Glass{Interactive: true})
}

func pill(c *ui.Context, label string) ui.Element {
	return ui.Text(c, label).FontSize(11).TextColor(palette.muted).SingleLine().
		Padding(2, 7).Radius(999).Border(1, palette.border)
}

// listFor is the thread's list of entries, kept per thread so that each
// keeps where it was scrolled to.
func (a *app) listFor(id api.ID) *ui.ListState {
	l := a.lists[id]
	if l == nil {
		l = &ui.ListState{}
		a.lists[id] = l
	}
	return l
}

// timeline lists the thread's entries, as MyGo's markdown-chat does: the
// list builds only those in view, starts at the end and follows it as a
// reply streams in, gliding, until the user scrolls up; scrolling back to
// the end follows it again. Each entry is in the middle of a row as wide
// as the pane, so that the wheel scrolls it wherever the pointer is. It
// fades in as the composer docks from the hero; into a hero, the thread
// left fades out, sliding up, out of the pointer's way.
func (a *app) timeline(c *ui.Context, t thread, es []entry) {
	tr := transcriptK(a.dockK)
	from, leaving := a.departing(t)
	if leaving {
		t, es = from, entriesOf(from)
	}
	a.drew, a.drawn = a.drawn, map[api.ID]sight{}
	a.laidAt, a.builtAt = a.builtAt, a.now
	l := a.listFor(t.id())
	l.Key = func(i int) any { return es[i].key }
	l.FollowEnd = true
	l.FollowAnimation = &ui.Smooth
	// The agents strip sits over the composer: the end clears it too.
	room := composerRoom + a.agentsRoom(t)
	list := ui.List(c.Key(t.id()), l, len(es), func(i int) {
		ui.Row(c).Justify(ui.Center).Children(func() {
			ui.Column(c).Grow(1).MaxWidth(contentWidth).Children(func() { a.entry(c, t, es, i) })
		})
	})
	if leaving {
		list.Translate(0, -32*(1-tr)).PassThrough()
	}
	list.Fill().Padding(20, columnRight, room, columnLeft).Gap(22).Justify(ui.End).
		ScrollbarInsets(0, 0, room-40, 0).Opacity(tr).Children(func() {
		// The hero has its own greeting, over its composer.
		if len(es) == 0 && !hero(t) {
			ui.Row(c).Justify(ui.Center).Children(func() { a.emptyState(c) })
		}
	})
	a.turnMarkers(c, es, l)
}

// jumpToLatest floats a pill over the composer while the user has
// scrolled up from the end, which takes them back. It fades and rises in
// as the composer folds, and goes as it opens, riding the composer's top
// edge as it moves.
func (a *app) jumpToLatest(c *ui.Context, t thread) {
	j := a.jumpK
	if j == 0 {
		return
	}
	l := a.listFor(t.id())
	// Over the composer as it is in this frame, which is laid out after.
	bottom := composerBarBottom + a.composerHeight(c, t) + 26
	if hero(t) {
		return // nothing to jump to
	}
	// The open Agents panel floats over where it is, as it fades in with
	// the composer: it gives way to the panel as that comes, taking no
	// presses once mostly gone, and none of the pointer once gone, where
	// it shows past a panel lower than it.
	o := j
	if len(a.shown(t)) > 0 {
		o *= 1 - a.agentsK*fadeIn(a.composerK, 0.45, 0.55)
	}
	ui.Row(c).Absolute().Left(columnLeft).Right(columnRight).Bottom(bottom).Justify(ui.Center).PassThrough().Children(func() {
		b := faded(a.glassButton(c, "jump").Padding(5, 12).Translate(0, 8*(1-j)), o)
		a.cover(coverKey{kind: "jump"}, b)
		if o <= 0.001 {
			b.Invisible()
		}
		if b.Children(func() {
			ui.Icon(c, icArrowDown).FontSize(13)
			ui.Text(c, "Jump to latest").FontSize(12)
		}).Clicked() {
			l.ScrollToEnd()
		}
	})
}

func (a *app) emptyState(c *ui.Context) {
	ui.Column(c).Key("empty").Center().Gap(12).Padding(140, 0, 0).Children(func() {
		ui.Icon(c, icSparkles).FontSize(30).TextColor(palette.accent)
		ui.Text(c, "What should we build?").FontSize(18).FontWeight(600)
		ui.Text(c, "Send a message to start the conversation.").TextColor(palette.muted)
	})
}

// userMarkdown is a message the user typed as Markdown that shows as
// typed where Markdown would hide it: each line break within a
// paragraph, which Markdown reads as a space, is made a hard break, and
// each line read as a link's definition, which shows as nothing, has its
// bracket escaped. A line break within a code span stays a space, and
// code, quotes' markers and the rest are as typed. It is "" when the
// message is only blocks' marks, as "#" or ">", which show as nothing.
func userMarkdown(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	if strings.Trim(text, "#>-+*=_ \t\n") == "" {
		return ""
	}
	text = keepDefinitions(text)
	d := parser.New(parser.GFM)
	d.Set(text)
	var b strings.Builder
	at := 0
	for i := 1; i < d.Len(); i++ {
		k := d.Block(i)
		if k.Kind != parser.Paragraph {
			continue
		}
		var code [][2]int // the code spans' and HTML's bytes
		for _, in := range d.Inlines(i) {
			if in.Kind == parser.Code || in.Kind == parser.HTML {
				code = append(code, [2]int{in.Start, in.End})
			}
		}
		for j := k.Start; j < k.End; j++ {
			if text[j] == '\n' && !slices.ContainsFunc(code, func(r [2]int) bool { return r[0] <= j && j < r[1] }) {
				b.WriteString(text[at:j])
				b.WriteString("  ")
				at = j
			}
		}
	}
	b.WriteString(text[at:])
	return b.String()
}

// keepDefinitions escapes the first bracket of each line of text that
// no block shows, which, a link's definition, would show as nothing.
func keepDefinitions(text string) string {
	d := parser.New(parser.GFM)
	d.Set(text)
	var shown [][2]int // the bytes of the blocks that show text
	for i := 1; i < d.Len(); i++ {
		switch k := d.Block(i); k.Kind {
		case parser.Quote, parser.List, parser.Item, parser.Table, parser.TableRow, parser.FootnoteDefinition:
		default:
			shown = append(shown, [2]int{k.Start, k.End})
		}
	}
	var b strings.Builder
	at := 0
	for s := 0; s < len(text); {
		e := strings.IndexByte(text[s:], '\n')
		if e < 0 {
			e = len(text)
		} else {
			e += s
		}
		line := text[s:e]
		if strings.TrimSpace(line) != "" && !slices.ContainsFunc(shown, func(r [2]int) bool { return r[0] < e && r[1] > s }) {
			if k := strings.IndexByte(line, '['); k >= 0 {
				b.WriteString(text[at : s+k])
				b.WriteString("\\")
				at = s + k
			}
		}
		s = e + 1
	}
	b.WriteString(text[at:])
	return b.String()
}

// userMarkdown is userMarkdown(text) for the message keyed key, made
// again only when its text changes.
func (a *app) userMarkdown(key, text string) string {
	if m, ok := a.userMDs[key]; ok && m.text == text {
		return m.md
	}
	if a.userMDs == nil || len(a.userMDs) >= userMDsKept {
		a.userMDs = map[string]userMD{}
	}
	m := userMD{text, userMarkdown(text)}
	a.userMDs[key] = m
	return m.md
}

// userMDsKept is how many messages' Markdown userMarkdown keeps.
const userMDsKept = 512

// userMD is a message's text and the Markdown it shows as.
type userMD struct{ text, md string }

// bubbleInset is how far a message's text is inside its bubble's edges,
// across: its padding and its border.
const bubbleInset = 15

func (a *app) entry(c *ui.Context, t thread, es []entry, i int) {
	e := es[i]
	key := e.key
	switch e.kind {
	case entryUser:
		r := ui.Row(c).Key(key).Transition(riseIn)
		r.Children(func() {
			ui.Spacer(c)
			ui.Column(c).AlignItems(ui.End).Gap(6).MaxWidthPercent(80).Children(func() {
				// It reads as Markdown, as a reply does, its code wrapping
				// rather than scrolling within the bubble.
				ui.Column(c).Padding(10, bubbleInset-1).Radius(16, 16, 4, 16).
					Background(palette.surface).Border(1, palette.border).Children(func() {
					if md := a.userMarkdown(key, e.text); md != "" {
						markdown.View(c, md, &a.userMD)
					} else {
						ui.Text(c, e.text).Selectable().LineHeight(1.5)
					}
				})
				if e.note != "" {
					a.messageNote(c, t, e)
				}
			})
			if e.failed.Err == nil && !t.id().IsZero() {
				a.messageActions(c, t, es, i, r.Hovered())
			}
		})
	case entryAssistant:
		col := ui.Column(c).Key(key).LineHeight(1.55).Transition(riseIn)
		col.Children(func() {
			if e.streaming {
				markdown.Stream(c, e.text, &a.md)
			} else {
				markdown.View(c, e.text, &a.md)
			}
			a.messageActions(c, t, es, i, col.Hovered() && !e.streaming)
		})
	case entryWork:
		a.workLog(c, t, key, e, changedFiles(es))
	case entryRequest:
		a.requestCard(c, t, e)
	case entryReport:
		a.reportCard(c, t, key, e)
	case entryFailed, entryError:
		ui.Row(c).Key(key).Gap(8).AlignItems(ui.Center).Padding(6, 6, 6, 14).Radius(12).
			Border(1, palette.red.Alpha(0.35)).Transition(riseIn).Children(func() {
			ui.Text(c, e.text).FontSize(12).TextColor(palette.red).Grow(1).Shrink(1)
			if e.kind == entryFailed {
				a.failedActions(c, t, e.failed)
			}
		})
	}
}

// messageNote is under a message that hasn't simply been sent: queued,
// cancelled, or not sent.
func (a *app) messageNote(c *ui.Context, t thread, e entry) {
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		col := palette.faint
		if e.failed.Err != nil {
			col = palette.red
		}
		ui.Text(c, e.note).FontSize(11.5).TextColor(col)
		if e.failed.Err != nil {
			a.failedActions(c, t, e.failed)
		}
	})
}

// failedActions are a failed command's buttons: Retry, unless sending it
// again can only fail the same way, and Dismiss. Retry waits while what
// refused the command holds, as a live run does a delete.
func (a *app) failedActions(c *ui.Context, t thread, p client.Pending) {
	_, del := p.Body.(api.ThreadDelete)
	if !p.Terminal() && ghost(c, "retry", false).Padding(2, 8).Disabled(del && t.View.Live()).Children(func() {
		ui.Icon(c, icRotate).FontSize(12)
		ui.Text(c, "Retry").FontSize(11.5)
	}).Clicked() {
		a.backend.Retry(t.env, t.id(), p.Command)
	}
	if ghost(c, "dismiss", false).Padding(2, 8).Children(func() {
		ui.Icon(c, icX).FontSize(12)
		ui.Text(c, "Dismiss").FontSize(11.5)
	}).Clicked() {
		a.backend.Dismiss(t.env, t.id(), p.Command)
	}
}

func (a *app) workLog(c *ui.Context, t thread, key string, e entry, files []fileChange) {
	working := e.working
	open := !a.collapsed[key]
	ui.Column(c).Key(key).Radius(12).Border(1, palette.border).Background(palette.bg.Alpha(0.6)).Clip().
		Transition(riseIn).Children(func() {
		if ghost(c, "head", false).Radius(0).Padding(8, 12).Children(func() {
			switch {
			case e.paused != "":
				ui.Icon(c, icClock).FontSize(12).TextColor(palette.amber)
				ui.Text(c, e.paused).FontSize(12).Grow(1)
			case working:
				a.statusMark(c, statusWorking)
				shimmerText(c, e.took, 12, palette.muted, palette.text)
			default:
				ui.Text(c, e.took).FontSize(12).Grow(1)
			}
			ui.Text(c, plural(len(e.work), "step")).FontSize(11).TextColor(palette.faint)
			chevron(c, open)
		}).Clicked() {
			a.collapsed[key] = open
		}
		if !open {
			return
		}
		ui.Column(c).Key("steps").Padding(6, 8, 10).Gap(2).BorderWidth(1, 0, 0, 0).BorderColor(palette.border).
			ClipY().Transition(fold).Children(func() {
			for _, w := range e.work {
				// Each step the agent takes unfolds below the last.
				a.step(c, t, files, w)
			}
		})
	})
}

// step is one of the agent's steps. A command that finished shows its
// exit code and how long it took, and what it printed when it failed. An
// edit of a file the diff lists opens it there.
func (a *app) step(c *ui.Context, t thread, files []fileChange, w client.WorkStep) {
	icon := icFile
	switch w.Kind {
	case "command":
		icon = icTerminal
	case "edit":
		icon = icPencil
	case "search":
		icon = icSearch
	}
	file := -1
	if w.Kind == "edit" && w.Path != "" {
		file = slices.IndexFunc(files, func(f fileChange) bool { return f.path == w.Path })
	}
	var r ui.Element
	if file >= 0 {
		r = ghost(c, w.Item, false).Padding(2, 4).Radius(5).Gap(8).ClipY().Transition(fold)
	} else {
		r = ui.Row(c).Key(w.Item).Padding(2, 4).Radius(5).Gap(8).AlignItems(ui.Center).ClipY().Transition(fold)
	}
	if r.Children(func() {
		ui.Icon(c, icon).FontSize(12).TextColor(palette.faint)
		ui.Text(c, w.Title).FontSize(12).SingleLine().Shrink(1).TextColor(palette.text)
		if file >= 0 {
			add, del := a.diffStats(t, files[file])
			counts(c, add, del)
		}
		ui.Spacer(c)
		switch {
		case w.Status == api.WorkStarted:
			a.statusMark(c, statusWorking)
		case w.Exit != nil:
			mark, color := "✓", palette.green
			if *w.Exit != 0 {
				mark, color = "✗", palette.red
			}
			ui.Textf(c, "%s %d", mark, *w.Exit).FontSize(11.5).Font("monospace").TextColor(color)
			ui.Text(c, "· "+tookFor(w.Ended.Sub(w.Started))).FontSize(11.5).Font("monospace").TextColor(palette.faint)
		case w.Status == api.WorkFailed:
			a.tip(c, ui.Icon(c, icX).FontSize(12).TextColor(palette.red), "Failed")
		case file >= 0 && r.Hovered():
			ui.Text(c, "Open in diff").FontSize(11.5).TextColor(palette.muted)
			ui.Icon(c, icExternal).FontSize(11).TextColor(palette.muted)
		}
	}).Clicked() && file >= 0 {
		a.showDiff, a.showPreview, a.panelsJump = true, false, false
		a.diffFile[t.id()] = w.Path
	}
	if w.Output != "" && (w.Status == api.WorkFailed || w.Exit != nil && *w.Exit != 0) {
		ui.Text(c, w.Output).Key(w.Item+"/output").Selectable().FontSize(11.5).Font("monospace").LineHeight(1.5).TextColor(palette.muted).
			Margin(2, 0, 4, 24).Padding(6, 10).Radius(6).Background(palette.bg).Border(1, palette.border)
	}
}

// tookFor is how long a step took, to a tenth of a second.
func tookFor(d time.Duration) string {
	return max(d, 0).Round(100 * time.Millisecond).String()
}

// plural counts things: "1 step", "2 steps".
func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return fmt.Sprintf("%d %ss", n, what)
}

func counts(c *ui.Context, add, del int) {
	if add > 0 {
		ui.Textf(c, "+%d", add).FontSize(11.5).Font("monospace").TextColor(palette.green)
	}
	if del > 0 {
		ui.Textf(c, "−%d", del).FontSize(11.5).Font("monospace").TextColor(palette.red)
	}
}

// composerMorph moves the composer on its way between compact and full,
// on host, for the frame: getting to the end of the thread opens it,
// scrolling up folds it, and either, mid-way, turns it round from where it
// is. Jump to latest comes and goes with the end. With less motion asked
// for, both are there at once (AnimateTo).
func (a *app) composerMorph(host ui.Element, t thread) {
	id := t.id()
	// A thread's list starts at its end, which it only knows once laid
	// out: until then the composer is full, rather than folding at first.
	if !a.composerSeen[id] {
		a.composerSeen[id] = true
		a.composerRest, a.composerK, a.jumpK = false, 1, 0
		return
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
	if a.listFor(t.id()).AtEnd() {
		a.composerOpened = false
		return false
	}
	return !a.composerOpened && strings.TrimSpace(a.draft) == "" && len(a.attachments) == 0
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
	if len(a.shown(t)) > 0 {
		h += a.aboveH * k
	}
	return h + a.envH*k
}

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
func (a *app) composer(c *ui.Context, t thread, es []entry) {
	compact := a.composerCompact(t)
	k := a.composerK
	morphing := k > 0 && k < 1
	lerp := func(from, to float32) float32 { return from + (to-from)*k }
	compactH, fullH := a.cardHeights(c)
	// The bar lets the pointer through, but for the composer in it, to
	// what is around it, as the pill taking the user to the end.
	// d is how far it is from the hero to its dock (hero.go). Docked, it's
	// in the transcript's column, its edges the column's; the hero, with
	// no transcript nor turn markers, is in the middle of the pane, the
	// marker column's room coming in as the transcript does.
	d := a.dockK
	left := columnRight + (columnLeft-columnRight)*transcriptK(d)
	bar := ui.Column(c).Key("composer-bar").Absolute().Bottom(a.heroLift()*(1-d)).Left(0).Right(0).AlignItems(ui.Center).
		Padding(0, columnRight, composerBarBottom, left).PassThrough()
	// The full composer's width at rest, as the last frame had room for:
	// the full card's, in its border, or 0 before there's been a frame.
	var fullW float32
	if w := bar.Bounds().W - left - columnRight; w > 0 {
		fullW = max(0, min(w, contentWidth)-2*a.paneBorder())
	}
	bar.Children(func() {
		frame := ui.Column(c).Key("composer-frame").WidthPercent(100).MaxWidth(lerp(compactWidth, contentWidth)).PassThrough().
			Bind(&a.composerH)
		frame.Children(func() {
			a.heroHeading(c, d)
			// The agents strip sits over the card, the room it takes going
			// with it as it slides down into it.
			if k > 0 && len(a.shown(t)) > 0 {
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
					in.Children(func() { a.agents(c, t, frame.Bounds().W, s) })
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
			a.accessEffects(card)
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
						ui.TextInput(c.Key("draft-compact"), &a.draft).Placeholder("Ask a follow-up…").
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
				ahead := k == 0 && (a.composerOpened || strings.TrimSpace(a.draft) != "" || len(a.attachments) > 0)
				if k == 0 && !ahead {
					// Compact, there's no Send to stop the agent with: ⌘⏎
					// still does, as it does from the full composer.
					if t.View.Live() && a.message(t) == "" && c.Shortcut(ui.Cmd, ui.KeyEnter) {
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
					switch r := waitingRequest(t, es); {
					case r != nil && r.Kind == api.KindQuestion:
						placeholder = "Reply to steer, or answer the questions above"
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
					area := ui.TextArea(c.Key(key), &a.draft).Placeholder(placeholder).
						Lines(2, 10).Background(ui.Transparent).Border(0, ui.Transparent).FocusRing(false).
						Padding(14, 16, 4).OnPaste(a.onPaste).OnTextKey(a.draftKey(t)).Opacity(in)
					if !ahead {
						area.AutoFocus().Bind(&a.draftArea)
					}
					if large {
						area.Grow(1)
					}
					in = fadeIn(k, 0.35, 0.5)
					faded(ui.Row(c).Key("controls").Padding(6, 8, 8).Gap(2).AlignItems(ui.Center).
						Translate(0, 6*(1-in)), in).Children(func() {
						a.composerControls(c)
						ui.Spacer(c)
						if t.local() && a.iconButton(c, "attach", icPaperclip, "Attach files", false).Clicked() {
							a.pickFiles()
						}
						a.sendButton(c, t, !ahead)
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
				env.Children(func() { a.workspaceButton(c, t) })
			})
		})
	})
}

// workspaceButton is where the thread runs: its workspace, and its machine
// when there are several. A draft's is a menu of where it could start.
func (a *app) workspaceButton(c *ui.Context, t thread) {
	several := len(a.backend.Envs()) > 1
	b := ghost(c, "workspace", false).Padding(3, 6).Label("Workspace").Children(func() {
		ui.Icon(c, icFolder).FontSize(12)
		ui.Text(c, filepath.Base(t.View.Workspace)).FontSize(11.5)
		if several && t.env != nil {
			ui.Text(c, "on "+t.env.Name).FontSize(11.5).TextColor(palette.faint)
		}
		if t.id().IsZero() {
			ui.Icon(c, icChevronD).FontSize(10)
		}
	})
	if !t.id().IsZero() {
		return
	}
	b.Menu(func(m *ui.Menu) {
		for _, e := range a.backend.Envs() {
			if a.duplicate(e) {
				continue
			}
			home := homeDir()
			if e.Kind == client.Remote {
				home = "~"
			}
			for _, d := range a.workspacesOn(e, home) {
				label := filepath.Base(d.workspace)
				if several {
					label += " on " + e.Name
				}
				if m.Item(label).Checked(t.env == e && t.View.Workspace == d.workspace).Chosen() {
					a.fresh = d
				}
			}
		}
		a.workspaceItems(m)
	})
}

// workspacesOn are where a draft on e could start: the workspaces of e's
// threads, and home.
func (a *app) workspacesOn(e *client.Env, home string) []draft {
	ds := []draft{{e, home}}
	for _, p := range a.projects {
		if p.env == e && p.key.workspace != home {
			ds = append(ds, draft{e, p.key.workspace})
		}
	}
	return ds
}

// sendButton presses in as it's pressed; its color says whether there is
// anything to send. While the agent works, a message steers it, and with
// none the button stops it, as it ends a run left uncertain. Holding a
// message, the composer keeps a Stop of its own beside it, so a draft
// never hides the only way to stop a run. Without keys, it takes no ⌘⏎:
// laid out unseen, it's no more there to the keyboard than to the pointer.
func (a *app) sendButton(c *ui.Context, t thread, keys bool) {
	empty := a.message(t) == ""
	live := t.View.Live()
	working := live && empty
	if live && !empty {
		stop := a.tip(c, ui.ButtonBase(c.Key("stop")).Label("Stop"), "Stop the agent").Size(30, 30).Radius(15).Center().
			Cursor(ui.CursorPointer).Border(1, palette.border).Margin(0, 4, 0, 0)
		if stop.Hovered() {
			stop.Background(palette.hover)
		}
		if stop.Children(func() { ui.Box(c).Size(10, 10).Radius(2).Background(palette.text) }).Clicked() {
			a.backend.Interrupt(t.env, t.id())
		}
	}
	label := "Send"
	if working {
		label = "Stop"
	}
	send := ui.ButtonBase(c.Key("send")).Label(label).Size(30, 30).Radius(15).Center().
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
	if send.Clicked() || keys && c.Shortcut(ui.Cmd, ui.KeyEnter) {
		if working {
			a.backend.Interrupt(t.env, t.id())
		} else {
			a.send(t)
		}
	}
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

func (a *app) composerControls(c *ui.Context) {
	a.modelPicker(c)
	ui.Box(c).Size(1, 16).Background(palette.border).Margin(0, 4)

	a.accessPicker(c)
}

// send sends the draft in the thread, creating it first when it's the
// draft. A snoozed thread comes back to the inbox with it, as the engine
// brings a settled one back with any message.
func (a *app) send(t thread) {
	text := a.message(t)
	if text == "" || t.env == nil {
		return
	}
	id := t.id()
	if id.IsZero() {
		title := text
		if r := []rune(title); len(r) > 48 {
			title = string(r[:48]) + "…"
		}
		if id = a.backend.Create(t.env, t.View.Workspace, title, a.modelRef(), accessOf[a.access]); id.IsZero() {
			return
		}
		if hero(t) {
			a.sentFromHero(id)
		}
		a.selected = id
		a.follow()
	} else if t.View.Disposition.State == api.Snoozed {
		a.backend.SetDisposition(t.env, id, api.Disposition{})
	}
	// Steer only: a message reaches a working agent now, rather than
	// waiting its turn.
	cmd := a.backend.Send(t.env, id, text, api.SteerActive)
	if cmd.IsZero() {
		return
	}
	a.steers[cmd] = steer{t.env, id}
	a.draft = ""
	a.attachments = slices.DeleteFunc(a.attachments, func(at attachment) bool { return at.held(t) == "" })
	a.listFor(id).ScrollToEnd()
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
		if !errors.As(p.Err, &rej) || s.env != t.env || s.thread != t.id() || a.message(t) != "" {
			continue // landed, or kept in the timeline
		}
		a.backend.Dismiss(s.env, s.thread, cmd)
		a.draft = p.Body.(api.MessageDispatch).Text
		c.AddToast(ui.Toast{Title: "Not sent", Description: rej.Reason, Type: "info"})
	}
}

// message is the draft and what's attached to it that goes to t's agent:
// files as their paths, as the composer's @ puts them, and pasted text
// after.
func (a *app) message(t thread) string {
	parts := []string{strings.TrimSpace(a.draft)}
	for _, at := range a.attachments {
		if at.held(t) != "" {
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

// modelRef is the model the pickers chose, for a new thread.
func (a *app) modelRef() api.ModelRef {
	m := modelByName(a.model)
	return api.ModelRef{Provider: strings.ToLower(m.provider), Model: m.name, Effort: strings.ToLower(a.effort), Fast: a.fast}
}

var accessOf = map[string]api.Access{accessAsk: api.AccessAsk, accessAuto: api.AccessAutoEdits, accessFull: api.AccessFull}

func (a *app) terminal(c *ui.Context, workspace string) {
	s, err := a.shellFor(workspace)
	title := "zsh"
	if s != nil {
		title = s.Title()
	}
	// The drawer is the terminal's colors, which are the theme's.
	th := a.theme.terminal()
	ui.Column(c).Height(220).Background(th.Background).BorderWidth(1, 0, 0, 0).BorderColor(palette.border).Children(func() {
		ui.Row(c).Padding(4, 8, 4, 12).Gap(6).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(palette.border).Children(func() {
			ui.Icon(c, icTerminal).FontSize(12).TextColor(palette.muted)
			ui.Text(c, title).FontSize(11.5).TextColor(palette.muted).SingleLine().Shrink(1)
			ui.Spacer(c)
			if a.iconButton(c, "close-terminal", icX, "Close terminal", false).Padding(3).Clicked() {
				a.showTerminal = false
			}
		})
		a.terminalBody(c, s, err)
	})
}

// cannedTerminal is the drawer's output where no shell runs, as in tests,
// in the terminal's colors: its text, its dim text and its green.
func (a *app) cannedTerminal(c *ui.Context) {
	th := a.theme.terminal()
	text, dim, green := th.Foreground, th.Foreground.Alpha(0.6), th.Palette[2]
	ui.Scroll(c).Grow(1).Padding(8, 12).Gap(2).Font("monospace").FontSize(12).Children(func() {
		for _, l := range []struct {
			text string
			col  ui.Color
		}{
			{"~/Projects/t3code (t3code/sidebar-sort) $ bun run typecheck", text},
			{"$ tsc --noEmit -p apps/web", dim},
			{"✓ No type errors", green},
			{"~/Projects/t3code (t3code/sidebar-sort) $ bun run test -- Sidebar", text},
			{" ✓ Sidebar > sorts threads by updatedAt (4ms)", green},
			{" ✓ Sidebar > collapses a project (2ms)", green},
			{" Test Files  1 passed (1)", dim},
		} {
			ui.Text(c, l.text).TextColor(l.col).NoWrap()
		}
		ui.Row(c).Children(func() {
			ui.Text(c, "~/Projects/t3code (t3code/sidebar-sort) $ ").TextColor(text).NoWrap()
			caret := ui.Box(c).Size(7, 14)
			caret.Background(text).Opacity(caret.Loop("blink", time.Second, blink))
		})
	})
}

func blink(t float32) float32 {
	if t < 0.5 {
		return 1
	}
	return 0
}
