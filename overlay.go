package main

import "github.com/egoist/mygo/ui"

// The app's overlays: its dialogs, popovers and panels over the window.
// Each is built here and notes when it shows, as the question card's keys,
// which reach past the focus (OverlayShortcut), wait under any of them, as
// MyGo's window shortcuts wait behind a dialog. A frame's keys go where the
// pass before registered them, so the card reads what that pass showed.
// TestEveryOverlayIsTheApps keeps every overlay here.

func (a *app) popover(c *ui.Context, anchor ui.Element, open *bool, fn func(panel ui.Element)) {
	ui.PopoverBase(c, anchor, open, fn)
	a.covering = a.covering || *open
}

func (a *app) dialog(c *ui.Context, open *bool, fn func(backdrop, panel ui.Element)) {
	ui.DialogBase(c, open, fn)
	a.covering = a.covering || *open
}

func (a *app) alert(c *ui.Context, open *bool, title, message string, buttons ...string) int {
	chose := ui.AlertDialog(c, open, title, message, buttons...)
	a.covering = a.covering || *open
	return chose
}

// overlay builds fn over the window, which covers the app while open.
func (a *app) overlay(c *ui.Context, open bool, fn func()) {
	ui.Overlay(c, fn)
	a.covering = a.covering || open
}
