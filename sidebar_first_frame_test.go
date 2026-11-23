package main

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// A window's first frame lays the sidebar out for the first time, so it
// has no height of its own yet: Settled's copy gets the window's half to
// scroll in, not none. The sidebar built-in draws what its first frame
// asked of it before the next frame comes as often as not on a loaded
// machine, and a list of no height fails it.
func TestAWindowsFirstFrameGivesSettledRoomToScroll(t *testing.T) {
	h := start(t, pagedDir(t, pagedBatch{name: "Settled", n: 3, settled: true}))
	h.until("the threads", func() bool { return len(h.a.itemsIn(catSettled)) == 3 })
	h.a.sectionClosed[catSettled] = false
	settlePlugins(t, h.a, h.tt, h.p)
	h.tt = ui.NewTester(h.a.view, 1320, 860)
	h.tt.Frame()
	deadline := time.After(20 * time.Second)
	for !h.a.plugins.Idle() {
		select {
		case <-h.p.wake:
		case <-time.After(10 * time.Millisecond): // Idle changes with nothing posted
		case <-deadline:
			t.Fatal("the plugins never settled")
		}
	}
	h.p.run()
	h.tt.Frame()
	settlePlugins(t, h.a, h.tt, h.p)
	if !h.tt.HasText("Settled 002") {
		t.Errorf("Settled's rows don't show: %q", h.tt.Texts())
	}
}
