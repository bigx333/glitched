package main

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"glitched/internal/plugins"
	"glitched/internal/plugins/pack"
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
		settingRow(c, "Install a plugin", "A .glitched-plugin file, a link to one, a GitHub release or repository (owner/repo), or a git repository. Glitched checks its signature before it writes anything.", func() {
			ui.TextInput(c.Key("source"), &a.pluginSource).Placeholder("owner/repo").Label("Plugin to install").Width(220)
			ui.Button(c.Key("install"), "Install").Disabled(a.pluginBusy || strings.TrimSpace(a.pluginSource) == "").OnClick(a.installPlugin)
		})
		settingRow(c, "Updates", a.updatesNote(), func() {
			ui.Button(c.Key("updates"), "Check for updates").Disabled(a.pluginBusy).OnClick(a.checkPluginUpdates)
		})
		for _, f := range a.pluginUpdates {
			if f.Err != nil {
				continue
			}
			settingRow(c, f.ID+" "+f.Version, updateDiff(f.Diff), func() {
				ui.Button(c.Key("update:"+f.ID), "Update").Disabled(a.pluginBusy).OnClick(func() { a.updatePlugin(f.ID) })
			})
		}
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
				a.pluginTrust(c, p)
				if p.Err != nil {
					ui.Text(c, p.Err.Error()).Key("error").Selectable().TextColor(palette.red).Padding(12, 16)
				}
				if p.Enabled && p.Err == nil {
					a.pluginSettingRows(c, p.ID)
				}
				for _, cp := range p.Capabilities {
					about := cp.About
					if cp.Name == "programs" {
						var names []string
						for _, pr := range p.Programs {
							names = append(names, pr.Name+" ("+pr.Risk+")")
						}
						about += ": " + strings.Join(names, ", ")
					}
					settingRow(c, about, cp.Name, func() {
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

// pluginTrust says why the plugin may run, or what it waits for: the
// consent sheet for someone else's, a way to trust an unsigned one as
// yours.
func (a *app) pluginTrust(c *ui.Context, p plugins.Plugin) {
	switch p.Trust {
	case plugins.TrustWaiting:
		ui.Column(c).Key("consent").Padding(14, 16).Gap(8).Background(palette.amber.Alpha(0.08)).Children(func() {
			ui.Text(c, p.Name+" is someone else's. It asks to:").FontWeight(600)
			for i, ask := range p.Asks {
				ui.Text(c, "• "+ask).Key(i)
			}
			if len(p.Asks) == 0 {
				ui.Text(c, "• Nothing beyond its own pages").Key("nothing")
			}
			ui.Text(c, "Signed by key "+p.Signer+". It stays off until you allow it; an update asking for more asks again.").FontSize(12).TextColor(palette.muted)
			ui.Row(c).Gap(8).Children(func() {
				ui.PrimaryButton(c.Key("allow"), "Allow").OnClick(func() { a.pluginsChanged(a.plugins.Allow(p.ID)) })
				ui.Button(c.Key("remove"), "Remove").TextColor(palette.red).OnClick(func() { a.pluginsChanged(a.plugins.Remove(p.ID)) })
			})
		})
	case plugins.TrustUnsigned:
		ui.Row(c).Key("unsigned").Padding(14, 16).Gap(12).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "Not signed. Trust it as yours only if you wrote it, or know who did: it then runs with what you grant it, and Glitched signs it again as you change it.").FontSize(12).TextColor(palette.muted).Grow(1).Shrink(1)
			ui.Button(c.Key("trust"), "Trust as mine").OnClick(func() { a.pluginsChanged(a.plugins.TrustAsMine(p.Folder)) })
		})
	default:
		if p.ID == "" {
			return
		}
		who := map[plugins.Trust]string{plugins.TrustOwn: "Yours", plugins.TrustConsented: "Someone else's, allowed", plugins.TrustTampered: "Changed since it was signed"}[p.Trust]
		if who == "" {
			return
		}
		settingRow(c, who, "Key "+p.Signer, func() {
			ui.Button(c.Key("remove"), "Remove").TextColor(palette.red).OnClick(func() { a.pluginsChanged(a.plugins.Remove(p.ID)) })
		})
	}
}

// installPlugin installs what the source field names, off the UI thread.
func (a *app) installPlugin() {
	src := strings.TrimSpace(a.pluginSource)
	a.pluginBusy = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		r, err := a.plugins.Install(ctx, pack.Source{Location: expandHome(src)})
		a.post(func() {
			a.pluginBusy = false
			if err != nil {
				a.pluginNotice(ui.Toast{Title: "Couldn't install " + src, Description: err.Error()})
				return
			}
			a.pluginSource = ""
			a.plugins.Reload()
			a.pluginsChanged(nil)
			note := "On, signed by your key."
			if r.Decision != pack.Trusted {
				note = "Someone else's: allow what it asks for below."
			}
			a.pluginNotice(ui.Toast{Title: "Installed " + r.ID + " " + r.Version, Description: note})
		})
	}()
}

func (a *app) checkPluginUpdates() {
	a.pluginBusy = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		found, err := a.plugins.CheckUpdates(ctx)
		a.post(func() {
			a.pluginBusy, a.pluginUpdates, a.pluginChecked = false, found, true
			if err != nil {
				a.pluginNotice(ui.Toast{Title: "Couldn't check for updates", Description: err.Error()})
			}
		})
	}()
}

func (a *app) updatePlugin(id string) {
	a.pluginBusy = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		r, err := a.plugins.Update(ctx, id)
		a.post(func() {
			a.pluginBusy = false
			a.pluginUpdates = slices.DeleteFunc(a.pluginUpdates, func(f pack.Found) bool { return f.ID == id })
			if err != nil {
				a.pluginNotice(ui.Toast{Title: "Couldn't update " + id, Description: err.Error()})
				return
			}
			a.plugins.Reload()
			a.pluginsChanged(nil)
			a.pluginNotice(ui.Toast{Title: "Updated " + id + " to " + r.Version})
		})
	}()
}

func (a *app) updatesNote() string {
	switch {
	case a.pluginBusy:
		return "Working…"
	case !a.pluginChecked:
		return "Plugins you installed from a file, a link or a repository update from there, signed by the same key."
	}
	n := 0
	for _, f := range a.pluginUpdates {
		if f.Err == nil {
			n++
		}
	}
	if n == 0 {
		return "Up to date."
	}
	return fmt.Sprintf("%d updates.", n)
}

// updateDiff says what an update newly asks for, which asks the user again.
func updateDiff(d pack.Diff) string {
	var parts []string
	for _, kind := range slices.Sorted(maps.Keys(d)) {
		if ch := d[kind]; len(ch.Added) > 0 {
			parts = append(parts, kind+": "+strings.Join(ch.Added, ", "))
		}
	}
	if len(parts) == 0 {
		return "Asks for nothing new."
	}
	return "Newly asks for " + strings.Join(parts, "; ") + ". You'll be asked to allow it."
}
