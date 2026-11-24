package main

import (
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/daemon"
	core "glitched/internal/engine"

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
	h.tt.Move(0, 0)
	h.tt.Advance(time.Second)
	h.hover("Queue")
	h.tt.Advance(2 * time.Second)
	if !h.tt.HasText("Send after this turn  ⌘⏎") {
		t.Fatalf("the Queue tooltip doesn't offer ⌘⏎: %q", h.tt.Texts())
	}
	h.click("Queue")
	h.until("the message queued by Queue", func() bool { return h.a.current().View.Queued == 2 && h.a.draft == "" })
	if v := h.a.current().View; v.Run != api.RunRunning || v.Turn == nil || v.Turn.Run != run {
		t.Fatalf("queueing changed the run: %v, turn %+v; want the first turn running", v.Run, v.Turn)
	}
}

// A run left uncertain never ends on its own, so nothing queues behind it:
// there's no Queue, and ⌘⏎ sends the message in a run that replaces it.
func TestNothingQueuesBehindAnUncertainRun(t *testing.T) {
	h := start(t, uncertainDir(t))
	th := h.thread("Stuck")
	h.a.select_(th)
	h.a.draft = "Continue after this turn."
	h.tt.Frame()
	if th.View.Run != api.RunUncertain || h.tt.HasText("Queue") {
		t.Fatalf("run %v, Queue shown %v; want an uncertain run with no Queue", th.View.Run, h.tt.HasText("Queue"))
	}
	h.tt.Key(ui.Cmd, ui.KeyEnter)
	h.until("the run replaced", func() bool { return th.View.Run != api.RunUncertain && len(th.Pending) == 0 })
	if v := h.a.current().View; v.Queued != 0 || v.LastTurn == nil || v.LastTurn.Outcome != api.OutcomeAbandoned {
		t.Fatalf("⌘⏎ on an uncertain run queued %d, last turn %+v; want none queued and the uncertain run abandoned", v.Queued, v.LastTurn)
	}
}

// With a code block open, ⌘⏎ steers the block in, so Queue doesn't offer it.
func TestQueueOffersNoShortcutThatSteers(t *testing.T) {
	h := enterReady(t, burst{}, "Start work.")
	h.until("the agent at work", func() bool { return h.a.current().View.Run == api.RunRunning })
	h.a.code = blocks(h.a, "go")
	codeSeg(h.a).text = "var x = 1"
	h.tt.Frame()
	r, ok := h.tt.Find("Queue")
	if !ok {
		t.Fatal("no Queue beside a code block")
	}
	h.tt.Move(r.X+r.W/2, r.Y+r.H/2)
	h.tt.Advance(2 * time.Second)
	if !h.tt.HasText("Send after this turn") || h.tt.HasText("Send after this turn  ⌘⏎") {
		t.Fatalf("the Queue tooltip offers ⌘⏎, which steers a code block, or isn't there: %q", h.tt.Texts())
	}
}

// A child's report that a stop held reads as waiting for the user's next
// message, even when the run completed before its interrupt landed.
func TestAHeldReportWaitsForYourNextMessage(t *testing.T) {
	id, run := api.NewID(), api.NewID()
	var state core.Thread
	fold := func(events []core.Event, _ []core.Effect, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range events {
			state = core.Apply(state, e)
		}
	}
	command := func(cid api.ID, intent api.Intent) {
		fold(core.Decide(state, api.Command{ID: cid, Target: id, Body: intent}, time.Now()))
	}
	see := func(o core.Observation) { fold(core.Observe(state, o, time.Now())) }
	command(api.NewID(), api.ThreadCreate{Title: "Parent"})
	command(run, api.MessageDispatch{Text: "work", Mode: api.StartNow})
	see(core.Started{Run: run})
	report := api.ChildReported{Child: api.NewID(), Run: api.NewID(), Kind: api.ReportDone, Title: "Child", Outcome: "done"}
	see(core.Reported{Report: report})
	command(api.NewID(), api.TurnInterrupt{})
	see(core.Ended{Run: run, Outcome: api.OutcomeCompleted})
	v := core.View(state)
	if got := wakeLine(thread{Thread: &client.Thread{View: v}}, []client.ReportEntry{{ChildReported: report, Wake: client.WakeWaiting}}); got != "↳ Goes to the agent with your next message" {
		t.Fatalf("the held report reads %q; want it waiting for your next message", got)
	}
}
