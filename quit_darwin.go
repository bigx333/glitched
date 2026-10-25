package main

// setUpWindowClose does nothing on macOS: the app keeps running as its
// window closes, as Mac apps do, its Quick Ask shortcut still live.
func (a *app) setUpWindowClose() {}
