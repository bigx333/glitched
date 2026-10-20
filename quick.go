package main

import (
	"log"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// Quick ask is a panel over every app, full-screen ones included, as
// MyGo's launcher example shows one: a global shortcut opens it from
// anywhere, without the window; Enter starts a thread in the chosen
// project with what was typed, and brings the window to it; Escape, or
// going elsewhere, hides it.

// quickShortcuts are tried in turn: the first one free is the app's.
var quickShortcuts = []string{"Alt+Shift+T", "CmdOrCtrl+Alt+Shift+T"}

const quickW, quickH = 640, 100

type quickAsk struct {
	panel    *mygo.Window
	shortcut string
	query    string
	project  int
	input    ui.Handle
}

// setUpQuickAsk makes the panel, hidden, and registers its shortcut.
func (a *app) setUpQuickAsk() {
	q := &a.quick
	q.panel = mygo.NewWindow(mygo.WindowOptions{
		Title:         "Quick Ask",
		Panel:         true,
		Hidden:        true,
		Frameless:     true,
		Transparent:   true,
		Vibrancy:      mygo.VibrancyPopover,
		Width:         quickW,
		Height:        quickH,
		DisableResize: true,
		Content:       ui.View(a.quickView),
	})
	// Hidden rather than closed, to show again at once; quitting destroys
	// it.
	q.panel.OnClose(func(e *mygo.CloseEvent) {
		e.PreventDefault()
		q.panel.Hide()
	})
	q.panel.OnBlur(q.panel.Hide)
	for _, s := range quickShortcuts {
		if err := mygo.GlobalShortcut.Register(s, a.toggleQuickAsk); err == nil {
			q.shortcut = s
			break
		} else {
			log.Printf("quick ask shortcut %s: %v", s, err)
		}
	}
}

func (a *app) toggleQuickAsk() {
	q := &a.quick
	if q.panel == nil {
		return
	}
	if q.panel.IsVisible() {
		q.panel.Hide()
		return
	}
	// On the display with the pointer, a third of the way down.
	d := mygo.Screen.DisplayNearestPoint(mygo.Screen.CursorScreenPoint())
	w, h := q.panel.Size()
	q.panel.SetPosition(d.WorkArea.X+(d.WorkArea.Width-w)/2, d.WorkArea.Y+(d.WorkArea.Height-h)/3)
	q.query = ""
	p, _ := a.current()
	for i, pr := range a.projects {
		if pr == p {
			q.project = i
		}
	}
	q.input.Focus()
	q.panel.Show()
}

func (a *app) quickView(c *ui.Context) {
	a.applyTheme(c)
	q := &a.quick
	root := c.Root()
	if c.Vibrancy() {
		root.Background(ui.Transparent) // the popover's material
	} else {
		root.Background(palette.surface)
	}
	ui.Column(c).Fill().Padding(14, 16, 10).Gap(10).Children(func() {
		ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, icSparkles).FontSize(20).TextColor(palette.accent)
			in := ui.TextInputBase(c, &q.query).Bind(&q.input).Grow(1).FontSize(20).
				Placeholder("Ask T3 Code to build something…").Label("Quick ask")
			if in.Submitted() && strings.TrimSpace(q.query) != "" {
				a.startQuickThread()
			}
		})
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			// The project the thread starts in: a click chooses another.
			for i, p := range a.projects {
				on := i == q.project
				b := ghost(c, p.id, on).Padding(3, 8, 3, 3).Gap(6).Label(p.name)
				if !on {
					b.Opacity(0.6)
				}
				if b.Children(func() {
					projectTile(c, p, 18)
					ui.Text(c, p.name).FontSize(12)
				}).Clicked() {
					q.project = i
				}
			}
			ui.Spacer(c)
			ui.Text(c, "↵ start thread   esc close").FontSize(11).TextColor(palette.faint)
		})
	})
}

// startQuickThread starts a thread in the chosen project with the query,
// sends it, and brings the window to it.
func (a *app) startQuickThread() {
	q := &a.quick
	p := a.projects[q.project]
	t := &thread{id: "q" + strings.ReplaceAll(a.now.Format("150405.000"), ".", ""), title: "New thread", age: "now", branch: "main"}
	p.threads = append([]*thread{t}, p.threads...)
	a.select_(t.id)
	a.draft = q.query
	a.send(t)
	q.query = ""
	if q.panel != nil {
		q.panel.Hide()
	}
	if a.win != nil {
		a.win.Show()
		a.win.Focus()
		a.win.Invalidate()
	}
}
