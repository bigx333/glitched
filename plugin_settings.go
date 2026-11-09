package main

import (
	"slices"
	"strings"

	"github.com/egoist/mygo"
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
				if p.Enabled && p.Err == nil {
					a.pluginSettingRows(c, p.ID)
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

// pluginSettingRows draws the settings the plugin id declares, by kind,
// with what it says of each.
func (a *app) pluginSettingRows(c *ui.Context, id string) {
	for _, st := range a.plugins.Settings(id) {
		desc := st.Description
		if st.Kind == "choice" {
			for _, o := range st.Options {
				if o.Value == st.Value && o.Description != "" {
					desc = o.Description
				}
			}
		}
		if st.Status != nil && st.Status.Text != "" {
			desc = st.Status.Text
		}
		set := func(v any) {
			if err := a.plugins.SetSetting(id, st.ID, v); err != nil {
				a.pluginNotice(ui.Toast{Title: "Couldn't set " + st.Label, Description: err.Error()})
			}
		}
		ui.Column(c).Key("setting:" + st.ID).Children(func() {
			settingRow(c, st.Label, desc, func() {
				if st.Status != nil && st.Status.Badge != "" {
					tone := map[string]ui.Color{"success": palette.green, "warning": palette.amber, "danger": palette.red}[st.Status.Tone]
					if tone == (ui.Color{}) {
						tone = palette.muted
					}
					ui.Text(c, st.Status.Badge).FontSize(11).FontWeight(600).TextColor(tone).Padding(1, 6).Radius(5).Background(tone.Alpha(0.14))
				}
				a.pluginSettingControl(c, st, set)
			})
		})
	}
}

func (a *app) pluginSettingControl(c *ui.Context, st plugins.SettingState, set func(any)) {
	switch st.Kind {
	case "switch":
		on, _ := st.Value.(bool)
		if ui.Switch(c.Key("value"), &on).Label(st.Label).Changed() {
			set(on)
		}
	case "choice":
		labels := make([]string, len(st.Options))
		i := 0
		for j, o := range st.Options {
			labels[j] = o.Label
			if o.Value == st.Value {
				i = j
			}
		}
		if st.Style == "segmented" {
			if ui.Segmented(c.Key("value"), &i, labels...).Label(st.Label).Changed() {
				set(st.Options[i].Value)
			}
			return
		}
		shown := labels[i]
		if ui.Select(c.Key("value"), &shown, labels).Label(st.Label).Width(200).Changed() {
			set(st.Options[slices.Index(labels, shown)].Value)
		}
	case "number":
		v, _ := st.Value.(float64)
		lo, hi := -1e9, 1e9
		if st.Min != nil {
			lo = *st.Min
		}
		if st.Max != nil {
			hi = *st.Max
		}
		step := st.Step
		if step <= 0 {
			step = 1
		}
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			if ui.NumberInput(c.Key("value"), &v, lo, hi, step).Label(st.Label).Width(110).Changed() {
				set(v)
			}
			if st.Unit != "" {
				ui.Text(c, st.Unit).TextColor(palette.muted)
			}
		})
	case "folder":
		v, _ := st.Value.(string)
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, cmpOr(displayPath(v), "None")).FontSize(12).TextColor(palette.muted).SingleLine().MaxWidth(240)
			ui.Button(c.Key("choose"), "Choose…").OnClick(func() {
				if dir := a.chooseFolder(); dir != "" {
					set(dir)
				}
			})
			if v != "" {
				ui.Button(c.Key("clear"), "Clear").OnClick(func() { set("") })
			}
		})
	default: // text, shortcut
		v, _ := st.Value.(string)
		e := ui.TextInput(c.Key("value"), &v).Label(st.Label).Width(240).Placeholder(cmpOr(st.Placeholder, map[string]string{"shortcut": "cmd+shift+k"}[st.Kind]))
		if e.Changed() && (st.Kind == "text" || v == "" || validShortcut(v)) {
			set(v)
		}
	}
}

func validShortcut(s string) bool { return plugins.ValidShortcut(s) }

// chooseFolder asks the user for a folder, "" if they chose none.
func (a *app) chooseFolder() string {
	if a.openFolder != nil {
		return a.openFolder()
	}
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Directory: true})
	if err != nil || len(paths) == 0 {
		return ""
	}
	return paths[0]
}
