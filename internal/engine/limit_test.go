package engine_test

import (
	"errors"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
)

var autoLimit = api.MessageDispatch{Text: api.LimitContinueText, Mode: api.StartNow, Auto: true}

// resetAt is when the usage limit limited's turn ends at resets.
var resetAt = now.Add(time.Hour)

func limited(t *testing.T) (*thread, api.ID) {
	f, run := running(t)
	f.observe(engine.Ended{Run: run, Outcome: api.OutcomeFailed, Limit: &api.UsageLimit{Resets: resetAt}})
	return f, run
}

// at decides body at a time of its own, as the engine's continue is
// decided once the limit resets.
func (f *thread) at(when time.Time, body api.Intent) (api.ID, error) {
	f.t.Helper()
	id := api.NewID()
	evs, _, err := engine.Decide(f.th, api.Command{ID: id, Target: f.th.ID, Body: body}, when)
	f.fold(evs)
	return id, err
}

func refused(t *testing.T, err error, what string) {
	t.Helper()
	if !errors.As(err, new(*api.RejectedError)) {
		t.Errorf("%s: %v, want refused", what, err)
	}
}

// The engine's own continue of a turn a usage limit ended starts a turn
// once the limit resets, and only as api.Thread.LimitResume allows: not
// before, nor a day after, nor once the user cancelled it, nor on a
// thread that waits on the user, is snoozed or is a child's, nor after a
// turn no limit ended.
func TestTheEngineContinuesOnlyATurnAUsageLimitEnded(t *testing.T) {
	after := resetAt.Add(time.Minute)
	f, run := limited(t)
	if l := f.th.Runs[0].Limit; l == nil || !l.Resets.Equal(resetAt) {
		t.Fatalf("the run's limit is %+v", l)
	}
	if v := engine.View(f.th); v.LastTurn == nil || v.LastTurn.Limit == nil || v.LastTurn.Run != run {
		t.Fatalf("the view's last turn is %+v, want the limited run", v.LastTurn)
	}
	_, err := f.at(resetAt.Add(-time.Minute), autoLimit)
	refused(t, err, "a continue before the reset")
	_, err = f.at(resetAt.Add(api.LimitOverdue+time.Minute), autoLimit)
	refused(t, err, "a continue a day after the reset")
	cont, err := f.at(after, autoLimit)
	if live := f.th.Live(); err != nil || live == nil || live.ID != cont {
		t.Fatalf("live run %+v (%v), want the continue's", live, err)
	}

	f, run = limited(t)
	_, _, err = f.submit(api.ResumeCancel{Run: api.NewID()})
	refused(t, err, "a cancel of another turn")
	f.must(api.ResumeCancel{Run: run})
	if v := engine.View(f.th); !v.LastTurn.ResumeCancelled {
		t.Errorf("the view's last turn %+v, want its resume cancelled", v.LastTurn)
	}
	_, _, err = f.submit(api.ResumeCancel{Run: run})
	refused(t, err, "a second cancel")
	_, err = f.at(after, autoLimit)
	refused(t, err, "a continue the user cancelled")
	f.must(dispatch("go on", api.StartNow))

	f, _ = limited(t)
	f.must(dispatch("later", api.Defer))
	_, err = f.at(after, autoLimit)
	refused(t, err, "a continue of a thread with a message waiting")

	f, _ = limited(t)
	f.must(api.InboxSet{Disposition: api.Disposition{State: api.Snoozed, Until: resetAt.Add(time.Hour)}})
	_, err = f.at(after, autoLimit)
	refused(t, err, "a continue of a snoozed thread")

	f, _ = limited(t)
	f.th.Parent = api.NewID()
	_, err = f.at(after, autoLimit)
	refused(t, err, "a continue of a child")

	f, run = running(t)
	f.observe(engine.Ended{Run: run, Outcome: api.OutcomeFailed, Limit: &api.UsageLimit{}})
	_, err = f.at(after, autoLimit)
	refused(t, err, "a continue of a turn a limit of no known reset ended")

	f, run = running(t)
	f.observe(engine.Ended{Run: run, Outcome: api.OutcomeFailed, Error: "boom"})
	_, err = f.at(after, autoLimit)
	refused(t, err, "a continue of a turn no limit ended")
	_, _, err = f.submit(api.ResumeCancel{Run: run})
	refused(t, err, "a cancel with no limit waited on")
}

// A run the user stopped holds the thread for no usage limit, even when
// its agent says one ended it.
func TestAStoppedRunKeepsNoUsageLimit(t *testing.T) {
	f, run := running(t)
	f.must(api.TurnInterrupt{})
	f.observe(engine.Ended{Run: run, Outcome: api.OutcomeFailed, Limit: &api.UsageLimit{Resets: resetAt}})
	if v := engine.View(f.th); v.LastTurn == nil || v.LastTurn.Limit != nil {
		t.Fatalf("the view's last turn is %+v, want one no limit holds", v.LastTurn)
	}
}
