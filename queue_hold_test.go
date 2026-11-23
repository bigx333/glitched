package main

import (
	"testing"
	"time"

	"glitched/internal/api"
)

// A Stop holds the message queued behind the run: it shows Held with a
// Resume beside it, nothing starts, and Resume starts its turn.
func TestAStopHoldsTheQueueUntilResume(t *testing.T) {
	h, th := asking(t, runTests)
	next := h.a.backend.Send(th.env, th.id(), "Then update the docs.", api.AfterActive)
	h.until("the queued message", func() bool { return h.tt.HasText("Then update the docs.") && h.tt.HasText("Queued") })
	h.click("Stop")
	h.until("the held message", func() bool {
		return !h.a.current().View.Live() && h.tt.HasText("Held") && h.tt.HasText("Resume")
	})
	h.tt.Advance(time.Second)
	if v := h.a.current().View; v.Live() || v.Queued != 1 {
		t.Fatalf("after the stop the thread is live %v with %d queued; want it quiet with the message held", v.Live(), v.Queued)
	}
	h.click("Resume")
	h.until("the held message's turn", func() bool {
		v := h.a.current().View
		return v.Turn != nil && v.Turn.Run == next && !h.tt.HasText("Resume")
	})
}
