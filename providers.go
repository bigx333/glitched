package main

import (
	"cmp"
	"fmt"

	"github.com/egoist/mygo/ui"

	"glitched/internal/transport"
)

// providerRow is one provider as Settings › Providers shows it: its
// family, what its engine last said of it, and whether it has said.
type providerRow struct {
	family family
	info   transport.ProviderInfo
	heard  bool
	served bool
}

// providerRows are the providers the engine serves, in the picker's
// order, and the default model's though none serves it now. One that
// runs and offers no models is a placeholder, as the picker has it, and
// isn't listed. Before the engine first answers, they are those it
// offered models of when it last did, as models.json keeps them.
func (a *app) providerRows() []providerRow {
	var out []providerRow
	for _, f := range a.providerFamilies() {
		info, served := a.models.served[f.provider]
		heard := a.models.served != nil
		offers := len(a.models.reported[f.provider]) > 0
		listed := served && (offers || info.State != "" || info.Error != "") || !heard && offers
		if !listed && a.model.Provider != f.provider {
			continue
		}
		out = append(out, providerRow{family: f, info: info, heard: heard, served: served || !heard})
	}
	return out
}

// standing is the provider's state in a word or two, and its tone.
func (r providerRow) standing() (string, tone) {
	switch {
	case !r.heard:
		return "Checking…", toneQuiet
	case !r.served:
		return "Not on this engine", toneWarn
	case r.info.State == transport.ProviderNotInstalled:
		return "Not installed", toneQuiet
	case r.info.State == transport.ProviderSignedOut:
		return "Signed out", toneWarn
	case r.info.Error != "":
		return "Didn't answer", toneBad
	}
	return "Ready", toneGood
}

// version is the provider's version as its row shows it.
func (r providerRow) version() string {
	if r.info.Info.Version == "" {
		return ""
	}
	return "v" + r.info.Info.Version
}

// rowModels are the provider's offered models, and the default where
// its engine no longer offers it, which says so.
func (a *app) rowModels(r providerRow) (models []modelInfo, gone bool) {
	models = a.familyModels(r.family)
	if m, offered := a.modelOf(a.model); !offered && a.model.Provider == r.family.provider {
		models = append(models, m)
		gone = true
	}
	return models, gone
}

// defaultLine is what the row says of the default model, where it is the
// provider's, else how many models it offers.
func (a *app) defaultLine(r providerRow) string {
	models, gone := a.rowModels(r)
	if a.model.Provider == r.family.provider {
		m, _ := a.modelOf(a.model)
		line := "Default · " + m.short
		if e := effortLabel(m, effortFor(m, a.effort)); e != "" {
			line += " · " + e
		}
		if gone {
			line += " · No longer offered"
		}
		return line
	}
	switch len(models) {
	case 0:
		return "No models"
	case 1:
		return "1 model"
	}
	return fmt.Sprintf("%d models", len(models))
}

func effortLabel(m modelInfo, id string) string {
	for _, e := range effortsOf(m) {
		if e.id == id {
			return e.label
		}
	}
	return ""
}

// providerDetail is everything about one provider: what to do before it
// runs, its models with the default for new threads, and the program
// Glitched runs.
func (a *app) providerDetail(c *ui.Context, r providerRow) {
	ui.Column(c).Gap(18).Padding(16).Children(func() {
		switch {
		case r.info.State == transport.ProviderNotInstalled:
			pluginNotice(c, "needs", toneWarn, "Install "+r.family.name, func() {
				ui.Text(c, r.info.Error).Selectable().FontSize(12).TextColor(palette.muted)
			}, func() { a.checkAgain(c) })
		case r.info.State == transport.ProviderSignedOut:
			pluginNotice(c, "needs", toneWarn, "Sign in to "+r.family.name, func() {
				ui.Text(c, r.info.Error).Selectable().FontSize(12).TextColor(palette.muted)
			}, func() { a.checkAgain(c) })
		case r.heard && r.served && r.info.Error != "":
			pluginNotice(c, "needs", toneBad, r.family.name+" didn't say what it runs", func() {
				ui.Text(c, clip(r.info.Error, 1200)).Selectable().Font("monospace").FontSize(11.5).TextColor(palette.text).MaxLines(12)
			}, func() { a.checkAgain(c) })
		}
		a.providerDefault(c, r)
		if r.info.Info.Path != "" || r.version() != "" || r.info.Info.NoAuto != "" {
			inset(c, "PROGRAM", func() {
				if r.info.Info.Path != "" {
					optionRow(c, "Path", "The program Glitched runs.", func() {
						ui.Text(c, displayPath(r.info.Info.Path)).Selectable().Font("monospace").FontSize(12).TextColor(palette.muted)
					})
				}
				if v := r.version(); v != "" {
					optionRow(c, "Version", "", func() {
						ui.Text(c, v).Selectable().FontSize(12).TextColor(palette.muted)
					})
				}
				if s := r.info.Info.NoAuto; s != "" {
					optionRow(c, accessAuto, s+", so Approve for me approves file edits and asks before the rest.", func() {})
				}
			})
		}
	})
}

func (a *app) checkAgain(c *ui.Context) {
	label := "Check again"
	if a.models.asking {
		label = "Checking…"
	}
	ui.Button(c.Key("check"), label).Disabled(a.models.asking).OnClick(a.checkProviders)
}

// defaultChoice is a model with the effort it runs at: what the default
// for new threads is, and what one item of a provider's control picks.
type defaultChoice struct {
	model  modelKey
	effort string
}

// providerDefault is the provider's one control of the default for new
// threads: the same one the picker and General choose, model and effort
// together, from the models its engine reports. Its menu lists them, and
// the efforts of the default where it is the provider's.
func (a *app) providerDefault(c *ui.Context, r providerRow) {
	models, gone := a.rowModels(r)
	if len(models) == 0 {
		return
	}
	ours := a.model.Provider == r.family.provider
	inset(c, "DEFAULT FOR NEW THREADS", func() {
		optionRow(c, "Default model", "New threads start on it. The composer's picker chooses the same one.", func() {
			m, _ := a.modelOf(a.model)
			effort := effortFor(m, a.effort)
			chosen := defaultChoice{a.model, effort}
			sel := ui.SelectBase(c.Key("default"), &chosen)
			label := "Choose a model"
			if ours {
				label = m.short + " · " + cmp.Or(effortLabel(m, effort), effort)
			}
			sel.Trigger.Label("Default model for new threads").Description(a.defaultLine(r)).Padding(5, 8, 5, 10).Gap(6).AlignItems(ui.Center).
				Radius(6).Border(1, palette.border).Background(palette.bg.Alpha(0.6)).Cursor(ui.CursorPointer).Children(func() {
				col := palette.text
				if !ours {
					col = palette.muted
				}
				ui.Text(c, label).FontSize(12.5).TextColor(col).SingleLine()
				if ours && gone {
					ui.Text(c, "No longer offered").FontSize(11.5).FontWeight(550).TextColor(palette.amber).SingleLine()
				}
				ui.Icon(c, icChevronD).FontSize(11).TextColor(palette.muted)
			})
			sel.Popup(func(panel ui.Element) {
				panel.Padding(4).MinWidth(240).Radius(10).Border(1, palette.border).Background(palette.surface).Shadow(0, 12, 32, 0, ui.RGBA(0, 0, 0, 0.3))
				item := func(v defaultChoice, text, note string, checked bool) {
					it := sel.Item(v).Padding(6, 10).Gap(8).Radius(6).AlignItems(ui.Center)
					if it.Highlighted() {
						it.Background(palette.hover)
					}
					it.Children(func() {
						ui.Box(c).Width(14).Shrink(0).Children(func() {
							if checked {
								ui.Icon(c, icCheck).FontSize(12).TextColor(palette.accent)
							}
						})
						ui.Text(c, text).FontSize(12.5).Grow(1).SingleLine()
						if note != "" {
							ui.Text(c, note).FontSize(11.5).TextColor(palette.amber).SingleLine()
						}
					})
				}
				popupLabel(c, "MODEL")
				for _, o := range models {
					note := ""
					if _, offered := a.modelOf(o.key); !offered {
						note = "No longer offered"
					}
					item(defaultChoice{o.key, effortFor(o, a.effort)}, o.short, note, o.key == a.model)
				}
				if !ours {
					return
				}
				ui.Divider(c).Background(palette.border).Margin(4, 0)
				popupLabel(c, "EFFORT")
				for _, e := range effortsOf(m) {
					item(defaultChoice{a.model, e.id}, e.label, "", e.id == effort)
				}
			})
			if chosen != (defaultChoice{a.model, effort}) {
				if chosen.model != a.model {
					a.chooseModel(chosen.model)
				}
				a.effort = chosen.effort
				a.saveComposer()
			}
		})
	})
}

// popupLabel heads a group of a menu's items, in line with their text.
func popupLabel(c *ui.Context, s string) {
	ui.Text(c, s).FontSize(10.5).FontWeight(600).LetterSpacing(0.6).TextColor(palette.faint).Padding(6, 10, 2, 32)
}

// providerHead is a provider in a line: its mark, name and version, what
// it says of the default, and how it stands.
func (a *app) providerHead(c *ui.Context, r providerRow) {
	state, t := r.standing()
	modelTile(c, r.family.icon, r.family.color, 28)
	ui.Column(c).Grow(1).Shrink(1).Gap(2).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, r.family.name).FontWeight(600).SingleLine().Shrink(1)
			if v := r.version(); v != "" {
				ui.Text(c, v).FontSize(11.5).TextColor(palette.faint).SingleLine().Shrink(0)
			}
		})
		ui.Text(c, a.defaultLine(r)).FontSize(12).TextColor(palette.muted).SingleLine()
	})
	statePill(c, state, t)
}

// providers is Settings › Providers: every provider the engine serves.
func (a *app) providers(c *ui.Context) {
	a.askModels()
	rows := a.providerRows()
	if len(rows) == 0 {
		ui.Text(c, "Waiting for the engine to say what it runs.").Key("none").TextColor(palette.muted).Padding(0, 4)
		return
	}
	ui.Column(c).Radius(12).Border(1, palette.border).Background(palette.surface).Clip().Dividers(1, palette.border).Children(func() {
		for _, r := range rows {
			a.providerRowA(c, r)
		}
	})
}

// providerRowA is a provider in a line that opens to its detail, as a
// plugin's row does.
func (a *app) providerRowA(c *ui.Context, r providerRow) {
	open := a.providersOpen[r.family.provider]
	was := open
	state, _ := r.standing()
	ui.Column(c).Key("provider:" + r.family.provider).Children(func() {
		var parts ui.CollapsibleParts
		head := ui.Row(c).AlignItems(ui.Center).Transition(hoverFade)
		if head.Hovered() {
			head.Background(palette.hover)
		}
		head.Children(func() {
			parts = ui.CollapsibleBase(c.Key("open"), &open)
			parts.Trigger.Label(r.family.name).Description(state+". "+a.defaultLine(r)).Grow(1).Shrink(1).Padding(12, 16, 12, 14).Gap(12).AlignItems(ui.Center).
				Cursor(ui.CursorPointer).Radius(8).FocusRingOffset(-3).Children(func() {
				ui.Icon(c, icChevronR).FontSize(14).TextColor(palette.faint).Shrink(0).Rotate(90 * parts.Progress())
				a.providerHead(c, r)
			})
		})
		if open != was {
			a.providersOpen[r.family.provider] = open
		}
		panel := parts.Panel(func() { a.providerDetail(c, r) })
		if panel.Valid() {
			panel.BorderWidth(1, 0, 0, 0).BorderColor(palette.border).Background(palette.bg.Alpha(0.45))
		}
	})
}
