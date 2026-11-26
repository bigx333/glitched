package client

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"glitched/internal/api"
)

func TestANewerSweepReplacesAFailedOne(t *testing.T) {
	failed := Pending{Command: api.NewID(), Body: api.ThreadConfigure{Sweep: &api.SweepSchedule{IntervalSeconds: 3600}}, Err: errors.New("offline")}
	if !replaces(failed, api.ThreadConfigure{Sweep: &api.SweepSchedule{IntervalSeconds: 14400}}) {
		t.Fatal("a newer sweep schedule left the failed one to retry")
	}
	if replaces(failed, api.ThreadConfigure{Access: new(api.AccessAsk)}) {
		t.Fatal("an access change took the place of a failed sweep schedule")
	}
}

func TestADismissedWakesEndIsNoNews(t *testing.T) {
	t0 := time.Unix(1000, 0).UTC()
	log := api.NewID()
	var seq uint64
	event := func(at time.Time, b api.EventBody) api.Event {
		raw, err := json.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		seq++
		return api.Event{Cursor: api.Cursor{Log: log, Seq: seq}, At: at, Type: api.EventType(b), Payload: raw}
	}
	sweep := func(tr *Transcript, at time.Time) api.ID {
		run := api.NewID()
		tr.Apply(event(at, api.MessageQueued{Message: run, Text: api.DefaultSweepPrompt, Mode: api.StartNow, Auto: true, Sweep: true}))
		tr.Apply(event(at, api.TurnAdmitted{Run: run, Generation: seq, Wake: api.WakeSweep}))
		tr.Apply(event(at, api.SweepStarted{Run: run}))
		tr.Apply(event(at.Add(time.Second), api.WakeDismissed{Run: run, UpdatedAt: t0}))
		return run
	}
	var tr Transcript
	quiet := sweep(&tr, t0)
	tr.Apply(event(t0.Add(2*time.Second), api.TurnEnded{Run: quiet, Outcome: api.OutcomeCompleted}))
	if !tr.Ended.IsZero() {
		t.Fatalf("a dismissed sweep's end moved Ended to %v", tr.Ended)
	}
	raised := sweep(&tr, t0.Add(time.Minute))
	tr.Apply(event(t0.Add(time.Minute+2*time.Second), api.WakeRaised{Run: raised}))
	ended := t0.Add(time.Minute + 3*time.Second)
	tr.Apply(event(ended, api.TurnEnded{Run: raised, Outcome: api.OutcomeCompleted}))
	if !tr.Ended.Equal(ended) {
		t.Fatalf("a raised wake's end left Ended at %v, want %v", tr.Ended, ended)
	}
}

// A Manager's wake admitted quiet ends as no news, as a dismissed one did,
// until it is raised.
func TestAQuietWakesEndIsNoNews(t *testing.T) {
	t0 := time.Unix(1000, 0).UTC()
	log := api.NewID()
	var seq uint64
	event := func(at time.Time, b api.EventBody) api.Event {
		raw, err := json.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		seq++
		return api.Event{Cursor: api.Cursor{Log: log, Seq: seq}, At: at, Type: api.EventType(b), Payload: raw}
	}
	wake := func(tr *Transcript, at time.Time) api.ID {
		run := api.NewID()
		tr.Apply(event(at, api.MessageQueued{Message: run, Text: "report", Mode: api.AfterActive, Held: true, Report: api.NewID()}))
		tr.Apply(event(at, api.TurnAdmitted{Run: run, Generation: seq, Wake: api.WakeReport, Quiet: true}))
		return run
	}
	var tr Transcript
	quiet := wake(&tr, t0)
	tr.Apply(event(t0.Add(time.Second), api.TurnEnded{Run: quiet, Outcome: api.OutcomeCompleted}))
	if !tr.Ended.IsZero() {
		t.Fatalf("a quiet wake's end moved Ended to %v", tr.Ended)
	}
	raised := wake(&tr, t0.Add(time.Minute))
	tr.Apply(event(t0.Add(time.Minute+time.Second), api.WakeRaised{Run: raised, Reason: "needs a key"}))
	ended := t0.Add(time.Minute + 2*time.Second)
	tr.Apply(event(ended, api.TurnEnded{Run: raised, Outcome: api.OutcomeCompleted}))
	if !tr.Ended.Equal(ended) {
		t.Fatalf("a raised wake's end left Ended at %v, want %v", tr.Ended, ended)
	}
}
