package main

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// A thread opened from the palette scrolls the sidebar to its row, the
// built-in's and Glitched's own.
func TestTheSidebarScrollsToAThreadOpenedFromThePalette(t *testing.T) {
	for _, builtin := range []bool{true, false} {
		h := start(t, pagedDir(t, pagedBatch{name: "Paged", n: 60}))
		h.until("the threads", func() bool { return len(h.a.itemsIn(catInbox)) == 60 })
		if !builtin {
			if err := h.a.plugins.SetEnabled("sidebar", false); err != nil {
				t.Fatal(err)
			}
			h.a.pluginsChanged(nil)
		}
		settlePlugins(t, h.a, h.tt, h.p)
		inSidebar := func(title string) bool {
			for _, x := range h.tt.AccessAll() {
				if strings.HasPrefix(x.Name, title+" ") && x.Bounds.X < 270 && x.Bounds.Y >= 0 && x.Bounds.Y+x.Bounds.H <= 860 {
					return true
				}
			}
			return false
		}
		if inSidebar("Paged 003") {
			t.Fatal("the sidebar shows Paged 003 before it is opened")
		}
		h.a.openCommandPalette()
		paletteShows(t, h)
		h.tt.Type("Paged 003")
		paletteShows(t, h)
		h.tt.Key(0, ui.KeyEnter)
		settlePlugins(t, h.a, h.tt, h.p)
		h.tt.Frame()
		h.tt.Frame()
		if h.a.current().View.Title != "Paged 003" || !inSidebar("Paged 003") {
			t.Errorf("built-in %v: the sidebar doesn't show Paged 003, opened from the palette", builtin)
		}
	}
}
