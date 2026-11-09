package main

import (
	"strings"

	"github.com/egoist/mygo/ui"

	"glitched/internal/plugins"
)

// displayPath is a path as the user reads it, from ~ when it's in the
// home folder.
func displayPath(p string) string {
	if rest, ok := strings.CutPrefix(p, homeDir()+"/"); ok {
		return "~/" + rest
	}
	return p
}

// pluginSettings lists the installed plugins: each one's switch, why it
// isn't running, and the capabilities it declares, with a switch for each
// one the user grants.
func (a *app) pluginSettings(c *ui.Context) {
	card(c, "", func() {
		settingRow(c, "Plugins folder", "Put a plugin's folder here, then reload. docs/plugins.md says how to write one.", func() {
			ui.Text(c, displayPath(plugins.Dir(a.dir))).Selectable().Font("monospace").FontSize(12).TextColor(palette.muted)
			ui.Button(c.Key("reload"), "Reload").OnClick(func() {
				a.plugins.Reload()
				a.pluginsChanged(nil)
			})
		})
	})
	list := a.plugins.Installed()
	if len(list) == 0 {
		ui.Text(c, "No plugins are installed.").Key("none").TextColor(palette.muted).Padding(0, 4)
	}
	for _, p := range list {
		ui.Column(c).Key("plugin:" + p.Folder).Children(func() {
			card(c, strings.ToUpper(p.Name), func() {
				desc := strings.TrimSpace(strings.Join([]string{p.Version, p.Description}, " · "))
				desc = strings.Trim(desc, " ·")
				settingRow(c, "Enabled", desc, func() {
					on := p.Enabled
					if ui.Switch(c.Key("enabled"), &on).Label("Enable " + p.Name).Disabled(p.ID == "").Changed() {
						a.pluginsChanged(a.plugins.SetEnabled(p.ID, on))
					}
				})
				if p.Err != nil {
					ui.Text(c, p.Err.Error()).Key("error").Selectable().TextColor(palette.red).Padding(12, 16)
				}
				for _, cp := range p.Capabilities {
					settingRow(c, cp.About, cp.Name, func() {
						if !cp.Grant {
							ui.Text(c, "Allowed").FontSize(12).TextColor(palette.muted)
							return
						}
						on := cp.Granted
						if ui.Switch(c.Key("grant:"+cp.Name), &on).Label("Grant " + cp.Name + " to " + p.Name).Changed() {
							a.pluginsChanged(a.plugins.SetGranted(p.ID, cp.Name, on))
						}
					})
				}
				ui.Text(c, displayPath(p.Folder)).Key("folder").Selectable().Font("monospace").FontSize(11.5).TextColor(palette.faint).Padding(10, 16)
			})
		})
	}
}
