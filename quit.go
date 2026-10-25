package main

import (
	"slices"

	"github.com/egoist/mygo"
)

// beforeQuit marks the app quitting as a quit starts, before MyGo closes
// the windows, in whatever order: the windows that hide on close then let
// it close them, and the quit completes. A listener after this one, a
// window's OnClose or an OnWillQuit can still cancel the quit; MyGo says
// nothing of it, so on the main loop's next turn, once the quit has gone
// through or not, the mark comes off if the window is still open.
func (a *app) beforeQuit(*mygo.QuitEvent) {
	a.quitting = true
	if a.nextTurn != nil {
		a.nextTurn(func() {
			if slices.Contains(mygo.Windows(), a.win) {
				a.quitting = false
			}
		})
	}
}

// hideUnlessQuitting is an OnClose listener that hides the window instead
// of closing it, unless the app is quitting.
func (a *app) hideUnlessQuitting(hide func()) func(*mygo.CloseEvent) {
	return func(e *mygo.CloseEvent) {
		if a.quitting {
			return
		}
		e.PreventDefault()
		hide()
	}
}
