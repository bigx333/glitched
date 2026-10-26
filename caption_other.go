//go:build !windows

package main

// setUpCaption does nothing: only Windows draws its window buttons
// through the window's material (caption_windows.go).
func (a *app) setUpCaption() {}
