package main

import (
	"strings"
	"time"

	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"

	"glitched/internal/plugins"
	"glitched/internal/plugins/widget"
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
	rs := a.rowsOf(t)
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
			a.header(c, t, rs.files)
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
				laid := a.timeline(c, t, rs)
				// After the timeline, whose replies note where the
				// selection is.
				a.selectionBar(c, t)
				// After the timeline, which says whether the end shows.
				a.composerMorph(pane, t, laid)
				// What scrolls fades out under the composer.
				ui.Box(c).Absolute().Bottom(0).Left(0).Right(0).Height(composerRoom - 40).PassThrough().
					Material(glass.ScrollEdge{Bottom: true, Background: palette.bg})
				// Over the edge, which would take its clicks.
				a.jumpToLatest(c, t)
				// Before the composer, whose ⌘⏎ would send or stop.
				a.approvalKeys(c, pane, t, rs)
				a.composer(c, t, rs)
			})
			// A server's workspace isn't here for a shell. The diff panel,
			// opened from an edit, says its files are there.
			if a.showTerminal && t.local() {
				dir := t.View.Workspace
				if a.folderless(t) {
					dir = homeDir()
				}
				drawer := ui.Column(c).Key("terminal").Height(220).Transition(panel(0, 16))
				drawer.Children(func() { a.terminal(c, t, dir) })
				a.terminalFocused = drawer.FocusWithin()
			}
		})
		switch {
		case a.showDiff:
			ui.Column(c).Key("diff").Width(a.diffWidth()).Transition(panel(16, 0)).Children(func() { a.diffPanel(c, t, rs.files) })
		case a.showPreview:
			ui.Column(c).Key("preview").Width(a.previewWidth()).Transition(panel(16, 0)).Children(func() { a.previewPanel(c) })
		case a.showPluginTab:
			ui.Column(c).Key("plugin-tab").Width(a.diffWidth()).Transition(panel(16, 0)).Children(func() { a.pluginTabPanel(c, t) })
		default:
			if sel, w, ok := a.subagentShown(t); ok {
				ui.Column(c).Key("subagent").Width(a.diffWidth()).Transition(panel(16, 0)).Children(func() { a.subagentPanel(c, t, sel, w) })
			}
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
	if typed := a.typedText(); typed != a.typed {
		a.typed = typed
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

type terminalPanel struct {
	Thread plugins.ThreadRecord `json:"thread"`
	Part   string               `json:"part"`
	Title  string               `json:"title"`
	Error  bool                 `json:"error,omitempty"`
}

func (a *app) terminal(c *ui.Context, t thread, workspace string) {
	s, err := a.shellFor(workspace)
	title := "zsh"
	if s != nil {
		title = s.Title()
	}
	// The drawer is the terminal's colors, which are the theme's.
	th := a.theme.terminal()
	ui.Column(c).Height(220).Background(th.Background).BorderWidth(1, 0, 0, 0).BorderColor(palette.border).Children(func() {
		key := composerKey(t)
		ctx := func(part string) func(plugins.ThreadRecord) any {
			return func(r plugins.ThreadRecord) any {
				return terminalPanel{Thread: r, Part: part, Title: title, Error: err != nil}
			}
		}
		a.pluginComponent(c, "thread.panel.terminal", &t, componentCopy{
			key: key + "/header", box: func() ui.Element { return ui.Row(c).Shrink(1) }, context: ctx("header"),
			native: func(*widget.Node) { a.terminalHeader(c, title) },
		})
		a.pluginComponent(c, "thread.panel.terminal", &t, componentCopy{
			key: key + "/body", box: func() ui.Element { return ui.Column(c).Grow(1).Shrink(1).Basis(0).FillWidth() }, context: ctx("body"),
			native: func(*widget.Node) { a.terminalBody(c, s, err) },
		})
	})
}

func (a *app) terminalHeader(c *ui.Context, title string) {
	ui.Row(c).Padding(4, 8, 4, 12).Gap(6).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(palette.border).Children(func() {
		ui.Icon(c, icTerminal).FontSize(12).TextColor(palette.muted)
		ui.Text(c, title).FontSize(11.5).TextColor(palette.muted).SingleLine().Shrink(1)
		ui.Spacer(c)
		if a.iconButton(c, "close-terminal", icX, "Close terminal", false).Padding(3).Clicked() {
			a.showTerminal = false
		}
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

// threadTitle is the title of a thread started with text: about, what
// text says without the quotes before it, when it says anything else.
func threadTitle(text, about string) string {
	title := strings.TrimSpace(about)
	if title == "" {
		title = text
	}
	if r := []rune(title); len(r) > 48 {
		title = string(r[:48]) + "…"
	}
	return title
}

// b2i is 1 for true and 0 for false.
func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
