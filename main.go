// T3 Code, rebuilt in MyGo's native UI as a clickable prototype: the
// projects and threads sidebar, the chat with its work log, the composer,
// the diff panel, a command palette and the settings. Everything is mock
// data; replies are canned and streamed in to show the motion.
package main

import (
	"log"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"github.com/egoist/mygo/ui/markdown"

	"t3-mygo/wind"
)

type colors struct {
	bg, sidebar, surface, hover, active, border, text, muted, faint ui.Color
	accent, green, red, amber, blue                                 ui.Color
}

var (
	darkColors = colors{
		bg:      ui.Hex("#0c0c0e"),
		sidebar: ui.Hex("#111113"),
		surface: ui.Hex("#18181b"),
		hover:   ui.RGBA(255, 255, 255, 0.05),
		active:  ui.RGBA(255, 255, 255, 0.09),
		border:  ui.RGBA(255, 255, 255, 0.08),
		text:    ui.Hex("#ececf0"),
		muted:   ui.Hex("#8e8e98"),
		faint:   ui.Hex("#5c5c66"),
		green:   ui.Hex("#4ade80"),
		red:     ui.Hex("#f87171"),
		amber:   ui.Hex("#fbbf24"),
		blue:    ui.Hex("#60a5fa"),
	}
	lightColors = colors{
		bg:      ui.Hex("#ffffff"),
		sidebar: ui.Hex("#f6f6f7"),
		surface: ui.Hex("#ffffff"),
		hover:   ui.RGBA(0, 0, 0, 0.04),
		active:  ui.RGBA(0, 0, 0, 0.07),
		border:  ui.RGBA(0, 0, 0, 0.09),
		text:    ui.Hex("#18181b"),
		muted:   ui.Hex("#71717a"),
		faint:   ui.Hex("#a1a1aa"),
		green:   ui.Hex("#16a34a"),
		red:     ui.Hex("#dc2626"),
		amber:   ui.Hex("#d97706"),
		blue:    ui.Hex("#2563eb"),
	}
	accents = []struct {
		name  string
		color ui.Color
	}{
		{"Violet", ui.Hex("#8b7cf6")},
		{"Blue", ui.Hex("#3b82f6")},
		{"Teal", ui.Hex("#14b8a6")},
		{"Orange", ui.Hex("#f97316")},
		{"Pink", ui.Hex("#ec4899")},
	}
	// palette is the colors of the frame being built, set from the
	// settings at the start of each.
	palette = darkColors
)

// settings is what the settings pages edit. Most of it is only shown.
type settings struct {
	theme         int // System, Light, Dark
	accent        int
	glass         bool
	vibrancy      float64 // 0 to 1: how much of the sidebar's vibrancy shows through the background
	wind          bool    // the wind's field of glyphs behind the chat
	fontSize      float64
	timeFormat    string
	confirmDelete bool
	streaming     bool
	newThreadMode int // Local, Worktree
	notifications bool

	claudeEnabled bool
	claudePath    string
	claudeModel   string
	claudeCustom  []string
	codexEnabled  bool
	codexPath     string
	codexModel    string
	codexCustom   []string

	checking bool
}

type app struct {
	win      *mygo.Window
	router   *ui.Router
	projects []*project
	archived []*thread
	selected string
	settings settings

	showSidebar  bool
	showDiff     bool
	showTerminal bool
	sidebarWidth float32
	// sidebarOpen is how far the sidebar has slid in this frame, from 0
	// to 1; sidebarJump skips the slide, for ⌘B.
	sidebarOpen float32
	sidebarJump bool
	// vibrant is whether the sidebar shows the window's vibrancy.
	vibrant bool
	// panelsJump shows the diff panel and the terminal without motion,
	// as when ⌘D and ⌘J toggle them.
	panelsJump bool

	draft string
	typed string // the draft the wind last saw: typing keeps it blowing
	wind  *wind.Texture
	// The model picker: open, fast mode, and when fast mode came on and
	// effort went Max, for their effects.
	pickerOpen   bool
	pickerFamily string
	fast         bool
	fastAt       time.Time
	// The effort slider's drag: held, and where it was pressed and last
	// held, in levels, to tell a click from a drag.
	effortHeld                  bool
	effortPressAt, effortLastAt float32
	maxAt                       time.Time
	model                       string
	effort                      string
	access                      string // one of accessLevels
	accessOpen                  bool
	accessAt                    time.Time // when the level last changed, for its effect
	collapsed                   map[string]bool
	// The inbox: the project it's filtered to, and its closed sections.
	projectFilter string
	sectionClosed map[category]bool
	// rowFx is the joke playing on a thread's row, by thread.
	rowFx map[string]rowGagFx
	// The diff panel: each file's viewer, by path, and their layout.
	diffs      map[string]*editor.DiffState
	diffLayout editor.DiffLayout
	// shells are the terminal drawer's shells, by project.
	shells map[string]*shell
	// snoozeFor is the thread whose snooze popover is open.
	snoozeFor string
	// now is the frame's time, as the view builds it.
	now      time.Time
	diffFile int

	cmdkOpen  bool
	cmdkQuery string
	cmdkIndex int

	keyQuery    string
	glassOffset ui.Point
	checkUntil  time.Time

	md     markdown.Options
	scroll ui.ScrollState
}

func newApp() *app {
	return &app{
		router:        newRouter(),
		wind:          wind.New(rand.Uint32(), nil),
		projects:      mockProjects(),
		archived:      mockArchived(),
		selected:      "t1",
		showSidebar:   true,
		sidebarWidth:  272,
		model:         "Claude Opus 5.5",
		effort:        "High",
		access:        accessAuto,
		collapsed:     map[string]bool{},
		sectionClosed: map[category]bool{catSettled: true},
		rowFx:         map[string]rowGagFx{},
		shells:        map[string]*shell{},
		diffs:         map[string]*editor.DiffState{},
		diffLayout:    editor.DiffUnified,
		settings: settings{
			theme:         0,
			glass:         true,
			vibrancy:      0.4,
			wind:          true,
			fontSize:      13,
			timeFormat:    "Relative",
			confirmDelete: true,
			streaming:     true,
			notifications: true,
			claudeEnabled: true,
			claudePath:    "/opt/homebrew/bin/claude",
			claudeModel:   "Claude Opus 5.5",
			claudeCustom:  []string{"claude-opus-5-5[1m]"},
			codexEnabled:  true,
			codexPath:     "/opt/homebrew/bin/codex",
			codexModel:    "GPT-5.5 Codex",
		},
	}
}

func (a *app) current() (*project, *thread) {
	for _, p := range a.projects {
		for _, t := range p.threads {
			if t.id == a.selected {
				return p, t
			}
		}
	}
	return a.projects[0], a.projects[0].threads[0]
}

func (a *app) select_(id string) {
	a.selected = id
	a.scroll.Y = 1e9
	if a.router.Path() != "/" {
		a.router.Push("/")
	}
}

// applyTheme sets the theme the settings ask for: the system's appearance,
// which the default theme follows, or light or dark.
func (a *app) applyTheme(c *ui.Context) {
	dark := c.Theme().Dark
	switch a.settings.theme {
	case 1:
		dark = false
	case 2:
		dark = true
	}
	if dark {
		palette = darkColors
	} else {
		palette = lightColors
	}
	palette.accent = accents[a.settings.accent].color

	t := *c.Theme()
	t.Dark = dark
	t.Background = palette.bg
	t.Surface = palette.surface
	t.SurfaceHover = palette.surface.Mix(palette.text, 0.06)
	t.SurfacePressed = palette.surface.Mix(palette.text, 0.1)
	t.Border = palette.border
	t.Text = palette.text
	t.TextMuted = palette.muted
	t.Accent = palette.accent
	t.AccentHover = palette.accent.Mix(ui.Hex("#ffffff"), 0.12)
	t.AccentPressed = palette.accent.Mix(ui.Hex("#000000"), 0.12)
	t.AccentText = ui.Hex("#ffffff")
	t.Selection = palette.accent.Alpha(0.35)
	t.Focus = palette.accent.Alpha(0.6)
	t.Scrollbar = palette.text.Alpha(0.18)
	t.Inverse, t.InverseText = palette.surface.Mix(palette.text, 0.1), palette.text
	t.Radius = 8
	t.FontSize = float32(a.settings.fontSize)
	c.SetTheme(&t)
}

// pane is the material of a floating panel: Liquid Glass, or a plain
// surface when the glass is turned off in the settings.
func (a *app) pane(e ui.Element, g glass.Glass) ui.Element {
	if a.settings.glass {
		return e.Material(g)
	}
	return e.Background(palette.surface).Border(1, palette.border)
}

// newRouter shows pages at once: settings sections are tabs, and their
// content should not slide.
func newRouter() *ui.Router {
	r := ui.NewRouter("/")
	r.Transition = ui.TransitionNone
	return r
}

func (a *app) view(c *ui.Context) {
	// The window's vibrancy is the system's material, in the system's
	// appearance: the sidebar shows it while the app's theme matches, and
	// the Liquid Glass setting is on.
	a.now = c.Now()
	systemDark := c.Theme().Dark
	a.applyTheme(c)
	a.shortcuts(c)
	a.vibrant = c.Vibrancy() && a.settings.glass && c.Theme().Dark == systemDark
	if c.Vibrancy() {
		// The material shows wherever nothing paints over it.
		c.Root().Background(ui.Transparent)
	}
	root := ui.Row(c).Fill().AlignItems(ui.Stretch)
	target := float32(0)
	if a.showSidebar {
		target = 1
	}
	if a.sidebarJump {
		a.sidebarOpen = root.Jump("sidebar", target)
	} else {
		a.sidebarOpen = root.AnimateTo("sidebar", target, sidebarSlide)
	}
	root.Children(func() {
		a.sidebarFrame(c)
		ui.Box(c).Grow(1).Shrink(1).Background(palette.bg).Children(func() {
			a.ambient(c)
			ui.Column(c).Fill().Children(func() { a.pages(c) })
		})
	})
	a.commandPalette(c)
	a.toasts(c)
}

func (a *app) pages(c *ui.Context) {
	a.router.View(c, func(r *ui.Route) {
		switch {
		case r.Match("/settings/{section...}"):
			r.Title("Settings")
			a.settingsPage(c, r)
		default:
			r.Title("Chat")
			a.chatPage(c)
		}
	}).Grow(1)
}

func (a *app) shortcuts(c *ui.Context) {
	if c.Shortcut(ui.Cmd, ui.KeyK) {
		a.openCommandPalette()
	}
	if c.Shortcut(ui.Cmd, ui.KeyN) {
		a.newThread()
	}
	// Keyboard actions don't animate.
	if c.Shortcut(ui.Cmd, ui.KeyB) {
		a.toggleSidebar(true)
	}
	if c.Shortcut(ui.Cmd, ui.KeyD) {
		a.showDiff, a.panelsJump = !a.showDiff, true
	}
	if c.Shortcut(ui.Cmd, ui.KeyJ) {
		a.showTerminal, a.panelsJump = !a.showTerminal, true
	}
	if c.Shortcut(ui.Cmd, ui.KeyComma) {
		a.router.Push("/settings/general")
	}
}

// toasts draws the window's toasts on glass, in the bottom right corner.
func (a *app) toasts(c *ui.Context) {
	ui.ToastViewportBase(c, func(viewport ui.Element, toasts []ui.Toast) {
		viewport.Padding(20).AlignItems(ui.End).Justify(ui.End).Gap(8)
		for _, t := range toasts {
			toast := ui.ToastBase(c, t)
			a.pane(toast.Root.Row().Gap(10).Padding(10, 10, 10, 14).Radius(14).MinWidth(260).AlignItems(ui.Center), glass.Glass{})
			// In and out the same way, from below, as Sonner's.
			toast.Root.Transition(ui.ElementTransition{
				Duration: 400 * time.Millisecond,
				Ease:     ease,
				Enter:    &ui.Motion{Y: 24},
				Exit:     &ui.Motion{Y: 24},
			})
			toast.Root.Children(func() {
				icon, col := icCheck, palette.green
				if t.Type == "info" {
					icon, col = icSparkles, palette.accent
				}
				ui.Icon(c, icon).FontSize(15).TextColor(col)
				ui.Column(c).Grow(1).Gap(1).Children(func() {
					ui.Text(c, t.Title).FontWeight(500)
					if t.Description != "" {
						ui.Text(c, t.Description).FontSize(11.5).TextColor(palette.muted)
					}
				})
				toast.CloseButton().Label("Close").Padding(4).Radius(6).TextColor(palette.muted).Children(func() {
					ui.Icon(c, icX).FontSize(12)
				})
			})
		}
	})
}

func main() {
	a := newApp()
	mygo.App.WhenReady(func() {
		a.win = mygo.NewWindow(mygo.WindowOptions{
			Title:         "T3 Code",
			Width:         1320,
			Height:        860,
			MinWidth:      760,
			MinHeight:     480,
			StateKey:      "main",
			TitleBarStyle: mygo.TitleBarHidden,
			Vibrancy:      mygo.VibrancySidebar,
			Content:       ui.View(a.view),
		})
		// The wind's glyph atlases are drawn between the window's frames.
		a.wind.SetLater(a.win.Update)
		// The terminals' shells hang up with the window.
		a.win.OnClosed(a.closeShells)
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}

func matches(s, q string) bool {
	return q == "" || strings.Contains(strings.ToLower(s), strings.ToLower(q))
}
