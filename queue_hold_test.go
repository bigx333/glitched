package main

import (
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/daemon"

	"github.com/egoist/mygo/ui"
)

// A Stop holds the message queued behind the run: it shows Held, Resume
// shows beside Send, nothing starts, and Resume starts its turn.
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

// A child's report a stop held is resumable from the composer too, with
// no message of the user's held.
func TestAResumeSendsAReportAStopHeld(t *testing.T) {
	h, _, ad, _ := heldReport(t)
	ad.park(api.ID{})
	if v := h.a.current().View; v.Live() || v.Queued != 1 || !v.Resumable {
		t.Fatalf("the parent is live %v with %d queued, resumable %v; want it quiet with the report waiting", v.Live(), v.Queued, v.Resumable)
	}
	h.click("Resume")
	h.until("the report sent to the agent", func() bool { return h.tt.HasText("↳ Sent to the agent") })
}

// While the agent works, ⌘⏎ and Queue send the draft for after its turn,
// where Send steers it into the turn: the run goes on, the messages wait.
func TestADraftQueuesBehindTheAgentAtWork(t *testing.T) {
	dir := tempDir(t)
	engine(t, daemon.Config{Dir: dir}, burst{})
	h := start(t, dir)
	h.a.draft = "Fix the sidebar."
	h.tt.Frame()
	h.click("Send")
	h.until("the agent at work", func() bool { return h.tt.HasText("Stop") && h.a.current().View.Run == api.RunRunning })
	run := h.a.current().View.Turn.Run
	h.a.draft = "Then update the docs."
	h.tt.Frame()
	h.tt.Key(ui.Cmd, ui.KeyEnter)
	h.until("the message queued by ⌘⏎", func() bool {
		return h.a.current().View.Queued == 1 && h.a.draft == "" && h.tt.HasText("Then update the docs.") && h.tt.HasText("Queued")
	})
	h.a.draft = "And the changelog."
	h.tt.Frame()
	h.click("Queue")
	h.until("the message queued by Queue", func() bool { return h.a.current().View.Queued == 2 && h.a.draft == "" })
	if v := h.a.current().View; v.Run != api.RunRunning || v.Turn == nil || v.Turn.Run != run {
		t.Fatalf("queueing changed the run: %v, turn %+v; want the first turn running", v.Run, v.Turn)
	}
}
