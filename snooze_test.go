package main

import (
	"testing"
	"time"
)

// The snooze popover opens next to the clock button it came from: under
// it, from its left edge, clear of the title bar and the traffic lights,
// in the window, whatever the sidebar's width; on a row near the window's
// bottom it goes above the button instead.
func TestSnoozePopoverHangsFromItsButton(t *testing.T) {
	for _, tc := range []struct {
		name   string
		width  float32 // the sidebar's
		height int     // the window's
		row    string
	}{
		{"narrow sidebar", minSidebarWidth, 860, "SIP re-INVITE handling"},
		{"wide sidebar", maxSidebarWidth, 860, "SIP re-INVITE handling"},
		// The window at its least height, a row at the inbox's bottom.
		{"short window", 272, 480, "Fix terminal resize flicker on Windows"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := tempDir(t)
			inbox(t, dir)
			h := start(t, dir)
			h.a.sidebarWidth = tc.width
			h.tt.SetSize(1320, tc.height)
			th := h.thread(tc.row)
			h.tt.Advance(time.Second)
			h.hover(th.View.Title)
			clock, ok := h.tt.Find("Snooze")
			if !ok {
				t.Fatal("no Snooze button")
			}
			h.click("Snooze")
			h.tt.Advance(time.Second)
			head, ok1 := h.tt.Find("Snooze until…")
			last, ok2 := h.tt.Find("a long vacation")
			if !ok1 || !ok2 {
				t.Fatalf("no popover: %q", h.tt.Texts())
			}
			top, bottom := head.Y, last.Y+last.H
			below := top >= clock.Y+clock.H
			above := bottom <= clock.Y
			switch {
			case !below && !above:
				t.Errorf("the popover, %v to %v, covers its button at %v", top, bottom, clock)
			case below && tc.height >= 860:
			case below:
				t.Errorf("in a %d-high window the popover went below, to %v", tc.height, bottom)
			case tc.height >= 860:
				t.Errorf("with room below, the popover went above its button, from %v", top)
			}
			if top < headerHeight {
				t.Errorf("the popover's top, %v, is in the title bar", top)
			}
			// From the button's left edge, over the chat rather than the
			// sidebar's header, give or take its padding.
			if head.X < clock.X-4 || head.X > clock.X+24 {
				t.Errorf("the popover's text starts at %v, its button at %v", head.X, clock.X)
			}
			if bottom > float32(tc.height) {
				t.Errorf("the popover runs out of the window, to %v", bottom)
			}
		})
	}
}
