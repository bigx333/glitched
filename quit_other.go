//go:build !darwin

package main

// setUpWindowClose lets the app quit as its window closes, as apps do on
// Windows and Linux, where nothing would show it again: the hidden Quick
// Ask panel, whose OnClose only hides it, would otherwise keep it running
// with no window. Destroy skips that OnClose, and the last window gone, the
// app quits.
func (a *app) setUpWindowClose() {
	a.win.OnClosed(func() {
		if a.quick.panel != nil {
			a.quick.panel.Destroy()
		}
	})
}
