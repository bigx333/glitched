// Glitched: T3 Code, rebuilt in MyGo's native UI: the projects and threads
// sidebar, the chat with its work log, the composer, the diff panel, a
// command palette and the settings. Its threads are those of the engine on
// this machine, which it embeds when none runs, and of the servers it is
// paired with.
package main

import (
	"log"
	"maps"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"github.com/egoist/mygo/ui/markdown"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/wind"
)

type colors struct {
	bg, sidebar, surface, hover, active, border, text, muted, faint ui.Color
	accent, green, red, amber, blue                                 ui.Color
}

// theme is one of the app's looks: its colors, and the terminal's, a
// theme of Ghostty's where the theme has a matching one.
type theme struct {
	name   string
	dark   bool
	colors colors
	term   string
}

// The two looks the system's appearance picks between.
var (
	lightTheme = theme{"Light", false, lightColors, ""}
	darkTheme  = theme{"Dark", true, darkColors, ""}
)

// themes are the looks the settings choose from, after System.
var themes = []theme{
	lightTheme,
	darkTheme,
	{"Mocha", true, mochaColors, "Catppuccin Mocha"},
	{"Dawn", false, dawnColors, "Rose Pine Dawn"},
}

// systemTheme is the choice that follows the system's appearance.
const systemTheme = "System"

// themeNames are the settings' choices: the system's appearance, then
// each theme.
func themeNames() []string {
	names := []string{systemTheme}
	for _, t := range themes {
		names = append(names, t.name)
	}
	return names
}

// themeFor is the theme a settings choice names; System, or a name
// that is no theme's, is the system's appearance, Dark or Light.
func themeFor(name string, systemDark bool) theme {
	for _, t := range themes {
		if t.name == name {
			return t
		}
	}
	if systemDark {
		return darkTheme
	}
	return lightTheme
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
		faint:   ui.Hex("#85858d"),
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
		muted:   ui.Hex("#64646d"),
		faint:   ui.Hex("#6f6f78"),
		green:   ui.Hex("#16a34a"),
		red:     ui.Hex("#dc2626"),
		amber:   ui.Hex("#d97706"),
		blue:    ui.Hex("#2563eb"),
	}
	// mochaColors are Catppuccin Mocha's.
	mochaColors = colors{
		bg:      ui.Hex("#1e1e2e"),
		sidebar: ui.Hex("#181825"),
		surface: ui.Hex("#262637"),
		hover:   ui.RGBA(205, 214, 244, 0.06),
		active:  ui.RGBA(205, 214, 244, 0.1),
		border:  ui.RGBA(205, 214, 244, 0.1),
		text:    ui.Hex("#cdd6f4"),
		muted:   ui.Hex("#a6adc8"),
		faint:   ui.Hex("#9399b2"),
		green:   ui.Hex("#a6e3a1"),
		red:     ui.Hex("#f38ba8"),
		amber:   ui.Hex("#f9e2af"),
		blue:    ui.Hex("#89b4fa"),
	}
	// dawnColors are Rosé Pine Dawn's.
	dawnColors = colors{
		bg:      ui.Hex("#faf4ed"),
		sidebar: ui.Hex("#f2e9e1"),
		surface: ui.Hex("#fffaf3"),
		hover:   ui.RGBA(87, 82, 121, 0.06),
		active:  ui.RGBA(87, 82, 121, 0.1),
		border:  ui.RGBA(87, 82, 121, 0.12),
		text:    ui.Hex("#575279"),
		muted:   ui.Hex("#605c78"),
		faint:   ui.Hex("#66627e"),
		green:   ui.Hex("#286983"),
		red:     ui.Hex("#b4637a"),
		amber:   ui.Hex("#ea9d34"),
		blue:    ui.Hex("#56949f"),
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
	theme         string // a theme's name, or systemTheme
	accent        int
	glass         bool
	vibrancy      float64 // 0 to 1: how much of the sidebar's vibrancy shows through the background
	tintPanels    bool    // the floating panels take the glass tint too, not only the window's bars
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
	// post runs a func on the thread that builds the views, as the
	// manager's changes do.
	post    func(func())
	win     *mygo.Window
	router  *ui.Router
	backend *client.Manager
	// projects are the backend's threads by project, as the frame being
	// built shows them.
	projects []*project
	selected api.ID // zero: the draft
	fresh    draft
	opened   opened
	// ended is each thread's last turn's end, as last seen, to tell of
	// the next once.
	ended    map[api.ID]time.Time
	settings settings
	// theme is the frame's look, as the settings and the system choose it.
	theme theme
	// server is the Servers page's form for adding one, why the last add
	// failed, and whether one is waiting for its server to answer.
	server    client.Server
	serverErr string
	adding    bool
	// pairLabel is the This machine page's label for the next token;
	// paired, the token it minted last, shown until dismissed.
	pairLabel  string
	paired     pairing
	machineErr string
	// control is the This machine page's draft of the control server.
	control string

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
	projectFilter map[projectKey]bool // the projects the sidebar shows; none is all
	sectionClosed map[category]bool
	// rowFx is the joke playing on a thread's row, by thread.
	rowFx map[api.ID]rowGagFx
	// attachments go with the next message; pastes counts pasted images,
	// to name them.
	attachments []attachment
	pastes      int
	// The composer's autocomplete: the token it's for, whether it was
	// dismissed, its suggestions and choice, its flyout, and the projects'
	// files it suggests.
	suggestFor       string
	suggestDismissed bool
	suggestions      []suggestion
	suggestIndex     int
	suggestFly       *mygo.Window
	suggestBox       [5]int // the flyout's anchor and height, as last set
	files            map[string][]string
	// The preview pane: shown, its web view, its address and the one
	// being typed, and why it could not start.
	showPreview              bool
	preview                  *mygo.WebView
	previewURL, previewDraft string
	previewErr               string
	// quick is the quick-ask panel over every app.
	quick quickAsk
	// lists are the threads' timelines, by thread.
	lists map[api.ID]*ui.ListState
	// The diff panel: each file's viewer, by thread and path, and their
	// layout.
	diffs      map[diffKey]*fileDiff
	reading    map[diffKey]diffRead // files being read for diffs
	readGen    uint64
	diffLayout editor.DiffLayout
	// diffWrap wraps the diff's long lines.
	diffWrap bool
	// diffFile is the file each thread's diff shows, by path.
	diffFile map[api.ID]string
	// shells are the terminal drawer's shells, by workspace.
	shells map[string]*shell
	// windDraw paints the wind's field behind the chat, as the frame's
	// pane has it.
	windDraw func(p *ui.Painter, r ui.Rect)
	// roomBeside is the window's width beside the sidebar, and chatWidth
	// the chat's, as the last frame laid them out: the panels size to the
	// one and the header to the other.
	roomBeside, chatWidth float32
	// effortFlow is the light flowing through the effort bar.
	effortFlow flowState
	// composerOpened is the composer brought back to its full size by a
	// click while compact, until the end of the thread shows again.
	composerOpened bool
	// steers are the messages sent from the composer, until each lands
	// or fails, by command.
	steers map[api.ID]steer
	// snoozeFor is the thread whose snooze popover is open.
	snoozeFor api.ID
	// renaming is the thread whose title is being edited in its row, and
	// renameDraft the title as typed; renameFocused once the input has
	// had the focus, so that losing it ends the edit.
	renaming      api.ID
	renameDraft   string
	renameFocused bool
	// deleting is the thread the alert asks about deleting.
	deleting   thread
	deleteOpen bool
	// now is the frame's time, as the view builds it.
	now time.Time

	cmdkOpen  bool
	cmdkQuery string
	cmdkIndex int
	cmdkList  ui.ListState

	keyQuery    string
	glassOffset ui.Point
	checkUntil  time.Time

	md markdown.Options
}

// newApp is the app on the engines its state directory dir names. The
// manager posts its changes through post, which runs them on the thread
// that builds the views.
func newApp(dir string, post func(func())) *app {
	a := &app{
		router:        newRouter(),
		wind:          wind.New(rand.Uint32(), nil),
		ended:         map[api.ID]time.Time{},
		showSidebar:   true,
		sidebarWidth:  272,
		model:         "Claude Opus 5.5",
		effort:        "High",
		access:        accessAuto,
		collapsed:     map[string]bool{},
		sectionClosed: map[category]bool{catWorking: true, catSnoozed: true, catSettled: true},
		projectFilter: map[projectKey]bool{},
		rowFx:         map[api.ID]rowGagFx{},
		shells:        map[string]*shell{},
		diffs:         map[diffKey]*fileDiff{},
		reading:       map[diffKey]diffRead{},
		steers:        map[api.ID]steer{},
		post:          post,
		lists:         map[api.ID]*ui.ListState{},
		files:         map[string][]string{},
		previewURL:    defaultPreviewURL,
		previewDraft:  defaultPreviewURL,
		diffLayout:    editor.DiffUnified,
		diffWrap:      true,
		diffFile:      map[api.ID]string{},
		settings: settings{
			theme:         systemTheme,
			glass:         true,
			vibrancy:      1, // untinted: Liquid Glass has no color of its own
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
	a.backend = client.New(client.Config{Dir: dir, Post: func(fn func()) {
		post(func() {
			fn()
			a.changed()
		})
	}})
	return a
}

// applyTheme sets the theme the settings ask for: the system's appearance,
// which the default choice follows, or one of the app's themes.
func (a *app) applyTheme(c *ui.Context) {
	a.theme = themeFor(a.settings.theme, c.Theme().Dark)
	palette = a.theme.colors
	palette.accent = accents[a.settings.accent].color

	t := *c.Theme()
	t.Dark = a.theme.dark
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
//
// The glass is plain, as macOS draws its own, unless the Tint panels
// setting is on: then every pane takes the one tint the Glass tint
// setting sets, as the window's vibrancy does in the sidebar and the
// headers, and the two kinds of glass, macOS's showing the desktop and
// MyGo's what the app draws under it, read as one material.
func (a *app) pane(e ui.Element, g glass.Glass) ui.Element {
	if a.settings.glass {
		if g.Tint == (ui.Color{}) && a.settings.tintPanels {
			g.Tint = a.glassTint()
		}
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
	a.projects = a.gather()
	// A project gone, as with its server, leaves the filter, lest it hide
	// every thread left.
	maps.DeleteFunc(a.projectFilter, func(k projectKey, _ bool) bool {
		return !slices.ContainsFunc(a.projects, func(p *project) bool { return p.key == k })
	})
	a.pruneDiffs()
	// A steer to a server removed since never lands.
	maps.DeleteFunc(a.steers, func(_ api.ID, s steer) bool { return !slices.Contains(a.backend.Envs(), s.env) })
	if w, _ := c.Size(); w > 0 {
		a.roomBeside = w - a.sidebarWidth*a.sidebarOpen
	}
	// Jokes over are gone, whether or not their rows show: a row in a
	// closed section isn't built to see its own end.
	for id, fx := range a.rowFx {
		if fx.done(a.now) {
			delete(a.rowFx, id)
		}
	}
	systemDark := c.Theme().Dark
	a.applyTheme(c)
	a.shortcuts(c)
	a.vibrant = c.Vibrancy() && a.settings.glass && c.Theme().Dark == systemDark
	if c.Vibrancy() {
		// The material shows wherever nothing paints over it.
		c.Root().Background(ui.Transparent)
	}
	root := ui.Row(c).Fill().AlignItems(ui.Stretch)
	noteGPU(root)
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
		// The pages paint their own backgrounds, under their headers,
		// which show the window's vibrancy as the sidebar does.
		// The bars' tint, as the sidebar's: the headers are clear over it,
		// and it shows around the content's rounded corner.
		main := a.chrome(ui.Box(c).Grow(1).Shrink(1))
		main.Children(func() {
			ui.Column(c).Fill().Children(func() { a.pages(c) })
		})
	})
	a.commandPalette(c)
	a.deleteAlert(c)
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
	if c.Shortcut(ui.Cmd|ui.Shift, ui.KeyP) {
		a.togglePreview()
	}
	if c.Shortcut(ui.Cmd|ui.Shift, ui.KeyL) {
		a.toggleLightDark()
	}
	if c.Shortcut(ui.Cmd, ui.KeyJ) {
		a.showTerminal, a.panelsJump = !a.showTerminal, true
	}
	if c.Shortcut(ui.Cmd, ui.KeyComma) {
		a.router.Push("/settings/general")
	}
}

// contrast is the WCAG contrast ratio of two colors.
func contrast(a, b ui.Color) float64 {
	la, lb := luminance(a), luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

func luminance(c ui.Color) float64 {
	lin := func(v uint8) float64 {
		s := float64(v) / 255
		if s <= 0.03928 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(c.R) + 0.7152*lin(c.G) + 0.0722*lin(c.B)
}

// toggleLightDark switches to Light from any dark theme, and to Dark from
// any light one.
func (a *app) toggleLightDark() {
	a.settings.theme = lightTheme.name
	if !a.theme.dark {
		a.settings.theme = darkTheme.name
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
	var a *app
	a = newApp(stateDir(), func(fn func()) {
		a.win.Update(func() {
			fn()
			// The quick ask panel shows the projects too.
			if a.quick.panel != nil {
				a.quick.panel.Invalidate()
			}
		})
	})
	mygo.App.WhenReady(func() {
		a.installMenu()
		mygo.App.OnNotificationClick(a.onNotificationClick)
		a.win = mygo.NewWindow(mygo.WindowOptions{
			Title:     "Glitched",
			Width:     1320,
			Height:    860,
			MinWidth:  760,
			MinHeight: 480,
			StateKey:  "main",
			// The title bar is the view's, 52 DIPs as its header, the
			// traffic lights inset to sit in its middle (macOS); elsewhere
			// the window controls fill or center in a bar that high.
			TitleBarStyle:        mygo.TitleBarHiddenInset,
			TitleBarHeight:       headerHeight,
			TrafficLightPosition: &mygo.Point{X: 20, Y: (headerHeight - 12) / 2},
			// The menu's material, not the sidebar's: about half as much
			// again of the desktop shows through it.
			Vibrancy: mygo.VibrancyMenu,
			Content:  ui.View(a.view),
		})
		// The wind's glyph atlases are drawn between the window's frames.
		a.wind.SetLater(a.win.Update)
		a.setUpQuickAsk()
		// The terminals' shells hang up with the window.
		a.win.OnClosed(a.closeShells)
		a.setUpWindowClose()
		// Coming back to the window reads the thread the chat shows.
		a.win.OnFocus(a.win.Invalidate)
		if err := a.backend.Start(); err != nil {
			log.Print(err)
		}
	})
	// An engine embedded here stops with the app, which lets the CLI's
	// next one take the state directory.
	mygo.App.OnWillQuit(func(*mygo.QuitEvent) {
		if err := a.backend.Close(); err != nil {
			log.Print(err)
		}
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}

func matches(s, q string) bool {
	return q == "" || strings.Contains(strings.ToLower(s), strings.ToLower(q))
}

// chrome is the look of the window's bars, the sidebar's and, behind
// the pages and their clear headers, the right side's: the window's
// vibrancy tinted with the background by as much
// as the setting leaves, or the background where there is none.
func (a *app) chrome(e ui.Element) ui.Element {
	if a.vibrant {
		return e.Background(a.glassTint())
	}
	return e.Background(palette.sidebar) // the sidebar's, to be one bar with it
}

// contentCard is a page's content under the bar of glass: on the
// background, edged with a hairline, its top left corner rounded where it
// meets the sidebar, square once the sidebar has slid away and it meets
// the window's edge. The hairline is one border all round, which follows
// the rounded corner (borders of differing sides draw straight), pushed
// out a DIP on the right and at the bottom, where it leaves the window or
// lies on the line of the panel or the terminal beside it.
func (a *app) contentCard(e ui.Element) ui.Element {
	r := 16 * a.sidebarOpen
	left := float32(0)
	if a.sidebarOpen == 0 {
		left = -1 // against the window's edge: no line there either
	}
	return e.Background(palette.bg).Radius(r, 0, 0, 0).Border(1, palette.border).
		Margin(0, -1, -1, left).Clip()
}

// glassTint is the tint over the window's bars, and the panels when they
// take it: the app's background by as much as the Glass tint setting
// leaves, clear at Clear.
func (a *app) glassTint() ui.Color {
	return palette.bg.Alpha(float32(1 - a.settings.vibrancy))
}
