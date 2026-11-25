package main

import (
	"encoding/json"
	"fmt"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"

	"glitched/internal/client"
	"glitched/internal/plugins"
)

func (a *app) settingsPage(c *ui.Context, r *ui.Route) {
	bar := c.TitleBar()
	left := 24 + bar.Left*(1-a.sidebarOpen)
	ui.Column(c).Fill().Children(func() {
		head := ui.Row(c).Height(max(bar.Height, headerHeight)).Shrink(0).Padding(0, 12+bar.Right, 0, left).Gap(8).AlignItems(ui.Center)
		head.DragWindow().Children(func() {
			if !a.showSidebar {
				if a.iconButton(c, "show-sidebar", icPanelLeft, "Show sidebar  ⌘B", false).Clicked() {
					a.toggleSidebar(false)
				}
			}
			ui.Text(c, "Settings").TextColor(palette.muted)
			ui.Text(c, "/").TextColor(palette.faint)
			title := "About"
			for _, s := range settingsSections {
				if a.router.Path() == "/settings/"+s.path {
					title = s.label
				}
			}
			for _, e := range a.plugins.Entries() {
				if e.Settings && a.router.Path() == (pluginKey{e.Plugin, e.Page}).settingsPath() {
					title = e.Title
				}
			}
			ui.Text(c, title).FontWeight(600)
		})
		body := r.View(c, func(r *ui.Route) {
			switch {
			case r.Match("/general"):
				r.Title("General")
				a.page(c, "General", "Defaults for new threads and how the app behaves.", a.general)
			case r.Match("/appearance"):
				r.Title("Appearance")
				a.page(c, "Appearance", "Theme, accent color and the Liquid Glass material.", a.appearance)
			case r.Match("/providers"):
				r.Title("Providers")
				a.page(c, "Providers", "The coding agents Glitched runs, and their models.", a.providers)
			case r.Match("/machine"):
				r.Title("This machine")
				a.page(c, "This machine", "The engine on this machine: whether it runs, where it serves the tailnet, and the tokens other machines reach it with.", a.machine)
			case r.Match("/servers"):
				r.Title("Servers")
				a.page(c, "Servers", "Engines on other machines, reached over the tailnet with a token each paired for this app. Their threads show beside this machine's.", a.servers)
			case r.Match("/keybindings"):
				r.Title("Keybindings")
				a.page(c, "Keybindings", "Shortcuts for the commands you use most.", a.keybindings)
			case r.Match("/plugin/{plugin}/{page}"):
				title, subtitle, _, body := a.pluginPageParts(pluginKey{r.Param("plugin"), r.Param("page")})
				r.Title(title)
				a.page(c, title, subtitle, body)
			case r.Match("/plugins"):
				r.Title("Plugins")
				a.page(c, "Plugins", "Plugins draw parts of Glitched and add to it. Each runs in a sandbox of its own, and reaches only what you grant it here.", a.pluginSettings)
			default:
				r.Title("About")
				a.page(c, "About", "", a.about)
			}
		})
		a.contentCard(body.Grow(1))
	})
}

// page is a settings section: a heading over its cards, in a column that
// scrolls.
func (a *app) page(c *ui.Context, title, desc string, body func(c *ui.Context)) {
	ui.Scroll(c).Fill().AlignItems(ui.Center).Padding(32, 24, 48).Children(func() {
		ui.Column(c).WidthPercent(100).MaxWidth(720).Gap(20).Children(func() {
			ui.Column(c).Gap(4).Children(func() {
				ui.Text(c, title).FontSize(22).FontWeight(650)
				if desc != "" {
					ui.Text(c, desc).TextColor(palette.muted)
				}
			})
			body(c)
		})
	})
}

func card(c *ui.Context, title string, fn func()) ui.Element {
	return ui.Column(c).Gap(8).Children(func() {
		if title != "" {
			ui.Text(c, title).FontSize(11).FontWeight(600).LetterSpacing(0.6).TextColor(palette.faint).Padding(0, 4)
		}
		ui.Column(c).Radius(12).Border(1, palette.border).Background(palette.surface).Clip().
			Dividers(1, palette.border).Children(fn)
	})
}

func settingRow(c *ui.Context, title, desc string, control func()) {
	ui.Row(c).Padding(14, 16).Gap(16).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Grow(1).Shrink(1).Gap(2).Children(func() {
			ui.Text(c, title).FontWeight(500)
			if desc != "" {
				ui.Text(c, desc).FontSize(12).TextColor(palette.muted)
			}
		})
		control()
	})
}

// --- General

// general is a plugin's: the built-in General page, or one of the user's
// that replaces it, each setting it changes set through the app.
func (a *app) general(c *ui.Context) {
	a.settingsComponent(c, "settings.page.general", generalPage{
		Settings: a.appSettings(),
		Choices:  settingChoices{NewThread: newThreadModes, DefaultModel: a.modelNames(), Timestamps: timeFormats},
	})
}

// generalPage is the context of settings.page.general.
type generalPage struct {
	Settings appSettings    `json:"settings"`
	Choices  settingChoices `json:"choices"`
}

type settingChoices struct {
	NewThread    []string `json:"newThread"`
	DefaultModel []string `json:"defaultModel"`
	Timestamps   []string `json:"timestamps"`
}

// appSettings are the settings a plugin reads and sets, by settings.json's
// names: General's and Appearance's, but for the material's.
type appSettings struct {
	Theme         string `json:"theme"`
	Accent        string `json:"accent"`
	NewThread     string `json:"newThread"`
	DefaultModel  string `json:"defaultModel"`
	Timestamps    string `json:"timestamps"`
	ConfirmDelete bool   `json:"confirmDelete"`
	Streaming     bool   `json:"streaming"`
	Notifications bool   `json:"notifications"`
}

func (a *app) appSettings() appSettings {
	f := fileOf(a.settings)
	m, _ := a.modelOf(a.model)
	return appSettings{f.Theme, f.Accent, f.NewThread, m.name, f.Timestamps, f.ConfirmDelete, f.Streaming, f.Notifications}
}

// setAppSetting is app.settings.set: it sets one of appSettings to a
// value the page could have chosen, and refuses any other, as
// settingsFrom does a file's.
func (a *app) setAppSetting(args json.RawMessage) (any, error) {
	var in struct {
		Key   string
		Value json.RawMessage
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, err
	}
	keys := fields(settingsFile{})
	switch _, ok := keys[in.Key]; {
	case in.Key == "defaultModel":
		// The page chooses the default by the name it shows: one of
		// the models offered, or the default itself.
		var name string
		if json.Unmarshal(in.Value, &name) != nil || !slices.Contains(a.modelNames(), name) {
			return nil, fmt.Errorf("defaultModel is one of %s", strings.Join(a.modelNames(), ", "))
		}
		if m, ok := a.modelNamed(name); ok {
			a.chooseModel(m.key)
		}
		a.saveComposer()
		return nil, nil
	case !ok || !slices.Contains([]string{"theme", "accent", "newThread", "timestamps", "confirmDelete", "streaming", "notifications"}, in.Key):
		return nil, fmt.Errorf("%q is no setting a plugin sets", in.Key)
	}
	all := fields(fileOf(a.settings))
	var was, value any
	json.Unmarshal(all[in.Key], &was)
	if json.Unmarshal(in.Value, &value) != nil || fmt.Sprintf("%T", value) != fmt.Sprintf("%T", was) {
		return nil, fmt.Errorf("%s is a %T, not %s", in.Key, was, cmpOr(string(in.Value), "nothing"))
	}
	all[in.Key] = in.Value
	b, _ := json.Marshal(all)
	var f settingsFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("%s: %v", in.Key, err)
	}
	s := settingsFrom(a.settings, f, a.themeNames())
	if got := fields(fileOf(s))[in.Key]; string(got) != string(fields(f)[in.Key]) {
		return nil, fmt.Errorf("%s can't be %s", in.Key, in.Value)
	}
	if in.Key == "theme" {
		a.chooseTheme(s.theme)
	}
	a.settings = s
	return nil, nil
}

// --- Appearance

type themeMatch struct{}
type accentMatch struct{}

// appearance draws the theme and the accent with a plugin, the built-in
// Appearance or one of the user's that replaces it, over the material,
// which is Glitched's own.
func (a *app) appearance(c *ui.Context) {
	s := &a.settings
	ctx := appearancePage{Settings: a.appSettings(), Themes: a.themeNames()}
	for _, acc := range accents {
		ctx.Accents = append(ctx.Accents, accentRecord{acc.name, fmt.Sprintf("#%02x%02x%02x", acc.color.R, acc.color.G, acc.color.B)})
	}
	a.settingsComponent(c, "settings.page.appearance", ctx)
	card(c, "MATERIAL", func() {
		settingRow(c, "Liquid Glass", "The sidebar shows the desktop through the window, and floating panels what scrolls under them.", func() {
			ui.Switch(c, &s.glass).Label("Liquid Glass")
		})
		// The setting is the app's tint over the glass, not how
		// transparent the glass is: at Clear the app adds none, and the
		// bars are the system's material, which it frosts itself.
		// What the slider reaches depends on the window: the bars only
		// where it shows vibrancy, the panels once they take it, and
		// nothing while the glass is off. The slider says which.
		tintDesc := "How much the app's background tints the glass: the sidebar and headers over the window's vibrancy, and the floating panels when they take it. At Clear, the app adds no tint: the bars are the system's material, the panels plain glass."
		switch {
		case !s.glass:
			tintDesc = "How much the app's background tints the glass, once Liquid Glass is on."
		case !a.vibrant:
			tintDesc = "How much the app's background tints the floating panels, once they take it. This window's sidebar shows no vibrancy, so it is a solid color."
		}
		settingRow(c, "Glass tint", tintDesc, func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "Tinted").FontSize(11.5).TextColor(palette.faint)
				ui.Slider(c, &s.vibrancy, 0, 1).Width(160).Label("Glass tint").Disabled(!s.glass || !a.vibrant && !s.tintPanels)
				ui.Text(c, "Clear").FontSize(11.5).TextColor(palette.faint)
			})
		})
		settingRow(c, "Tint panels", "The app's own panes of glass take the tint the slider sets: the composer, the jump-to-latest pill, the command palette, the popovers, the toasts and the pane below. Off, they are plain glass, as the system's menus are.", func() {
			ui.Switch(c, &s.tintPanels).Label("Tint the panels").Disabled(!s.glass)
		})
		a.glassDemo(c)
		settingRow(c, "Wind background", "Characters lit by a passing wind behind the chat. Move the pointer to warm the air, click to blow a ripple.", func() {
			ui.Switch(c, &a.settings.wind).Label("Wind background")
		})
		settingRow(c, "Interface font size", fmt.Sprintf("%.0f pt", s.fontSize), func() {
			ui.StepSlider(c, &s.fontSize, 11, 16, 1).Width(200).Label("Interface font size")
		})
	})
	card(c, "CHAT", func() {
		settingRow(c, "Zen mode", "Fold each turn's steps and in-between replies into one line you can open. Approvals, questions and errors always show.", func() {
			ui.Switch(c, &a.settings.zen).Label("Zen mode")
		})
	})
}

// appearancePage is the context of settings.page.appearance.
type appearancePage struct {
	Settings appSettings    `json:"settings"`
	Themes   []string       `json:"themes"`
	Accents  []accentRecord `json:"accents"`
}

type accentRecord struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

// swatch is an accent's dot, ringed while it is the one chosen: the ring
// moves to the next one chosen.
func (a *app) swatch(c *ui.Context, key any, color ui.Color, on bool, label string) bool {
	sw := a.tip(c, ui.ButtonBase(c.Key(key)).Size(24, 24).Radius(12).Center().Label(label), label).Cursor(ui.CursorPointer)
	return sw.Children(func() {
		if on {
			ui.Box(c).Key("ring").Absolute().Top(-4).Left(-4).Right(-4).Bottom(-4).Radius(16).Border(2, color).PassThrough().
				Transition(indicator(accentMatch{}))
		}
		ui.Box(c).Size(24, 24).Radius(12).Background(color)
	}).Clicked()
}

// themeCard previews a theme's sidebar and chat, and the terminal's
// colors under them; System's is half light, half dark. A click picks it.
func (a *app) themeCard(c *ui.Context, name string, pick func()) {
	s := &a.settings
	th := a.themeFor(name, true)
	cols := th.colors
	b := ui.ButtonBase(c.Key(name)).Grow(1).Basis(0).Column().AlignItems(ui.Stretch).Gap(8).Cursor(ui.CursorPointer)
	if b.Children(func() {
		ui.Box(c).Height(96).Radius(10).Clip().Children(func() {
			if name == s.theme {
				ui.Box(c).Key("ring").Absolute().Top(0).Left(0).Right(0).Bottom(0).Radius(10).Border(2, palette.accent).PassThrough().
					Transition(indicator(themeMatch{}))
			}
			ui.Row(c).Fill().Border(1, palette.border).Radius(10).Clip().AlignItems(ui.Stretch).Children(func() {
				side, main := cols.sidebar, cols.bg
				if name == systemTheme { // half and half
					side, main = a.themeFor(name, false).colors.sidebar, cols.bg
				}
				ui.Column(c).Width(44).Background(side).Padding(10, 8).Gap(5).Children(func() {
					for range 4 {
						ui.Box(c).Height(4).Radius(2).Background(cols.muted.Alpha(0.35))
					}
				})
				ui.Column(c).Grow(1).Background(main).Padding(12, 12, 8).Gap(6).Justify(ui.End).Children(func() {
					ui.Box(c).Height(5).WidthPercent(70).Radius(3).Background(cols.muted.Alpha(0.4))
					ui.Box(c).Height(5).WidthPercent(50).Radius(3).Background(cols.muted.Alpha(0.4))
					ui.Box(c).Height(16).Radius(6).Background(cols.surface).Border(1, cols.border).Margin(4, 0, 0)
					ui.Row(c).Gap(2).Margin(2, 0, 0).Children(func() {
						for _, col := range th.terminal().Palette[1:7] {
							ui.Box(c).Size(6, 4).Radius(1).Background(col)
						}
					})
				})
			})
		})
		t := ui.Text(c, name).FontSize(12).TextAlign(ui.Center)
		if name != s.theme {
			t.TextColor(palette.muted)
		}
	}).Clicked() {
		pick()
	}
}

// glassDemo is a pane of glass the user throws over colorful stripes.
func (a *app) glassDemo(c *ui.Context) {
	ui.Box(c).Height(170).Margin(0, 16, 16).Radius(12).Clip().Children(func() {
		ui.Box(c).Fill().ConicGradient(ui.ConicGradient{Angle: 30, Oklab: true, Stops: []ui.GradientStop{
			{Color: accents[0].color, At: 0},
			{Color: accents[1].color, At: 0.25},
			{Color: accents[2].color, At: 0.5},
			{Color: accents[3].color, At: 0.75},
			{Color: accents[0].color, At: 1},
		}})
		ui.Column(c).Absolute().Top(0).Left(0).Right(0).Bottom(0).Padding(18).Gap(8).PassThrough().Children(func() {
			for _, w := range []float32{80, 55, 70, 40} {
				ui.Box(c).Height(8).WidthPercent(w).Radius(4).Background(ui.RGBA(255, 255, 255, 0.55))
			}
		})
		pane := ui.Row(c).Key("pane").Absolute().Top(40).Left(40).Padding(14, 20).Gap(10).Radius(26).AlignItems(ui.Center).Cursor(ui.CursorGrab)
		// A gesture: thrown, it carries its velocity into a spring.
		pane.Draggable(&a.glassOffset, ui.DraggableOptions{Bounds: &ui.Rect{X: -40, Y: -40, W: 420, H: 90}})
		lift := ui.Pose{}
		if pane.Dragging() {
			lift.Scale = 1.03
		}
		pane.AnimatePose(lift, press)
		// The tint the slider sets, when the panels take it.
		a.pane(pane, glass.Glass{}).Children(func() {
			ui.Icon(c, icSparkles).FontSize(16)
			ui.Text(c, "Drag me").FontWeight(600)
		})
	})
}

// --- Providers: providers.go

// --- Servers

func (a *app) servers(c *ui.Context) {
	card(c, "", func() {
		servers := a.backend.Servers()
		if len(servers) == 0 {
			ui.Text(c, "No servers yet.").Key("none").TextColor(palette.muted).Padding(16)
		}
		for _, s := range servers {
			ui.Row(c).Key(s.Label).Padding(12, 12, 12, 16).Gap(12).AlignItems(ui.Center).ClipY().Transition(fold).Children(func() {
				col, status := a.envStatus(a.serverEnv(s.Label))
				ui.Box(c).Size(8, 8).Radius(4).Background(col)
				ui.Column(c).Grow(1).Shrink(1).Gap(2).Children(func() {
					ui.Text(c, s.Label).FontWeight(500)
					ui.Text(c, s.Addr).FontSize(12).Font("monospace").TextColor(palette.muted).SingleLine()
				})
				a.tip(c, ui.Text(c, status).FontSize(12).TextColor(col).SingleLine().Shrink(1).MaxWidth(360), status)
				ui.Button(c.Key("remove"), "Remove").TextColor(palette.red).OnClick(func() {
					if err := a.backend.RemoveServer(s.Label); err != nil {
						c.AddToast(ui.Toast{Title: "Couldn't remove " + s.Label, Description: err.Error()})
					}
				})
			})
		}
	})
	card(c, "ADD A SERVER", func() {
		ui.Column(c).Padding(16).Gap(14).Children(func() {
			ui.Field(c, "Label", func() { ui.TextInput(c, &a.server.Label).Placeholder("devbox") }).
				Description("What the sidebar calls the machine. Adding a label already listed pairs it again.")
			ui.Field(c, "Address", func() { ui.TextInput(c, &a.server.Addr).Placeholder("devbox:7433").Font("monospace") }).
				Description("Where glitched serve -listen listens on it. It's listed once it answers, unless it's a machine listed already.")
			ui.Field(c, "Token", func() { ui.TextInput(c, &a.server.Token).Password().Font("monospace") }).
				Description("What glitched pair printed on it.")
			ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
				ui.Text(c, a.serverErr).FontSize(12).TextColor(palette.red).Grow(1).Shrink(1)
				add := "Add server"
				if a.adding {
					add = "Adding…"
				}
				ui.Button(c.Key("add"), add).Disabled(a.adding || a.server.Label == "" || a.server.Addr == "").OnClick(func() {
					s := a.server
					a.serverErr, a.adding = "", true
					a.backend.AddServer(s, func(err error) {
						a.adding = false
						switch {
						case err != nil:
							a.serverErr = err.Error()
						case a.server == s:
							a.server = client.Server{} // unless edited meanwhile
						}
					})
				})
			})
		})
	})
}

func (a *app) serverEnv(label string) *client.Env {
	for _, e := range a.backend.Envs() {
		if e.Kind == client.Remote && e.Name == label {
			return e
		}
	}
	return nil
}

// envStatus is how a machine's engine is reached, as a color and a word.
func (a *app) envStatus(e *client.Env) (ui.Color, string) {
	if o := a.backend.ShownBy(e); o != e {
		return palette.red, "Same engine as " + o.Name + " — remove one"
	}
	switch s := e.Status.(type) {
	case client.Online:
		return palette.green, "Online"
	case client.Offline:
		return palette.red, "Offline: " + s.Err.Error()
	case client.Refused:
		return palette.red, "Refused: no key for this token, or not the paired engine"
	case client.Incompatible:
		who := "server"
		if e.Kind == client.Local {
			who = "engine"
		}
		return palette.red, fmt.Sprintf("Incompatible: %s speaks protocol %d, this app %d", who, s.Theirs, s.Ours)
	}
	return palette.amber, "Connecting…"
}

// machineBadge is a row's mark of the machine its thread runs on: a dot of
// how it's reached and the fewest letters of its name that tell it from
// the other machines', the whole in a tooltip.
func (a *app) machineBadge(c *ui.Context, e *client.Env) ui.Element {
	var names []string
	for _, o := range a.backend.Envs() {
		names = append(names, o.Name)
	}
	col, status := a.envStatus(e)
	return a.tip(c, ui.Row(c).Gap(3).AlignItems(ui.Center).Shrink(0).Label(e.Name), e.Name+" · "+status).Children(func() {
		ui.Box(c).Size(5, 5).Radius(3).Background(col)
		ui.Text(c, prefix(e.Name, names)).FontSize(10).TextColor(palette.faint).SingleLine()
	})
}

// prefix is the shortest start of name, of two letters or more, that none
// of the other names starts with.
func prefix(name string, names []string) string {
	r := []rune(name)
	for n := min(2, len(r)); n < len(r); n++ {
		p := string(r[:n])
		if !slices.ContainsFunc(names, func(o string) bool { return o != name && strings.HasPrefix(o, p) }) {
			return p
		}
	}
	return name
}

// machinePill names the machine a thread runs on, after a dot of how it's
// reached.
func (a *app) machinePill(c *ui.Context, e *client.Env) ui.Element {
	col, status := a.envStatus(e)
	pill := ui.Row(c).Gap(4).AlignItems(ui.Center).Padding(1, 6).Radius(999).Border(1, palette.border).Shrink(0)
	return a.tip(c, pill, e.Name+" · "+status).Children(func() {
		ui.Box(c).Size(5, 5).Radius(3).Background(col)
		ui.Text(c, e.Name).FontSize(10.5).TextColor(palette.muted).SingleLine()
	})
}

// --- Keybindings

var keybindings = []struct {
	label string
	keys  []string
}{
	{"New thread", []string{"⌘", "N"}},
	{"Search threads and commands", []string{"⌘", "K"}},
	{"Send message", []string{"↩"}},
	{"New line in the message", []string{"⇧", "↩"}},
	{"Send, or stop the agent when empty", []string{"⌘", "↩"}},
	{"Allow the pending approval", []string{"⌘", "↩"}},
	{"Deny the pending approval", []string{"⇧", "⌘", "D"}},
	{"Previous / next change in the diff", []string{"⌘", "[", "]"}},
	{"Mark the change in view reviewed", []string{"⇧", "⌘", "↩"}},
	{"Toggle sidebar", []string{"⌘", "B"}},
	{"Toggle diff panel", []string{"⌘", "D"}},
	{"Toggle terminal", []string{"⌘", "J"}},
	{"Open settings", []string{"⌘", ","}},
	{"Interrupt agent", []string{"⌃", "C"}},
	{"Previous thread", []string{"⌘", "⌥", "↑"}},
	{"Next thread", []string{"⌘", "⌥", "↓"}},
}

// keybindings is a plugin's: the built-in Keybindings page, or one of
// the user's that replaces it.
func (a *app) keybindings(c *ui.Context) {
	ctx := keybindingsPage{Keybindings: make([]keybindingRecord, len(keybindings))}
	for i, k := range keybindings {
		ctx.Keybindings[i] = keybindingRecord{k.label, k.keys}
	}
	a.settingsComponent(c, "settings.page.keybindings", ctx)
}

// keybindingsPage is the context of settings.page.keybindings.
type keybindingsPage struct {
	Keybindings []keybindingRecord `json:"keybindings"`
}

type keybindingRecord struct {
	Label string   `json:"label"`
	Keys  []string `json:"keys"`
}

// settingsComponent draws a settings page's body with the plugin that
// replaces the component, in the page's column.
func (a *app) settingsComponent(c *ui.Context, name string, ctx any) {
	box := func() ui.Element { return ui.Column(c).Key("component:" + name) }
	a.pluginComponent(c, name, nil, componentCopy{key: "page", box: box, context: func(plugins.ThreadRecord) any { return ctx }})
}

// --- About

// about is a plugin's: the built-in About page, or one of the user's
// that replaces it. The update check it starts is the app's, which says
// how it went.
func (a *app) about(c *ui.Context) {
	s := &a.settings
	if s.checking {
		if !c.Now().Before(a.checkUntil) {
			s.checking = false
			c.AddToast(ui.Toast{Title: "You're up to date", Description: "Glitched " + appVersion + " is the newest version."})
		} else {
			c.After(a.checkUntil.Sub(c.Now()))
		}
	}
	a.settingsComponent(c, "settings.page.about", aboutPage{Version: appVersion, Toolkit: toolkit, Checking: s.checking, Source: "https://github.com/bigx333/glitched"})
}

// appVersion is Glitched's version.
const appVersion = "0.0.42"

// toolkit is the MyGo Glitched was built with, as its build says.
var toolkit = func() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "MyGo"
	}
	for _, d := range info.Deps {
		if d.Path == "github.com/egoist/mygo" {
			if d.Replace != nil {
				d = d.Replace
			}
			return "MyGo " + d.Version
		}
	}
	return "MyGo"
}()

// aboutPage is the context of settings.page.about.
type aboutPage struct {
	Version  string `json:"version"`
	Toolkit  string `json:"toolkit"`
	Checking bool   `json:"checking"`
	Source   string `json:"source"`
}

// checkForUpdatesEffect is app.checkForUpdates: About's check, which
// shows it runs for a moment and says how it went.
func (a *app) checkForUpdatesEffect() (any, error) {
	a.pluginLater = append(a.pluginLater, func(c *ui.Context) {
		if !a.settings.checking {
			a.settings.checking, a.checkUntil = true, c.Now().Add(1500*time.Millisecond)
		}
	})
	return nil, nil
}
