package main

import (
	_ "embed"

	"github.com/egoist/mygo/ui"

	"glitched/mygoscript"
)

//go:embed scripts/counter.luau
var counterScript string

// scripts is the mygoscript demo: a counter that a Luau script draws with
// native widgets, from state the app keeps.
func (a *app) scripts(c *ui.Context) {
	if a.counter == nil {
		a.counter = mygoscript.New("counter.luau", counterScript)
	}
	card(c, "COUNTER.LUAU", func() {
		ui.Column(c).Padding(16).Children(func() {
			mygoscript.View(c, a.counter)
		})
	})
}
