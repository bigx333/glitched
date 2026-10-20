package main

import (
	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"
)

type command struct {
	section string
	label   string
	hint    string
	icon    *ui.SVG
	run     func()
}

func (a *app) openCommandPalette() {
	a.cmdkOpen, a.cmdkQuery, a.cmdkIndex = true, "", 0
}

func (a *app) commands() []command {
	cmds := []command{
		{"Actions", "New thread", "⌘N", icSquarePen, a.newThread},
		{"Actions", "Toggle diff panel", "⌘D", icDiff, func() { a.showDiff = !a.showDiff }},
		{"Actions", "Toggle terminal", "⌘J", icTerminal, func() { a.showTerminal = !a.showTerminal }},
		{"Actions", "Toggle sidebar", "⌘B", icPanelLeft, func() { a.showSidebar = !a.showSidebar }},
		{"Actions", "Toggle light / dark", "", icPalette, func() {
			if a.settings.theme == 1 {
				a.settings.theme = 2
			} else {
				a.settings.theme = 1
			}
		}},
		{"Actions", "Open settings", "⌘,", icSettings, func() { a.router.Push("/settings/general") }},
	}
	for _, p := range a.projects {
		for _, t := range p.threads {
			cmds = append(cmds, command{"Threads", t.title, p.name, icMessage, func() { a.select_(t.id) }})
		}
	}
	var shown []command
	for _, cmd := range cmds {
		if matches(cmd.label, a.cmdkQuery) || matches(cmd.hint, a.cmdkQuery) {
			shown = append(shown, cmd)
		}
	}
	return shown
}

// commandPalette is a sheet of glass over the blurred window. It's opened
// from the keyboard many times a day, so it shows and hides at once, and
// its selection moves at once with the arrows.
func (a *app) commandPalette(c *ui.Context) {
	ui.DialogBase(c, &a.cmdkOpen, func(backdrop, panel ui.Element) {
		backdrop.Justify(ui.Start).Padding(96, 0, 0)
		if a.settings.glass {
			backdrop.Material(glass.Blur{Radius: 3})
		} else {
			backdrop.Background(ui.RGBA(0, 0, 0, 0.35))
		}
		panel.Width(600).Radius(18).Clip()
		a.pane(panel, glass.Glass{}).Shadow(0, 24, 64, 0, ui.RGBA(0, 0, 0, 0.4))

		cmds := a.commands()
		a.cmdkIndex = max(0, min(a.cmdkIndex, len(cmds)-1))
		if c.Shortcut(0, ui.KeyDown) && len(cmds) > 0 {
			a.cmdkIndex = (a.cmdkIndex + 1) % len(cmds)
		}
		if c.Shortcut(0, ui.KeyUp) && len(cmds) > 0 {
			a.cmdkIndex = (a.cmdkIndex - 1 + len(cmds)) % len(cmds)
		}

		ui.Row(c).Gap(10).Padding(14, 16).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(palette.border).Children(func() {
			ui.Icon(c, icSearch).FontSize(16).TextColor(palette.muted)
			in := ui.TextInput(c.Key("q"), &a.cmdkQuery).Placeholder("Search threads and commands…").AutoFocus().
				Grow(1).Background(ui.Transparent).Border(0, ui.Transparent).FocusRing(false).Padding(0).FontSize(15)
			if in.Changed() {
				a.cmdkIndex = 0
			}
			if in.Submitted() && len(cmds) > 0 {
				a.cmdkOpen = false
				cmds[a.cmdkIndex].run()
			}
			kbd(c, "esc")
		})
		ui.Scroll(c).MaxHeight(380).Padding(6).Children(func() {
			if len(cmds) == 0 {
				ui.Text(c, "No results").TextColor(palette.muted).Padding(24).TextAlign(ui.Center)
				return
			}
			section := ""
			for i, cmd := range cmds {
				if cmd.section != section {
					section = cmd.section
					ui.Text(c, section).FontSize(11).FontWeight(600).TextColor(palette.faint).Padding(8, 10, 4)
				}
				selected := i == a.cmdkIndex
				r := row(c, cmd.section+cmd.label, selected).Padding(8, 10)
				if r.Hovered() {
					a.cmdkIndex = i
				}
				if r.Children(func() {
					ui.Icon(c, cmd.icon).FontSize(14)
					ui.Text(c, cmd.label).SingleLine().Grow(1).Shrink(1)
					ui.Text(c, cmd.hint).FontSize(11.5).TextColor(palette.faint)
				}).Clicked() {
					a.cmdkOpen = false
					cmd.run()
				}
			}
		})
	})
}
