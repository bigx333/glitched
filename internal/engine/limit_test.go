package engine_test

import (
	"errors"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
)

var autoLimit = api.MessageDispatch{Text: api.LimitContinueText, Mode: api.StartNow, Auto: true}

func limited(t *testing.T) (*thread, api.ID) {
	f, run := running(t)
	f.observe(engine.Ended{Run: run, Outcome: api.OutcomeFailed, Limit: &api.UsageLimit{Resets: now.Add(time.Hour)}})
	return f, run
}

func refused(t *testing.T, err error, what string) {
	t.Helper()
	if !errors.As(err, new(*api.RejectedError)) {
		t.Errorf("%s: %v, want refused", what, err)
	}
}

// The engine's own continue of a turn a usage limit ended starts a turn,
// unless the user cancelled it, or the thread is a child's, snoozed or
// settled, or its last turn ended some other way.
func TestTheEngineContinuesOnlyATurnAUsageLimitEnded(t *testing.T) {
	f, run := limited(t)
	if l := f.th.Runs[0].Limit; l == nil || !l.Resets.Equal(now.Add(time.Hour)) {
		t.Fatalf("the run's limit is %+v", l)
	}
	if v := engine.View(f.th); v.LastTurn == nil || v.LastTurn.Limit == nil || v.LastTurn.Run != run {
		t.Fatalf("the view's last turn is %+v, want the limited run", v.LastTurn)
	}
	cont := f.must(autoLimit)
	if live := f.th.Live(); live == nil || live.ID != cont {
		t.Fatalf("live run %+v, want the continue's", live)
	}

	f, _ = limited(t)
	f.must(api.ResumeCancel{})
	if v := engine.View(f.th); !v.LastTurn.ResumeCancelled {
		t.Errorf("the view's last turn %+v, want its resume cancelled", v.LastTurn)
	}
	_, _, err := f.submit(api.ResumeCancel{})
	refused(t, err, "a second cancel")
	_, _, err = f.submit(autoLimit)
	refused(t, err, "a continue the user cancelled")
	f.must(dispatch("go on", api.StartNow))

	f, _ = limited(t)
	f.must(api.InboxSet{Disposition: api.Disposition{State: api.Snoozed, Until: now.Add(2 * time.Hour)}})
	_, _, err = f.submit(autoLimit)
	refused(t, err, "a continue of a snoozed thread")

	f, _ = limited(t)
	f.th.Parent = api.NewID()
	_, _, err = f.submit(autoLimit)
	refused(t, err, "a continue of a child")

	f, run = running(t)
	f.observe(engine.Ended{Run: run, Outcome: api.OutcomeFailed, Error: "boom"})
	_, _, err = f.submit(autoLimit)
	refused(t, err, "a continue of a turn no limit ended")
	_, _, err = f.submit(api.ResumeCancel{})
	refused(t, err, "a cancel with no limit waited on")
}
