package engine_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
)

// on is b as a remote child's machine logs it, at seq.
func on(t *testing.T, seq uint64, b api.EventBody) api.Event {
	t.Helper()
	payload, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return api.Event{Cursor: api.Cursor{Seq: seq}, Type: api.EventType(b), Payload: payload}
}

// A remote child's events report as a local child's runs do: a run's end
// done or failed, and a request it waits on, but not a silent run, one
// that was stopping, or one uncertain; and none already delivered.
func TestFollowReportsAsALocalChildDoes(t *testing.T) {
	done, silent, stopped, asks, unsure := api.NewID(), api.NewID(), api.NewID(), api.NewID(), api.NewID()
	question, approval := api.NewID(), api.NewID()
	body := json.RawMessage(`{"questions":[{"text":"Which?","options":[{"label":"A"}]}]}`)
	events := []api.Event{
		on(t, 1, api.TurnAdmitted{Run: done, Generation: 1}),
		on(t, 2, api.TurnStarted{Run: done}),
		on(t, 3, api.TurnEnded{Run: done, Outcome: api.OutcomeCompleted}),
		on(t, 4, api.TurnAdmitted{Run: silent, Generation: 2, Silent: true}),
		on(t, 5, api.TurnEnded{Run: silent, Outcome: api.OutcomeCompleted}),
		on(t, 6, api.TurnAdmitted{Run: stopped, Generation: 3}),
		on(t, 7, api.TurnStopping{Run: stopped}),
		on(t, 8, api.TurnEnded{Run: stopped, Outcome: api.OutcomeInterrupted}),
		on(t, 9, api.TurnAdmitted{Run: asks, Generation: 4}),
		on(t, 10, api.RequestOpened{Request: question, Run: asks, Generation: 4, Kind: api.KindQuestion, Body: body}),
		on(t, 11, api.RequestOpened{Request: approval, Run: asks, Generation: 4, Kind: api.KindApproval, Body: json.RawMessage(`{"command":"ls"}`)}),
		on(t, 12, api.TurnEnded{Run: asks, Outcome: api.OutcomeFailed, Error: "it broke"}),
		on(t, 13, api.TurnAdmitted{Run: unsure, Generation: 5}),
		on(t, 14, api.TurnUncertain{Run: unsure}),
		on(t, 15, api.TurnEnded{Run: unsure, Outcome: api.OutcomeAbandoned}),
	}
	r, got, gone, err := engine.Follow(engine.Remote{State: engine.RemoteBoundState}, events, true)
	if err != nil || gone {
		t.Fatal(err, gone)
	}
	want := []engine.RemoteReport{
		{Key: done, Run: done, Kind: api.ReportDone},
		{Key: question, Run: asks, Kind: api.ReportBlocked, Ask: body, Question: question},
		{Key: approval, Run: asks, Kind: api.ReportBlocked, Ask: json.RawMessage(`{"command":"ls"}`)},
		{Key: asks, Run: asks, Kind: api.ReportFailed, Error: "it broke"},
	}
	if len(got) != len(want) {
		t.Fatalf("reports %+v; want %+v", got, want)
	}
	for i := range want {
		if got[i].Key != want[i].Key || got[i].Run != want[i].Run || got[i].Kind != want[i].Kind || got[i].Error != want[i].Error || got[i].Question != want[i].Question || string(got[i].Ask) != string(want[i].Ask) {
			t.Fatalf("report %d: %+v; want %+v", i, got[i], want[i])
		}
	}
	if r.Run != unsure || r.Slot != api.RunIdle || r.Silent {
		t.Fatalf("the child's last run: %+v; want %v idle", r, unsure)
	}

	r.Reported = []api.ID{done, question, approval, asks}
	if _, again, _, _ := engine.Follow(r, events, true); len(again) != 0 {
		t.Fatalf("a snapshot replayed reported %+v again; want nothing", again)
	}
	// A run that went on after a restart here keeps whether it is silent.
	quiet, _, _, _ := engine.Follow(engine.Remote{}, events[3:4], false)
	if _, late, _, _ := engine.Follow(quiet, events[4:5], false); len(late) != 0 || !quiet.Silent {
		t.Fatalf("a silent run's end after a fold resumed: %+v; want nothing", late)
	}
	if _, _, gone, _ := engine.Follow(r, []api.Event{on(t, 16, api.ThreadDeleted{})}, false); !gone {
		t.Fatal("a tombstone wasn't seen")
	}
}

// placeholder is a remote child's placeholder whose machine took it.
func placeholder(t *testing.T) engine.Thread {
	t.Helper()
	at := api.Remote{Engine: api.NewID(), Link: api.NewID()}
	th := engine.Apply(engine.Thread{}, engine.Event{Thread: api.NewID(), At: time.Now(), Body: api.ThreadCreated{Title: "there", Parent: api.NewID(), Remote: &at}})
	return engine.Apply(th, engine.Event{Thread: th.ID, At: time.Now(), Body: api.RemoteBound{Thread: api.NewID()}})
}

// observe has th observe o, and returns th after.
func observe(t *testing.T, th engine.Thread, o engine.Observation) (engine.Thread, []engine.Event, []engine.Effect, error) {
	t.Helper()
	evs, effs, err := engine.Observe(th, o, time.Now())
	for _, e := range evs {
		th = engine.Apply(th, e)
	}
	return th, evs, effs, err
}

// A placeholder reports each key of its child's once, with the reply its
// machine gave, and is lost once, reporting it failed.
func TestAPlaceholderReportsEachKeyOnce(t *testing.T) {
	th := placeholder(t)
	run := api.NewID()
	seen := engine.RemoteSeen{Cursor: api.Cursor{Log: api.NewID(), Seq: 9}, Run: run, Reports: []engine.RemoteReport{{Key: run, Run: run, Kind: api.ReportDone, Reply: "all done"}}, On: "laptop"}
	th, evs, effs, err := observe(t, th, seen)
	if err != nil || len(evs) != 1 || len(effs) != 1 {
		t.Fatalf("the first sight: %v %v %v; want one remote.seen and one report", evs, effs, err)
	}
	if r := effs[0].(engine.ReportToParent); r.Run != run || r.Kind != api.ReportDone || r.Reply == nil || *r.Reply != "all done" || r.On != "laptop" || r.Parent != th.Parent {
		t.Fatalf("the report: %+v", r)
	}
	seen.Cursor.Seq = 12
	th, _, effs, err = observe(t, th, seen)
	if err != nil || len(effs) != 0 || th.Remote.Cursor.Seq != 12 || len(th.Remote.Reported) != 1 {
		t.Fatalf("the same key again: %v %v %+v; want the cursor moved and no report", effs, err, th.Remote)
	}
	th, _, effs, err = observe(t, th, engine.RemoteLost{Reason: "deleted on laptop", On: "laptop"})
	if r, ok := effs[0].(engine.ReportToParent); err != nil || !ok || r.Kind != api.ReportFailed || r.Error != "deleted on laptop" || th.Remote.State != engine.RemoteLostState {
		t.Fatalf("lost: %v %v %+v", effs, err, th.Remote)
	}
	if _, _, _, err := observe(t, th, engine.RemoteLost{Reason: "again"}); !errors.Is(err, engine.ErrRefused) {
		t.Fatalf("lost again: %v; want refused", err)
	}
	if _, _, _, err := observe(t, th, seen); !errors.Is(err, engine.ErrRefused) {
		t.Fatalf("seen once lost: %v; want refused", err)
	}
}

// A placeholder settled before its create's first try refuses the try,
// so nothing reaches its machine; one tried before goes on.
func TestARemoteCreatesFirstTryIsRefusedOnceSettled(t *testing.T) {
	at := api.Remote{Engine: api.NewID(), Link: api.NewID()}
	fresh := engine.Apply(engine.Thread{}, engine.Event{Thread: api.NewID(), At: time.Now(), Body: api.ThreadCreated{Title: "there", Parent: api.NewID(), Remote: &at}})
	settled := engine.Apply(fresh, engine.Event{Thread: fresh.ID, At: time.Now(), Body: api.DispositionChanged{Disposition: api.Disposition{State: api.Settled}}})
	if _, _, _, err := observe(t, settled, engine.RemoteAttempted{}); !errors.Is(err, engine.ErrRefused) {
		t.Fatalf("a first try on a settled placeholder: %v; want refused", err)
	}
	tried, evs, _, err := observe(t, fresh, engine.RemoteAttempted{})
	if err != nil || len(evs) != 1 || !tried.Remote.Attempted {
		t.Fatalf("a first try: %v %v", evs, err)
	}
	tried = engine.Apply(tried, engine.Event{Thread: tried.ID, At: time.Now(), Body: api.DispositionChanged{Disposition: api.Disposition{State: api.Settled}}})
	if _, evs, _, err := observe(t, tried, engine.RemoteAttempted{}); err != nil || len(evs) != 0 {
		t.Fatalf("a try again once settled: %v %v; want it allowed, recording nothing", evs, err)
	}
	if due, _ := engine.Due(settled, engine.RemoteCreate{}); due {
		t.Fatal("a create of a placeholder settled before any try is due")
	}
	if due, _ := engine.Due(tried, engine.RemoteCreate{}); !due {
		t.Fatal("a create tried before a settle is not due")
	}
}
