package main

import (
	"cmp"
	"fmt"
	"slices"

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
		if e := effortLabel(m, a.effort); e != "" {
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
			pluginNotice(c, "needs", toneWarn, r.family.name+" is not installed", func() {
				ui.Text(c, r.info.Error).Selectable().FontSize(12).TextColor(palette.muted)
			}, func() { a.checkAgain(c) })
		case r.info.State == transport.ProviderSignedOut:
			pluginNotice(c, "needs", toneWarn, r.family.name+" is signed out", func() {
				ui.Text(c, r.info.Error).Selectable().FontSize(12).TextColor(palette.muted)
			}, func() { a.checkAgain(c) })
		case r.heard && r.served && r.info.Error != "":
			pluginNotice(c, "needs", toneBad, r.family.name+" didn't say what it runs", func() {
				ui.Text(c, clip(r.info.Error, 1200)).Selectable().Font("monospace").FontSize(11.5).TextColor(palette.text).MaxLines(12)
			}, func() { a.checkAgain(c) })
		}
		a.providerModels(c, r)
		if r.info.Info.Path != "" || r.version() != "" {
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

// providerModels are the models the provider's engine offers, each a
// choice of the default for new threads: the same one the picker and
// General choose, model and effort together.
func (a *app) providerModels(c *ui.Context, r providerRow) {
	models, _ := a.rowModels(r)
	if len(models) == 0 {
		return
	}
	inset(c, "MODELS", func() {
		chosen := a.model
		ui.RadioGroup(c, func() {
			for _, m := range models {
				_, offered := a.modelOf(m.key)
				ui.Row(c).Key(m.key.String()).Padding(10, 12).GapY(8).GapX(12).AlignItems(ui.Center).Wrap().Children(func() {
					ui.Row(c).Grow(1).Shrink(1).Basis(220).Gap(10).AlignItems(ui.Center).Children(func() {
						ui.Radio(c.Key("default"), &chosen, m.key, "").Label("Default model: " + m.name)
						ui.Column(c).Grow(1).Shrink(1).Gap(2).Children(func() {
							ui.Text(c, m.short).FontWeight(500)
							if m.desc != "" {
								ui.Text(c, m.desc).FontSize(12).TextColor(palette.muted)
							}
						})
					})
					if !offered {
						statePill(c, "No longer offered", toneWarn)
					}
					if m.key == a.model {
						es := effortsOf(m)
						labels := make([]string, len(es))
						for i, e := range es {
							labels[i] = e.label
						}
						shown := cmp.Or(effortLabel(m, a.effort), labels[0])
						if ui.Select(c.Key("effort"), &shown, labels).Label("Effort of " + m.name).Width(150).Changed() {
							a.effort = es[slices.Index(labels, shown)].id
						}
					}
				})
			}
		}).Label("Default model for new threads").Dividers(1, palette.border)
		if chosen != a.model {
			a.chooseModel(chosen)
		}
		a.saveComposer()
	})
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

// providersVariant picks the layout of Settings › Providers being tried.
var providersVariant = "A"

// providers is Settings › Providers: every provider the engine serves.
func (a *app) providers(c *ui.Context) {
	a.askModels()
	rows := a.providerRows()
	if len(rows) == 0 {
		ui.Text(c, "Waiting for the engine to say what it runs.").Key("none").TextColor(palette.muted).Padding(0, 4)
		return
	}
	if providersVariant == "B" {
		a.providerTabs(c, rows)
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

// providerTabs is a tab per provider over the chosen one's detail.
func (a *app) providerTabs(c *ui.Context, rows []providerRow) {
	names := make([]string, len(rows))
	for i, r := range rows {
		names[i] = r.family.name
	}
	a.providersTab = min(a.providersTab, len(rows)-1)
	ui.Row(c).Gap(12).AlignItems(ui.Center).Wrap().Children(func() {
		ui.Segmented(c.Key("tabs"), &a.providersTab, names...).Label("Provider")
	})
	r := rows[a.providersTab]
	ui.Column(c).Key("provider:"+r.family.provider).Radius(12).Border(1, palette.border).Background(palette.surface).Clip().Children(func() {
		ui.Row(c).Padding(12, 16, 12, 14).Gap(12).AlignItems(ui.Center).Children(func() { a.providerHead(c, r) })
		ui.Column(c).BorderWidth(1, 0, 0, 0).BorderColor(palette.border).Background(palette.bg.Alpha(0.45)).Children(func() { a.providerDetail(c, r) })
	})
}
