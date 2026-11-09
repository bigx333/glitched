package main

import (
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
)

// ghost is a borderless button that only shows a face while hovered, or
// while on.
func ghost(c *ui.Context, key any, on bool) ui.Element {
	b := ui.ButtonBase(c.Key(key)).Gap(6).AlignItems(ui.Center).Justify(ui.Start).Padding(5, 8).Radius(6).
		TextColor(palette.muted).Background(ui.Transparent).Cursor(ui.CursorPointer).Transition(hoverFade)
	switch {
	case on:
		b.Background(palette.active).TextColor(palette.text)
	case b.Hovered():
		b.Background(palette.hover).TextColor(palette.text)
	}
	return b
}

// row is a list row: selection shows at once, hover fades.
func row(c *ui.Context, key any, selected bool) ui.Element {
	b := ui.ButtonBase(c.Key(key)).Gap(8).AlignItems(ui.Center).Justify(ui.Start).Padding(6, 8).Radius(7).
		TextColor(palette.muted).Background(ui.Transparent).Cursor(ui.CursorPointer)
	switch {
	case selected:
		b.Background(palette.active).TextColor(palette.text)
	case b.Hovered():
		b.Background(palette.hover).TextColor(palette.text).Transition(hoverFade)
	default:
		b.Transition(hoverFade)
	}
	return b
}

// iconButton is a square ghost button showing only an icon.
func (a *app) iconButton(c *ui.Context, key any, icon *ui.SVG, label string, on bool) ui.Element {
	return a.tip(c, ghost(c, key, on).Padding(6).Label(label), label).Children(func() {
		ui.Icon(c, icon).FontSize(15)
	})
}

// shortcutHint shows a shortcut's keycaps only while ⌘ is held, as the
// app's commands are found: out of the way otherwise. They show and go at
// once, as the key does.
func shortcutHint(c *ui.Context, keys ...string) {
	if c.Modifiers()&ui.Cmd == 0 {
		return
	}
	kbd(c, keys...)
}

// kbd is a key of a shortcut, as a keycap.
func kbd(c *ui.Context, keys ...string) {
	ui.Row(c).Gap(3).Children(func() {
		for _, k := range keys {
			ui.Text(c, k).FontSize(10.5).TextColor(palette.muted).MinWidth(18).TextAlign(ui.Center).
				Padding(1, 4).Radius(4).Border(1, palette.border).Background(palette.hover)
		}
	})
}

// sidebarFrame holds the sidebar, which slides out to the left as the
// frame narrows, keeping its own width so that it doesn't reflow. It shows
// the window's vibrancy, the desktop blurred through it, or a solid fill
// where there is none.
func (a *app) sidebarFrame(c *ui.Context) {
	w := a.sidebarWidth * a.sidebarOpen
	ui.Row(c).Key("sidebar").Width(w).Shrink(0).AlignItems(ui.Stretch).Clip().Children(func() {
		side := ui.Column(c).Width(a.sidebarWidth).Shrink(0).Margin(0, 0, 0, w-a.sidebarWidth)
		switch {
		case !a.vibrant:
			side.Background(palette.sidebar)
		default:
			// The app's background tints the material, all but what the
			// vibrancy setting lets through: near black in the dark, the
			// desktop still blurring through.
			side.Background(a.glassTint())
		}
		if a.sidebarOpen == 0 {
			side.Invisible()
		}
		side.Children(func() {
			// The material is lighter than the app's background: the
			// faintest text takes the muted color to stay legible on it.
			if a.vibrant {
				faint := palette.faint
				palette.faint = palette.muted
				defer func() { palette.faint = faint }()
			}
			a.sidebar(c)
		})
		a.sidebarEdge(c)
	})
}

// The sidebar's width, as dragged: enough for a title, never half the
// window.
const (
	minSidebarWidth = 220
	maxSidebarWidth = 440
)

// sidebarEdge is the sidebar's right edge, which the pointer drags to
// resize it.
func (a *app) sidebarEdge(c *ui.Context) {
	edge := dragEdge(c, "edge", "Resize sidebar").Right(0)
	dx, _, ok := edge.Dragged()
	a.sidebarPressed = ok
	if ok && dx != 0 {
		a.sidebarWidth = min(max(a.sidebarWidth+dx, minSidebarWidth), maxSidebarWidth)
		a.sidebarHeld = true
	}
	if _, _, ok := edge.DragEnded(); ok {
		a.sidebarHeld = false
	}
}

// dragEdge is a pane's edge, along its height, which the pointer drags to
// resize it, with the resize cursor. It lights up as it's pointed at.
func dragEdge(c *ui.Context, key, label string) ui.Element {
	edge := ui.Box(c).Key(key).Absolute().Top(0).Bottom(0).Width(5).Cursor(ui.CursorResizeEW).Label(label)
	if edge.Hovered() || edge.Pressed() {
		edge.Background(palette.accent.Alpha(0.5))
	}
	return edge
}

func (a *app) sidebar(c *ui.Context) {
	bar := c.TitleBar()
	inSettings := strings.HasPrefix(a.router.Path(), "/settings")
	// Title bar: room for the traffic lights, which sit on the glass, the
	// wordmark, and a button hiding the sidebar.
	ui.Row(c).Height(max(bar.Height, headerHeight)).Padding(0, 10, 0, max(bar.Left, 12)).
		AlignItems(ui.Center).Gap(6).DragWindow().Children(func() {
		ui.Spacer(c)
		if a.iconButton(c, "hide-sidebar", icPanelLeft, "Hide sidebar  ⌘B", false).Clicked() {
			a.toggleSidebar(false)
		}
	})
	if inSettings {
		a.settingsNav(c)
	} else {
		a.threadsNav(c)
	}
}

func (a *app) toggleSidebar(keyboard bool) {
	a.showSidebar = !a.showSidebar
	a.sidebarJump = keyboard
}

// statusMark shows what a thread is doing, before its title. Only work
// in progress moves.
func (a *app) statusMark(c *ui.Context, s status) {
	switch s {
	case statusWorking:
		spin := ui.Icon(c, icLoader).FontSize(11).TextColor(palette.blue)
		spin.Rotate(spin.Loop("spin", 800*time.Millisecond, ui.Linear) * 360)
	case statusApproval:
		a.pendingMark(c)
	case statusCompleted:
		a.tip(c, ui.Box(c).Size(7, 7).Radius(4).Background(palette.green).Margin(0, 2), "Completed")
	}
}

// pendingMark is the amber dot of a thread waiting on an approval.
func (a *app) pendingMark(c *ui.Context) {
	a.tip(c, ui.Box(c).Size(7, 7).Radius(4).Background(palette.amber).Margin(0, 2), "Waits on you")
}

var settingsSections = []struct {
	path, label string
	icon        *ui.SVG
	color       ui.Color
}{
	{"general", "General", icSliders, ui.Hex("#94a3b8")},
	{"appearance", "Appearance", icPalette, ui.Hex("#f472b6")},
	{"providers", "Providers", icPlug, ui.Hex("#4ade80")},
	{"machine", "This machine", icGlobe, ui.Hex("#2dd4bf")},
	{"servers", "Servers", icServer, ui.Hex("#38bdf8")},
	{"keybindings", "Keybindings", icKeyboard, ui.Hex("#60a5fa")},
	{"about", "About", icInfo, ui.Hex("#a78bfa")},
}

func (a *app) settingsNav(c *ui.Context) {
	ui.Column(c).Padding(0, 8, 8).Gap(1).Children(func() {
		if ghost(c, "back", false).Padding(7, 8).Children(func() {
			ui.Icon(c, icArrowLeft).FontSize(15)
			ui.Text(c, "Back to app")
		}).Clicked() {
			a.router.Push("/")
		}
		ui.Text(c, "SETTINGS").FontSize(10.5).Bold().LetterSpacing(0.8).TextColor(palette.faint).Padding(14, 8, 6)
		for _, s := range settingsSections {
			path := "/settings/" + s.path
			if row(c, s.path, a.router.Path() == path).Padding(7, 8).Children(func() {
				ui.Icon(c, s.icon).FontSize(15).TextColor(s.color)
				ui.Text(c, s.label)
			}).Clicked() {
				a.router.Replace(path)
			}
		}
	})
}

// newThread shows the draft, in the shown thread's workspace, as the hero,
// its text area taking the focus from wherever the press left it.
func (a *app) newThread() {
	t := a.current()
	a.startIn(draft{t.env, t.View.Workspace})
	a.draftArea.Focus()
}

// startThread shows the draft, in the project's workspace.
func (a *app) startThread(p *project) { a.startIn(draft{p.env, p.key.workspace}) }

func (a *app) startIn(d draft) {
	a.leaveFor()
	a.fresh = d
	a.selected, a.dockJump = api.ID{}, false
	a.follow()
	if a.router.Path() != "/" {
		a.router.Push("/")
	}
}
