package main

import "testing"

// A maximized window loses its window menu, which brings DWM's buttons,
// and gets it back as it is restored; nothing else of its style changes.
func TestCaptionStyle(t *testing.T) {
	const overlapped = 0x00CF0000 | 0x02000000 // WS_OVERLAPPEDWINDOW | WS_CLIPCHILDREN
	if got := captionStyle(overlapped, true); got != overlapped&^wsSysMenu {
		t.Errorf("maximized: %#x, want %#x", got, overlapped&^wsSysMenu)
	}
	if got := captionStyle(overlapped&^wsSysMenu, false); got != overlapped {
		t.Errorf("restored: %#x, want %#x", got, overlapped)
	}
	if got := captionStyle(overlapped, false); got != overlapped {
		t.Errorf("restored again: %#x, want %#x", got, overlapped)
	}
}
