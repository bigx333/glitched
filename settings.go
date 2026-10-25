package main

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"

	"glitched/internal/client"
)

func (a *app) settingsPage(c *ui.Context, r *ui.Route) {
	bar := c.TitleBar()
	left := 24 + bar.Left*(1-a.sidebarOpen)
	ui.Column(c).Fill().Children(func() {
		head := ui.Row(c).Height(max(bar.Height, headerHeight)).Shrink(0).Padding(0, 12+bar.Right, 0, left).Gap(8).AlignItems(ui.Center)
		head.DragWindow().Children(func() {
			if !a.showSidebar {
				if iconButton(c, "show-sidebar", icPanelLeft, "Show sidebar  ⌘B", false).Clicked() {
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

func (a *app) general(c *ui.Context) {
	s := &a.settings
	card(c, "THREADS", func() {
		settingRow(c, "New thread environment", "Where a new thread runs its agent.", func() {
			ui.Segmented(c, &s.newThreadMode, "Local", "New worktree").Label("New thread environment")
		})
		settingRow(c, "Default model", "Used for new threads. Each thread can pick its own.", func() {
			ui.Select(c, &a.model, modelNames()).Width(200)
		})
		settingRow(c, "Confirm before deleting threads", "", func() {
			ui.Switch(c, &s.confirmDelete).Label("Confirm before deleting threads")
		})
	})
	card(c, "INTERFACE", func() {
		settingRow(c, "Timestamps", "How the sidebar shows when a thread last changed.", func() {
			ui.Select(c, &s.timeFormat, []string{"Relative", "12-hour", "24-hour"}).Width(160)
		})
		settingRow(c, "Stream assistant messages", "Show replies as they're written, rather than once done.", func() {
			ui.Switch(c, &s.streaming).Label("Stream assistant messages")
		})
		settingRow(c, "Desktop notifications", "When a thread finishes or needs approval in the background.", func() {
			ui.Switch(c, &s.notifications).Label("Desktop notifications")
		})
	})
}

// --- Appearance

type themeMatch struct{}
type accentMatch struct{}

func (a *app) appearance(c *ui.Context) {
	s := &a.settings
	card(c, "THEME", func() {
		ui.Row(c).Padding(16).Gap(12).Children(func() {
			for _, name := range themeNames() {
				a.themeCard(c, name)
			}
		})
	})
	card(c, "ACCENT", func() {
		settingRow(c, "Accent color", accents[s.accent].name, func() {
			ui.Row(c).Gap(10).Children(func() {
				for i, acc := range accents {
					sw := ui.ButtonBase(c.Key(acc.name)).Size(24, 24).Radius(12).Center().Label(acc.name).Tooltip(acc.name).Cursor(ui.CursorPointer)
					if sw.Children(func() {
						if i == s.accent {
							ui.Box(c).Key("ring").Absolute().Top(-4).Left(-4).Right(-4).Bottom(-4).Radius(16).Border(2, acc.color).PassThrough().
								Transition(indicator(accentMatch{}))
						}
						ui.Box(c).Size(24, 24).Radius(12).Background(acc.color)
					}).Clicked() {
						s.accent = i
					}
				}
			})
		})
	})
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
}

// themeCard previews a theme's sidebar and chat, and the terminal's
// colors under them; System's is half light, half dark.
func (a *app) themeCard(c *ui.Context, name string) {
	s := &a.settings
	th := themeFor(name, true)
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
					side, main = themeFor(name, false).colors.sidebar, cols.bg
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
		s.theme = name
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

// --- Providers

func (a *app) providers(c *ui.Context) {
	s := &a.settings
	a.provider(c, "Claude Code", icClaude, ui.Hex("#d97757"), "v2.4.1", &s.claudeEnabled, &s.claudePath, &s.claudeModel, &s.claudeCustom,
		[]string{"Claude Opus 5.5", "Claude Sonnet 5.5", "Claude Haiku 5.5"})
	a.provider(c, "Codex", icOpenAI, ui.Hex("#10a37f"), "v0.98.0", &s.codexEnabled, &s.codexPath, &s.codexModel, &s.codexCustom,
		[]string{"GPT-5.5 Codex", "GPT-5.5", "GPT-5.5 mini"})
}

func (a *app) provider(c *ui.Context, name string, mark *ui.SVG, brand ui.Color, version string, enabled *bool, path, model *string, custom *[]string, models []string) {
	ui.Column(c).Key(name).Radius(12).Border(1, palette.border).Background(palette.surface).Clip().
		Transition(ui.ElementTransition{Size: true, Duration: 200 * time.Millisecond, Ease: easeOut}).Children(func() {
		ui.Row(c).Padding(14, 16).Gap(12).AlignItems(ui.Center).Children(func() {
			modelTile(c, mark, brand, 32)
			ui.Column(c).Grow(1).Gap(2).Children(func() {
				ui.Text(c, name).FontWeight(600)
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					col, label := palette.green, "Installed · "+version
					if !*enabled {
						col, label = palette.faint, "Disabled"
					}
					ui.Box(c).Size(6, 6).Radius(3).Background(col)
					ui.Text(c, label).FontSize(12).TextColor(palette.muted)
				})
			})
			ui.Switch(c, enabled).Label("Enable " + name)
		})
		if !*enabled {
			return
		}
		ui.Column(c).Key("fields").Padding(4, 16, 16).Gap(14).BorderWidth(1, 0, 0, 0).BorderColor(palette.border).
			ClipY().Transition(fold).Children(func() {
			ui.Box(c).Height(4)
			ui.Field(c, "Binary path", func() { ui.TextInput(c, path).Font("monospace") }).Description("Leave empty to find it on your PATH.")
			ui.Field(c, "Default model", func() { ui.Select(c, model, models) })
			ui.Field(c, "Custom models", func() { ui.TokenField(c, custom, nil) }).Description("Model ids to offer in the picker, as the CLI takes them.")
		})
	})
}

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
				ui.Text(c, status).FontSize(12).TextColor(col).SingleLine().Shrink(1).MaxWidth(360).Tooltip(status)
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
	return ui.Row(c).Gap(3).AlignItems(ui.Center).Shrink(0).Label(e.Name).Tooltip(e.Name + " · " + status).Children(func() {
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
	return ui.Row(c).Gap(4).AlignItems(ui.Center).Padding(1, 6).Radius(999).Border(1, palette.border).Shrink(0).
		Tooltip(e.Name + " · " + status).Children(func() {
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
	{"Send message", []string{"⌘", "↩"}},
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

func (a *app) keybindings(c *ui.Context) {
	ui.SearchField(c, &a.keyQuery).Placeholder("Search keybindings")
	card(c, "", func() {
		shown := 0
		for _, k := range keybindings {
			if !matches(k.label, a.keyQuery) {
				continue
			}
			shown++
			ui.Row(c).Key(k.label).Padding(11, 16).AlignItems(ui.Center).Children(func() {
				ui.Text(c, k.label).Grow(1)
				kbd(c, k.keys...)
			})
		}
		if shown == 0 {
			ui.Text(c, "No keybindings match.").Key("none").TextColor(palette.muted).Padding(16)
		}
	})
}

// --- About

func (a *app) about(c *ui.Context) {
	s := &a.settings
	ui.Column(c).Center().Gap(10).Padding(24, 0).Children(func() {
		ui.Image(c, appIcon).Size(96, 96)
		ui.Text(c, "Glitched").FontSize(20).FontWeight(650)
		ui.Text(c, "0.0.42 · native prototype built with MyGo v0.5.5").TextColor(palette.muted)
	})
	card(c, "", func() {
		settingRow(c, "Updates", "Checks for a new version when the app starts.", func() {
			if s.checking {
				if c.Now().Before(a.checkUntil) {
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						ui.Spinner(c)
						ui.Text(c, "Checking…").TextColor(palette.muted)
					})
					c.After(a.checkUntil.Sub(c.Now()))
					return
				}
				s.checking = false
				c.AddToast(ui.Toast{Title: "You're up to date", Description: "Glitched 0.0.42 is the newest version."})
			}
			if ghost(c, "check", false).Border(1, palette.border).Padding(6, 12).Children(func() {
				ui.Icon(c, icRotate).FontSize(13)
				ui.Text(c, "Check for updates")
			}).Clicked() {
				s.checking, a.checkUntil = true, c.Now().Add(1500*time.Millisecond)
			}
		})
		settingRow(c, "Source", "github.com/pingdotgg/t3code", func() {
			ui.Link(c, "Open on GitHub", "https://github.com/pingdotgg/t3code")
		})
	})
}
