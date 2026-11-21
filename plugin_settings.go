package main

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"path/filepath"
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

// pluginSettings is Settings › Plugins: the plugins by whose they are,
// each a row that opens to what it draws, what it may do and why it
// isn't running; and the parts of Glitched, with who draws each.
func (a *app) pluginSettings(c *ui.Context) {
	list := a.plugins.Installed()
	// Only yours make the list long: the built-ins alone need no filter.
	long := len(list) > 8 && slices.ContainsFunc(list, func(p plugins.Plugin) bool { return !p.Builtin })
	ui.Row(c).Gap(12).AlignItems(ui.Center).Wrap().Children(func() {
		ui.Segmented(c.Key("view"), &a.pluginsView, "Plugins", "Parts of Glitched").Label("Show")
		ui.Spacer(c)
		if long && a.pluginsView == 0 {
			ui.SearchField(c.Key("filter"), &a.pluginsQuery).Label("Filter plugins").Placeholder("Filter plugins").Width(220)
		}
	})
	if a.pluginsView == 1 {
		a.appParts(c, list)
		return
	}
	if len(list) == 0 {
		ui.Text(c, "No plugins are installed.").Key("none").TextColor(palette.muted).Padding(0, 4)
	}
	shown := 0
	for _, g := range groupPlugins(list) {
		var rows []plugins.Plugin
		for _, p := range g.list {
			if !long || matchesPlugin(p, a.pluginsQuery) {
				rows = append(rows, p)
			}
		}
		shown += len(rows)
		if len(rows) == 0 && (g.empty == "" || len(list) == 0 || long && strings.TrimSpace(a.pluginsQuery) != "") {
			continue
		}
		waiting := 0
		for _, p := range g.list {
			if _, t := a.pluginStanding(p); t >= toneWarn {
				waiting++
			}
		}
		ui.Column(c).Key(g.title).Gap(8).Children(func() {
			// The count of those that wait is in the title, which no
			// grant moves: a line of its own would come and go under the
			// pointer.
			ui.Row(c).Gap(10).Padding(0, 4).AlignItems(ui.Center).Children(func() {
				sectionLabel(c, fmt.Sprintf("%s  %d", g.title, len(g.list)))
				if waiting > 0 {
					ui.Text(c, fmt.Sprintf("%d to look at", waiting)).Key("waiting").FontSize(11).FontWeight(600).TextColor(palette.amber)
				}
			})
			ui.Column(c).Radius(12).Border(1, palette.border).Background(palette.surface).Clip().Dividers(1, palette.border).Children(func() {
				if len(rows) == 0 {
					ui.Row(c).Key("empty").Padding(16).Gap(12).AlignItems(ui.Center).Children(func() {
						ui.Icon(c, icPencil).FontSize(18).TextColor(palette.faint).Shrink(0)
						ui.Text(c, g.empty).TextColor(palette.muted).Grow(1).Shrink(1)
					})
				}
				for _, p := range rows {
					a.pluginRow(c, p)
				}
			})
		})
	}
	if shown == 0 && len(list) > 0 {
		ui.Text(c, "No plugin matches “"+strings.TrimSpace(a.pluginsQuery)+"”.").Key("no-match").TextColor(palette.muted).Padding(0, 4)
	}
	card(c.Key("sources"), "ADD AND UPDATE", func() {
		optionRow(c, "Install a plugin", "A .glitched-plugin file, a link to one, a GitHub release or repository (owner/repo), or a git repository. Glitched checks its signature before it writes anything.", func() {
			ui.TextInput(c.Key("source"), &a.pluginSource).Placeholder("owner/repo").Label("Plugin to install").Width(220)
			ui.Button(c.Key("install"), "Install").Disabled(a.pluginBusy || strings.TrimSpace(a.pluginSource) == "").OnClick(a.installPlugin)
		})
		optionRow(c, "Updates", a.updatesNote(), func() {
			ui.Button(c.Key("updates"), "Check for updates").Disabled(a.pluginBusy).OnClick(a.checkPluginUpdates)
		})
		for _, f := range a.pluginUpdates {
			if f.Err != nil {
				continue
			}
			ui.Column(c).Key("update:" + f.ID).Children(func() {
				optionRow(c, f.ID+" "+f.Version, updateDiff(f.Diff), func() {
					ui.Button(c.Key("update"), "Update").Label("Update " + f.ID).Disabled(a.pluginBusy).OnClick(func() { a.updatePlugin(f.ID) })
				})
			})
		}
		optionRow(c, "Plugins folder", "Put a plugin's folder here, then reload. docs/plugins.md says how to write one.", func() {
			ui.Text(c, displayPath(plugins.Dir(a.dir))).Selectable().Font("monospace").FontSize(12).TextColor(palette.muted)
			ui.Button(c.Key("reload"), "Reload").OnClick(func() {
				a.plugins.Reload()
				a.pluginsChanged(nil)
			})
		})
	})
}

// optionRow is a settings row whose control drops under its words when
// the window is too narrow for both.
func optionRow(c *ui.Context, title, desc string, control func()) {
	ui.Row(c).Padding(12, 16).GapY(10).GapX(16).AlignItems(ui.Center).Wrap().Children(func() {
		ui.Column(c).Grow(1).Shrink(1).Basis(240).Gap(2).Children(func() {
			ui.Text(c, title).FontWeight(500)
			if desc != "" {
				ui.Text(c, desc).FontSize(12).TextColor(palette.muted)
			}
		})
		ui.Row(c).Gap(8).AlignItems(ui.Center).Shrink(1).Wrap().Children(control)
	})
}

// statePill is a state in a word or two, in its tone's color.
func statePill(c *ui.Context, text string, t tone) {
	col := t.color()
	ui.Row(c).Gap(5).AlignItems(ui.Center).Padding(2, 8).Radius(999).Background(col.Alpha(0.12)).Shrink(0).Children(func() {
		ui.Box(c).Size(6, 6).Radius(3).Background(col)
		ui.Text(c, text).FontSize(11.5).FontWeight(550).TextColor(col).SingleLine()
	})
}

// showPlugin opens the plugin's row in the list, and scrolls to it.
func (a *app) showPlugin(folder string) {
	a.pluginsView, a.pluginsQuery = 0, ""
	a.pluginsOpen[folder] = true
	a.pluginsReveal = folder
}

// pluginRow is a plugin in a line: its name, what it does to the app, how
// it stands and its switch. It opens to the plugin's detail, and is open
// from the start when the plugin waits on the user.
func (a *app) pluginRow(c *ui.Context, p plugins.Plugin) {
	state, t := a.pluginStanding(p)
	open, chosen := a.pluginsOpen[p.Folder]
	if !chosen && t >= toneWarn {
		// Kept, so that the grant that settles it doesn't close the row
		// under the pointer.
		open = true
		a.pluginsOpen[p.Folder] = true
	}
	was := open
	box := ui.Column(c).Key("plugin:" + p.Folder)
	if a.pluginsReveal == p.Folder {
		box.ScrollIntoView()
		a.pluginsReveal = ""
	}
	box.Children(func() {
		var parts ui.CollapsibleParts
		head := ui.Row(c).AlignItems(ui.Center).PaddingEnd(16).Transition(hoverFade)
		if head.Hovered() {
			head.Background(palette.hover)
		}
		head.Children(func() {
			parts = ui.CollapsibleBase(c.Key("open"), &open)
			parts.Trigger.Label(rowName(p)).Description(state+". "+pluginSummary(p)).Grow(1).Shrink(1).Padding(12, 12, 12, 14).Gap(12).AlignItems(ui.Center).
				Cursor(ui.CursorPointer).Radius(8).FocusRingOffset(-3).Children(func() {
				ui.Icon(c, icChevronR).FontSize(14).TextColor(palette.faint).Shrink(0).Rotate(90 * parts.Progress())
				ui.Column(c).Grow(1).Shrink(1).Gap(2).Children(func() {
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						name := ui.Text(c, p.Name).FontWeight(600).SingleLine().Shrink(1)
						if p.ShadowedBy != "" {
							name.TextColor(palette.muted)
						}
						if tag := originTag(p); tag != "" {
							ui.Text(c, tag).FontSize(10.5).FontWeight(600).TextColor(palette.muted).Padding(1, 6).Radius(5).Border(1, palette.border).SingleLine().Shrink(0)
						}
						if p.Version != "" {
							ui.Text(c, p.Version).FontSize(11.5).TextColor(palette.faint).SingleLine().Shrink(0)
						}
					})
					ui.Text(c, pluginSummary(p)).FontSize(12).TextColor(palette.muted).SingleLine()
				})
				statePill(c, state, t)
			})
			if open != was {
				a.pluginsOpen[p.Folder] = open
			}
			if p.ShadowedBy != "" {
				return // its copy has the switch
			}
			// Lit only when the plugin runs, as its pill says.
			on := a.pluginRuns(p)
			if ui.Switch(c.Key("enabled"), &on).Label("Enable " + p.Name).Description(state).Disabled(p.ID == "").Changed() {
				if on && p.Enabled {
					// Turned on, and something stops it: loaded again, with
					// its row open at what does. Its notice turns it off.
					a.pluginsOpen[p.Folder] = true
					a.pluginsReveal = p.Folder
				}
				a.pluginsChanged(a.plugins.SetEnabled(p.ID, on))
			}
		})
		panel := parts.Panel(func() { a.pluginDetail(c, p) })
		if panel.Valid() {
			panel.BorderWidth(1, 0, 0, 0).BorderColor(palette.border).Background(palette.bg.Alpha(0.45))
		}
	})
}

func sectionLabel(c *ui.Context, s string) {
	ui.Text(c, s).FontSize(11).FontWeight(600).LetterSpacing(0.6).TextColor(palette.faint)
}

// inset is a list inside a plugin's detail.
func inset(c *ui.Context, title string, fn func()) {
	ui.Column(c).Gap(8).Children(func() {
		sectionLabel(c, title)
		ui.Column(c).Radius(10).Border(1, palette.border).Background(palette.surface).Clip().Dividers(1, palette.border).Children(fn)
	})
}

// pluginDetail is everything about one plugin: what stops it and what to
// do, what it draws, what it may do, its settings, and whose it is.
func (a *app) pluginDetail(c *ui.Context, p plugins.Plugin) {
	ui.Column(c).Gap(18).Padding(16).Children(func() {
		if p.Description != "" {
			ui.Text(c, p.Description).TextColor(palette.muted)
		}
		a.pluginNotices(c, p)
		if p.ShadowedBy != "" {
			return // its copy's row has the rest
		}
		if len(p.Replaces) > 0 {
			inset(c, "DRAWS", func() {
				for i, name := range p.Replaces {
					st := a.partState(p, name)
					ui.Row(c).Key(i).Padding(10, 12).GapY(8).GapX(12).AlignItems(ui.Center).Wrap().Children(func() {
						ui.Column(c).Grow(1).Shrink(1).Basis(220).Gap(2).Children(func() {
							ui.Text(c, partTitle(name)).FontWeight(500)
							ui.Text(c, clip(st.why, 240)).FontSize(12).TextColor(palette.muted)
						})
						statePill(c, st.word, st.tone)
					})
				}
			})
		}
		a.pluginGrants(c, p)
		if p.Enabled && p.Err == nil && len(a.plugins.Settings(p.ID)) > 0 {
			inset(c, "SETTINGS", func() { a.pluginSettingRows(c, p.ID) })
		}
		a.pluginTrust(c, p)
	})
}

// pluginNotice is something the user should read before the rest: what is
// wrong, in a line, what was said, and what to do.
func pluginNotice(c *ui.Context, key string, t tone, title string, body, actions func()) {
	col := t.color()
	ui.Row(c).Key(key).Radius(10).Background(col.Alpha(0.08)).Border(1, col.Alpha(0.28)).Padding(12, 14).Gap(12).Children(func() {
		ui.Icon(c, icShieldAlert).FontSize(16).TextColor(col).Shrink(0).Margin(1, 0, 0)
		ui.Column(c).Grow(1).Shrink(1).Gap(6).Children(func() {
			ui.Text(c, title).FontWeight(600)
			if body != nil {
				body()
			}
			if actions != nil {
				ui.Row(c).Gap(8).Padding(4, 0, 0).Wrap().Children(actions)
			}
		})
	})
}

// pluginNotices say why the plugin isn't running as it would, and what to
// do: the consent sheet for someone else's, a way to trust an unsigned
// one as yours, what its loading or its drawing failed with.
func (a *app) pluginNotices(c *ui.Context, p plugins.Plugin) {
	said := func(s string) func() {
		return func() { ui.Text(c, s).FontSize(12).TextColor(palette.muted) }
	}
	failure := func(err error, advice string) func() {
		return func() {
			// The start of it, in a box that cuts a line with nowhere to
			// break; Copy takes the whole of it.
			ui.Column(c).Radius(6).Background(palette.bg.Alpha(0.6)).Padding(8, 10).Clip().Children(func() {
				ui.Text(c, clip(err.Error(), 1200)).Key("error").Selectable().Font("monospace").FontSize(11.5).TextColor(palette.text).MaxLines(12)
			})
			if advice != "" {
				ui.Text(c, advice).FontSize(12).TextColor(palette.muted)
			}
		}
	}
	copyError := func(err error) func() {
		return func() {
			ui.Button(c.Key("copy-error"), "Copy error").Label("Copy error of " + p.Name).OnClick(func() {
				text := err.Error()
				a.pluginLater = append(a.pluginLater, func(c *ui.Context) { c.WriteClipboard(text) })
			})
		}
	}
	remove := func() {
		ui.Button(c.Key("remove"), "Remove").Label("Remove " + p.Name).TextColor(palette.red).OnClick(func() { a.pluginsChanged(a.plugins.Remove(p.ID)) })
	}
	// Its switch shows off while something stops it, so the notice of
	// what does is where it is turned off.
	turnOff := func() {
		ui.Button(c.Key("off"), "Turn off").Label("Turn off " + p.Name).OnClick(func() { a.pluginsChanged(a.plugins.SetEnabled(p.ID, false)) })
	}
	stopped := func(err error) func() {
		return func() {
			copyError(err)()
			turnOff()
		}
	}
	switch {
	case p.ShadowedBy != "":
		title, what := "Your copy runs in its place", " draws what this one drew"
		if !a.copyRuns(p) {
			title, what = "Your copy isn't running, so this one draws", " is off or stopped, and this one draws until it runs"
		}
		pluginNotice(c, "shadowed", toneQuiet, title, said("Your copy in "+displayPath(p.ShadowedBy)+what+". Revert to built-in moves the copy to "+displayPath(filepath.Join(plugins.Dir(a.dir), ".reverted"))+" and runs this one again."), func() {
			// Shadowed, the built-in runs as a fallback under an id of its
			// own; Revert takes the id its copy has.
			ui.Button(c.Key("revert"), "Revert to built-in").Label("Revert to the built-in " + p.Name).OnClick(func() {
				a.pluginsChanged(a.plugins.Revert(strings.TrimSuffix(p.ID, plugins.FallbackSuffix)))
			})
		})
	case p.Trust == plugins.TrustWaiting:
		pluginNotice(c, "consent", toneWarn, p.Name+" is someone else's. It asks to:", func() {
			for i, ask := range p.Asks {
				ui.Text(c, "• "+ask).Key(i)
			}
			if len(p.Asks) == 0 {
				ui.Text(c, "• Nothing beyond its own pages").Key("nothing")
			}
			ui.Text(c, "Signed by key "+p.Signer+". It stays off until you allow it; an update asking for more asks again.").FontSize(12).TextColor(palette.muted)
		}, func() {
			ui.PrimaryButton(c.Key("allow"), "Allow").Label("Allow " + p.Name).OnClick(func() { a.pluginsChanged(a.plugins.Allow(p.ID)) })
			remove()
			turnOff()
		})
	case p.Trust == plugins.TrustUnsigned:
		pluginNotice(c, "unsigned", toneWarn, "Not signed, so it isn't running", said("Trust it as yours only if you wrote it, or know who did: it then runs with what you grant it, and Glitched signs it again as you change it."), func() {
			ui.Button(c.Key("trust"), "Trust as mine").Label("Trust as mine, " + p.Name).OnClick(func() { a.pluginsChanged(a.plugins.TrustAsMine(p.Folder)) })
			turnOff()
		})
	case p.Trust == plugins.TrustTampered && p.Err != nil:
		pluginNotice(c, "tampered", toneBad, "Changed since it was signed, so it isn't running", failure(p.Err, ""), stopped(p.Err))
	case p.Err != nil && p.Enabled:
		title, advice := "It didn't load", "Reload below. If it still doesn't load, turn it off: Glitched draws what it drew."
		switch {
		case strings.HasPrefix(p.Err.Error(), "its changed code didn't load"):
			title, advice = "Its changed code didn't load", "Fix it in "+displayPath(p.Folder)+": Glitched loads it again as you save."
		case p.Trust == plugins.TrustConsented:
			advice = "Check for an update below, or remove it."
		case !p.Builtin:
			advice = "Fix its plugin.json or its code in " + displayPath(p.Folder) + ": Glitched loads it again as you save."
		}
		pluginNotice(c, "failed", toneBad, title, failure(p.Err, advice), stopped(p.Err))
	case p.Enabled && a.plugins.Failed(p.ID) != nil:
		pluginNotice(c, "failed", toneBad, "It failed while it ran", failure(a.plugins.Failed(p.ID), ""), stopped(a.plugins.Failed(p.ID)))
	}
}

// pluginGrants lists what the plugin may do. The grants that let it act
// as the user, or redraw the app, come first and say what they risk; then
// the lighter grants; then what any plugin that declares it may do
// without asking.
func (a *app) pluginGrants(c *ui.Context, p plugins.Plugin) {
	var asked []plugins.CapabilityState
	var free []string
	for _, cp := range p.Capabilities {
		switch {
		case grantListed(p, cp):
			asked = append(asked, cp)
		case !cp.Grant:
			free = append(free, cp.About)
		}
	}
	if len(asked)+len(free) == 0 {
		return
	}
	slices.SortStableFunc(asked, func(x, y plugins.CapabilityState) int {
		return cmp.Compare(btoi(capRisk(y.Name) != ""), btoi(capRisk(x.Name) != ""))
	})
	ui.Column(c).Gap(8).Children(func() {
		sectionLabel(c, "WHAT IT MAY DO")
		if len(asked) > 0 {
			ui.Column(c).Radius(10).Border(1, palette.border).Background(palette.surface).Clip().Dividers(1, palette.border).Children(func() {
				for _, cp := range asked {
					risk := capRisk(cp.Name)
					row := ui.Row(c).Key(cp.Name).Padding(10, 12).Gap(12).AlignItems(ui.Center)
					if risk != "" && cp.Granted {
						row.Background(palette.amber.Alpha(0.06))
					}
					row.Children(func() {
						if risk != "" {
							ui.Icon(c, icShieldAlert).FontSize(16).TextColor(palette.amber).Shrink(0)
						} else {
							ui.Icon(c, icEye).FontSize(16).TextColor(palette.faint).Shrink(0)
						}
						ui.Column(c).Grow(1).Shrink(1).Gap(2).Children(func() {
							ui.Text(c, cmpOr(cp.About, cp.Name)).FontWeight(500)
							if cp.Name == "programs" {
								for i, pr := range p.Programs {
									ui.Text(c, pr.Name+": "+pr.Risk).Key(i).FontSize(12).TextColor(palette.muted)
								}
							}
							if risk != "" {
								ui.Text(c, risk).FontSize(12).TextColor(palette.amber)
							}
							ui.Text(c, cp.Name).Font("monospace").FontSize(11).TextColor(palette.faint)
						})
						on := cp.Granted
						if ui.Switch(c.Key("grant"), &on).Label("Grant " + cp.Name + " to " + p.Name).Changed() {
							a.pluginsChanged(a.plugins.SetGranted(p.ID, cp.Name, on))
						}
					})
				}
			})
		}
		if len(free) > 0 {
			ui.Column(c).Key("free").Gap(3).Padding(2, 4, 0).Children(func() {
				ui.Text(c, "Without asking, as any plugin may:").FontSize(12).TextColor(palette.muted)
				for i, about := range free {
					ui.Row(c).Key(i).Gap(6).AlignItems(ui.Center).Children(func() {
						ui.Icon(c, icCheck).FontSize(12).TextColor(palette.faint).Shrink(0)
						ui.Text(c, about).FontSize(12).TextColor(palette.muted).Shrink(1)
					})
				}
			})
		}
	})
}

// clip is s cut to n runes, with an ellipsis when it was longer.
func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// pluginTrust says whose the plugin is and which key signed it, with what
// that lets the user do: make a copy of a built-in, revert a copy to it,
// remove another.
func (a *app) pluginTrust(c *ui.Context, p plugins.Plugin) {
	who, how := trustWords(p)
	if who == "" {
		return
	}
	inset(c, "WHOSE IT IS", func() {
		switch {
		case p.Trust == plugins.TrustBuiltin:
			optionRow(c, who, how+" Make a copy to change it: your copy runs in its place, from your plugins folder, and Revert to built-in brings this one back.", func() {
				ui.Button(c.Key("copy"), "Make a copy").Label("Make a copy of " + p.Name).OnClick(func() {
					dst, err := a.plugins.Copy(p.ID)
					if err == nil {
						a.pluginsOpen[dst] = true
						a.pluginsReveal = dst
					}
					a.pluginsChanged(err)
				})
			})
		case p.Shadows:
			optionRow(c, who, how+" Revert moves your copy to "+displayPath(filepath.Join(plugins.Dir(a.dir), ".reverted"))+" and runs the built-in again.", func() {
				ui.Button(c.Key("revert"), "Revert to built-in").Label("Revert to built-in, " + p.Name).OnClick(func() { a.pluginsChanged(a.plugins.Revert(p.ID)) })
			})
		case p.ID == "" || p.Trust == plugins.TrustWaiting || p.Trust == plugins.TrustUnsigned:
			// The consent sheet removes one that waits; an unsigned
			// folder is the user's to delete.
			optionRow(c, who, how, func() {})
		default:
			optionRow(c, who, how, func() {
				ui.Button(c.Key("remove"), "Remove").Label("Remove " + p.Name).TextColor(palette.red).OnClick(func() { a.pluginsChanged(a.plugins.Remove(p.ID)) })
			})
		}
		ui.Text(c, displayPath(p.Folder)).Key("folder").Selectable().Font("monospace").FontSize(11.5).TextColor(palette.faint).Padding(10, 16)
	})
}

// appParts lists the parts of Glitched a plugin may draw, each with the
// plugin that draws it now and why another doesn't, then the parts that
// stay Glitched's own.
func (a *app) appParts(c *ui.Context, list []plugins.Plugin) {
	card(c.Key("parts"), "PARTS A PLUGIN MAY DRAW", func() {
		for _, comp := range plugins.Components {
			if comp.Locked != "" {
				continue
			}
			drawer, drawn := a.partDrawer(comp.Name)
			ui.Row(c).Key(comp.Name).Padding(12, 16).GapY(8).GapX(16).AlignItems(ui.Center).Wrap().Children(func() {
				ui.Column(c).Grow(1).Shrink(1).Basis(240).Gap(2).Children(func() {
					ui.Text(c, partTitle(comp.Name)).FontWeight(500)
					ui.Text(c, comp.About).FontSize(12).TextColor(palette.muted)
					for _, p := range list {
						if p.ShadowedBy != "" || !slices.Contains(p.Replaces, comp.Name) || drawn && (p.Folder == drawer.Folder || p.Builtin) {
							continue
						}
						st := a.partState(p, comp.Name)
						ui.Text(c, p.Name+": "+clip(st.why, 240)).Key("other:" + p.Folder).FontSize(12).TextColor(st.tone.color())
					}
				})
				switch {
				case !drawn && (comp.Native || comp.Own):
					statePill(c, "Glitched's own", toneQuiet)
					return
				case !drawn:
					statePill(c, "Nothing draws it", toneBad)
					return
				}
				whose := cmpOr(originTag(drawer), "Yours")
				if drawer.Builtin {
					whose = "Built in"
				}
				b := ui.ButtonBase(c.Key("drawer")).Label("Show "+drawer.Name+", which draws "+partTitle(comp.Name)).Gap(8).AlignItems(ui.Center).Padding(5, 10).Radius(8).
					Border(1, palette.border).Background(ui.Transparent).Cursor(ui.CursorPointer).Transition(hoverFade).Shrink(0)
				if b.Hovered() {
					b.Background(palette.hover)
				}
				b.OnClick(func() { a.showPlugin(drawer.Folder) }).Children(func() {
					col := palette.muted
					if !drawer.Builtin {
						col = palette.accent
					}
					ui.Text(c, whose).FontSize(11).FontWeight(600).TextColor(col).SingleLine()
					ui.Text(c, drawer.Name).FontSize(12.5).FontWeight(500).SingleLine().MaxWidth(180)
					ui.Icon(c, icChevronR).FontSize(12).TextColor(palette.faint)
				})
			})
		}
	})
	card(c.Key("locked"), "ALWAYS GLITCHED'S OWN", func() {
		for _, comp := range plugins.Components {
			if comp.Locked == "" {
				continue
			}
			ui.Row(c).Key(comp.Name).Padding(10, 16).Gap(12).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, icShieldTerminal).FontSize(16).TextColor(palette.faint).Shrink(0)
				ui.Column(c).Grow(1).Shrink(1).Gap(2).Children(func() {
					ui.Text(c, partTitle(comp.Name)).FontWeight(500)
					ui.Text(c, strings.ToUpper(comp.Locked[:1])+comp.Locked[1:]+".").FontSize(12).TextColor(palette.muted)
				})
			})
		}
	})
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
		set := func(v any) bool {
			if err := a.plugins.SetSetting(id, st.ID, v); err != nil {
				a.pluginNotice(ui.Toast{Title: "Couldn't set " + st.Label, Description: err.Error()})
				return false
			}
			return true
		}
		ui.Column(c).Key("setting:" + st.ID).Children(func() {
			optionRow(c, st.Label, desc, func() {
				if st.Status != nil && st.Status.Badge != "" {
					tone := map[string]ui.Color{"success": palette.green, "warning": palette.amber, "danger": palette.red}[st.Status.Tone]
					if tone == (ui.Color{}) {
						tone = palette.muted
					}
					ui.Text(c, st.Status.Badge).FontSize(11).FontWeight(600).TextColor(tone).Padding(1, 6).Radius(5).Background(tone.Alpha(0.14))
				}
				a.pluginSettingControl(c, pluginSetting{st, id}, set)
			})
		})
	}
}

func (a *app) pluginSettingControl(c *ui.Context, st pluginSetting, set func(any) bool) {
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
		// A draft while it's typed, saved on Enter or Save: one write,
		// and a shortcut half typed isn't refused as it goes.
		v, _ := st.Value.(string)
		k := st.plugin + "\x00" + st.ID
		draft, editing := a.settingDrafts[k]
		if !editing {
			draft = v
		}
		save := func() {
			if st.Kind == "shortcut" && draft != "" && !validShortcut(draft) {
				a.pluginNotice(ui.Toast{Title: "Not a shortcut", Description: draft + ": cmd, ctrl or alt, then a key, as cmd+shift+k"})
				return
			}
			if set(draft) {
				delete(a.settingDrafts, k) // a value refused stays to fix
			}
		}
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			e := ui.TextInput(c.Key("value"), &draft).Label(st.Label).Width(220).Placeholder(cmpOr(st.Placeholder, map[string]string{"shortcut": "cmd+shift+k"}[st.Kind]))
			if e.Changed() {
				a.settingDrafts[k] = draft
			}
			if e.Submitted() {
				save()
			}
			if editing && draft != v {
				ui.Button(c.Key("save"), "Save").OnClick(save)
			}
		})
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

// pluginSetting is a plugin's setting, with the plugin's id.
type pluginSetting struct {
	plugins.SettingState
	plugin string
}
