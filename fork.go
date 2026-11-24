package main

import (
	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
)

// A thread forks from a finished turn into a new thread of its own, from
// the toolbar under that turn's reply or, at its last turn, from its row's
// menu, which also continues it on another provider: the same, as a fork
// there. The source stays as it was. The fork opens on its composer, and
// its timeline starts with where it goes on from.

// fork opens a new thread that goes on from run of t, its last finished
// turn when zero, on model.
func (a *app) fork(t thread, run api.ID, model api.ModelRef) {
	name := title(t) + " fork"
	if model.Provider != t.View.Model.Provider {
		name = title(t) + " on " + providerName(model.Provider)
	}
	if f, ok := a.find(a.backend.Fork(t.env, t.View, run, name, model)); ok {
		a.choose(f)
		a.follow()
	}
}

// forkDivider is the first row of a fork's timeline: the thread it goes on
// from, which opens it while it's there, and how its first run took the
// conversation up, once one has.
func (a *app) forkDivider(c *ui.Context, t thread, e entry) {
	f := e.fork
	verb := "Forked from"
	if f.Provider != t.View.Model.Provider {
		verb = "Continued from"
	}
	src, found := a.find(f.Thread)
	ui.Row(c).Key(e.key).Gap(10).AlignItems(ui.Center).Padding(4, 12).Transition(riseIn).Children(func() {
		ui.Box(c).Height(1).Grow(1).Background(palette.border)
		ui.Text(c, verb).FontSize(11.5).TextColor(palette.faint).SingleLine()
		if found {
			b := ghost(c, "fork-source", false).Padding(2, 6).Label("Open " + f.Title).Children(func() {
				ui.Text(c, f.Title).FontSize(11.5).SingleLine().Shrink(1)
			})
			if b.Clicked() {
				a.choose(src)
			}
		} else {
			ui.Text(c, f.Title).FontSize(11.5).TextColor(palette.faint).SingleLine().Shrink(1)
		}
		if f.Took != "" {
			kind, tip := "Native", "The agent's own conversation, forked at that turn"
			if f.Took == api.RewindPortable {
				kind, tip = "Portable", "A new conversation, handed the turns up to there"
			}
			a.tip(c, ui.Box(c).Label(kind).Padding(1, 6).Radius(6).Border(1, palette.border).Children(func() {
				ui.Text(c, kind).FontSize(10.5).TextColor(palette.faint).AccessHidden(true)
			}), tip)
		}
		ui.Box(c).Height(1).Grow(1).Background(palette.border)
	})
}
