package main

import (
	"time"

	"github.com/egoist/mygo"
)

// setUpWindowClose has the window's close button hide it on macOS, as Mac
// apps' do: the app runs on, its Quick Ask shortcut live and its terminals'
// shells running, and a click on the Dock icon, or a thread started from
// Quick Ask, shows the window again. A quit closes it.
func (a *app) setUpWindowClose() {
	a.win.OnClose(a.hideUnlessQuitting(a.hideWindow))
	mygo.App.OnActivate(func(bool) {
		// A hide waiting on full screen goes even while the window is still
		// visible, on its way out of it, or it would hide what was clicked for.
		a.cancelHide()
		if !a.win.IsVisible() {
			a.win.Show()
		}
	})
}

// hideWindow hides the window. One in full screen leaves it first, and
// hides once it has: hidden there, it would leave its Space behind, empty
// and black.
func (a *app) hideWindow() {
	if !a.win.IsFullScreen() {
		a.win.Hide()
		return
	}
	if a.leavingFullScreen != nil {
		return // already on its way out of full screen
	}
	done := false
	hide := func() {
		if done {
			return
		}
		a.leavingFullScreen()
		if !a.quitting && !a.win.IsFullScreen() {
			a.win.Hide()
		}
	}
	off := a.win.OnLeaveFullScreen(hide)
	a.leavingFullScreen = func() {
		done = true
		off()
		a.leavingFullScreen = nil
	}
	a.win.SetFullScreen(false)
	// macOS can drop the request, as when the window is still going into
	// full screen, and says nothing: after a while, the hide is off, or
	// goes ahead if the window did leave, and the next close tries again.
	time.AfterFunc(3*time.Second, func() { a.win.Update(hide) })
}

// cancelHide drops a hide waiting on the window to leave full screen, for
// what shows the window: the hide would undo the show.
func (a *app) cancelHide() {
	if a.leavingFullScreen != nil {
		a.leavingFullScreen()
	}
}
