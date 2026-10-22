package main

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"
)

func (a *app) settingsPage(c *ui.Context, r *ui.Route) {
	bar := c.TitleBar()
	left := 24 + bar.Left*(1-a.sidebarOpen)
	ui.Column(c).Fill().Children(func() {
		head := ui.Row(c).Height(max(bar.Height, headerHeight)).Shrink(0).Padding(0, 12, 0, left).Gap(8).AlignItems(ui.Center)
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
			case r.Match("/keybindings"):
				r.Title("Keybindings")
				a.page(c, "Keybindings", "Shortcuts for the commands you use most.", a.keybindings)
			case r.Match("/archived"):
				r.Title("Archived threads")
				a.page(c, "Archived threads", "Threads you archived. Restore one to bring it back to its project.", a.archivedThreads)
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
			for i, name := range themeNames() {
				a.themeCard(c, i, name)
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
		// transparent the glass is: at Clear, the glass is macOS's own
		// material, which it frosts and tints itself.
		settingRow(c, "Glass tint", "How much the app tints all its glass: the sidebar and headers, and the panels over the chat. At Clear, the glass is untinted.", func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "Tinted").FontSize(11.5).TextColor(palette.faint)
				ui.Slider(c, &s.vibrancy, 0, 1).Width(160).Label("Glass tint").Disabled(!s.glass)
				ui.Text(c, "Clear").FontSize(11.5).TextColor(palette.faint)
			})
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
func (a *app) themeCard(c *ui.Context, i int, name string) {
	s := &a.settings
	th := themeFor(i, true)
	cols := th.colors
	b := ui.ButtonBase(c.Key(name)).Grow(1).Basis(0).Column().AlignItems(ui.Stretch).Gap(8).Cursor(ui.CursorPointer)
	if b.Children(func() {
		ui.Box(c).Height(96).Radius(10).Clip().Children(func() {
			if i == s.theme {
				ui.Box(c).Key("ring").Absolute().Top(0).Left(0).Right(0).Bottom(0).Radius(10).Border(2, palette.accent).PassThrough().
					Transition(indicator(themeMatch{}))
			}
			ui.Row(c).Fill().Border(1, palette.border).Radius(10).Clip().AlignItems(ui.Stretch).Children(func() {
				side, main := cols.sidebar, cols.bg
				if i == 0 { // half and half
					side, main = themeFor(i, false).colors.sidebar, cols.bg
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
					// The terminal's palette, as a strip of its colors.
					term := th.terminal()
					ui.Row(c).Gap(2).Margin(2, 0, 0).Children(func() {
						for _, col := range term.Palette[1:7] {
							ui.Box(c).Size(6, 4).Radius(1).Background(col)
						}
					})
				})
			})
		})
		t := ui.Text(c, name).FontSize(12).TextAlign(ui.Center)
		if i != s.theme {
			t.TextColor(palette.muted)
		}
	}).Clicked() {
		s.theme = i
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
		// The tint the slider sets, as every glass has it.
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

// --- Archived

func (a *app) archivedThreads(c *ui.Context) {
	if len(a.archived) == 0 {
		ui.Column(c).Key("empty").Center().Gap(10).Padding(60, 0).Transition(riseIn).Children(func() {
			ui.Icon(c, icArchive).FontSize(26).TextColor(palette.faint)
			ui.Text(c, "No archived threads").TextColor(palette.muted)
		})
		return
	}
	card(c, "", func() {
		for _, t := range a.archived {
			ui.Row(c).Key(t.id).Padding(10, 12, 10, 16).Gap(10).AlignItems(ui.Center).ClipY().Transition(fold).Children(func() {
				ui.Icon(c, icMessage).FontSize(14).TextColor(palette.faint)
				ui.Column(c).Grow(1).Gap(2).Children(func() {
					ui.Text(c, t.title).SingleLine()
					ui.Text(c, "Archived "+t.age+" ago").FontSize(11.5).TextColor(palette.faint)
				})
				ui.Button(c.Key("restore"), "Restore").OnClick(func() {
					a.archived = remove(a.archived, t)
					a.projects[0].threads = append(a.projects[0].threads, t)
					c.AddToast(ui.Toast{Title: "Thread restored", Description: t.title, Type: "info"})
				})
				ui.Button(c.Key("delete"), "Delete").TextColor(palette.red).OnClick(func() {
					a.archived = remove(a.archived, t)
				})
			})
		}
	})
}

func remove(ts []*thread, t *thread) []*thread {
	out := ts[:0:0]
	for _, x := range ts {
		if x != t {
			out = append(out, x)
		}
	}
	return out
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
