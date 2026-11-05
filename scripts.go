package main

import (
	_ "embed"

	"github.com/egoist/mygo/ui"

	"glitched/mygoscript"
)

var (
	//go:embed scripts/counter.luau
	counterScript string
	//go:embed scripts/widgets.luau
	widgetsScript string
)

// scripts is the mygoscript demo: a counter and a tour of the widgets,
// which Luau scripts draw with native widgets, from state the app keeps.
func (a *app) scripts(c *ui.Context) {
	if a.counter == nil {
		a.counter = mygoscript.New("counter.luau", counterScript)
	}
	if a.widgetTour == nil {
		a.widgetTour = mygoscript.New("widgets.luau", widgetsScript)
	}
	card(c, "COUNTER.LUAU", func() {
		ui.Column(c).Padding(16).Children(func() {
			mygoscript.View(c, a.counter)
		})
	})
	card(c, "WIDGETS.LUAU", func() {
		ui.Column(c).Padding(16).Children(func() {
			mygoscript.View(c, a.widgetTour)
		})
	})
}
