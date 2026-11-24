package runtime

import (
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
)

var at = time.Unix(1_700_000_000, 0).UTC()

type history struct {
	t  *testing.T
	id api.ID
	th engine.Thread
}

func (h *history) fold(evs []engine.Event, err error) {
	h.t.Helper()
	if err != nil {
		h.t.Fatal(err)
	}
	for _, e := range evs {
		h.th = engine.Apply(h.th, e)
	}
}

func (h *history) submit(body api.Intent, now time.Time) api.ID {
	h.t.Helper()
	id := api.NewID()
	evs, _, err := engine.Decide(h.th, api.Command{ID: id, Target: h.id, Body: body}, now)
	h.fold(evs, err)
	return id
}

func (h *history) observe(o engine.Observation, now time.Time) {
	h.t.Helper()
	evs, _, err := engine.Observe(h.th, o, now)
	h.fold(evs, err)
}

// lostThread is a thread whose first run a restart lost while it ran,
// then continued and lost again continues times, the last continue lost at
// now and each before it a minute before the next.
func lostThread(t *testing.T, parent api.ID, continues int, now time.Time) (*history, lostRun) {
	h := &history{t: t, id: api.NewID()}
	h.submit(api.ThreadCreate{Title: "t"}, at)
	h.th.Parent = parent
	when := now.Add(-time.Duration(continues) * time.Minute)
	run := h.submit(api.MessageDispatch{Text: "go", Mode: api.StartNow}, when)
	for i := range continues + 1 {
		h.observe(engine.Started{Run: run}, when)
		h.observe(engine.Reconciled{Run: run}, when)
		if i == continues {
			break
		}
		when = when.Add(time.Minute)
		run = h.submit(api.MessageDispatch{Text: api.ContinueText, Mode: api.RestartActive, Replaces: run}, when)
	}
	return h, lostRun{thread: h.th.ID, run: run, was: engine.RunRunning}
}

func TestWhichLostRunsAreContinued(t *testing.T) {
	now := at.Add(time.Hour)
	for _, tc := range []struct {
		name string
		make func() (engine.Thread, lostRun)
		want bool
	}{
		{"a thread of the user's lost while it worked", func() (engine.Thread, lostRun) {
			h, l := lostThread(t, api.ID{}, 0, now)
			return h.th, l
		}, true},
		{"a child", func() (engine.Thread, lostRun) {
			h, l := lostThread(t, api.NewID(), 0, now)
			return h.th, l
		}, false},
		{"lost while it waited on the user", func() (engine.Thread, lostRun) {
			h, l := lostThread(t, api.ID{}, 0, now)
			l.was = engine.RunWaiting
			return h.th, l
		}, false},
		{"lost while it stopped", func() (engine.Thread, lostRun) {
			h, l := lostThread(t, api.ID{}, 0, now)
			l.was = engine.RunStopping
			return h.th, l
		}, false},
		{"snoozed", func() (engine.Thread, lostRun) {
			h, l := lostThread(t, api.ID{}, 0, now)
			h.th.Disposition = api.Disposition{State: api.Snoozed, Until: now.Add(time.Hour)}
			return h.th, l
		}, false},
		{"snoozed until before now", func() (engine.Thread, lostRun) {
			h, l := lostThread(t, api.ID{}, 0, now)
			h.th.Disposition = api.Disposition{State: api.Snoozed, Until: now.Add(-time.Minute)}
			return h.th, l
		}, true},
		{"replaced since", func() (engine.Thread, lostRun) {
			h, l := lostThread(t, api.ID{}, 0, now)
			h.submit(api.MessageDispatch{Text: "instead", Mode: api.RestartActive}, now)
			return h.th, l
		}, false},
		{"its second continue lost", func() (engine.Thread, lostRun) {
			h, l := lostThread(t, api.ID{}, 2, now)
			return h.th, l
		}, true},
		{"its third continue lost", func() (engine.Thread, lostRun) {
			h, l := lostThread(t, api.ID{}, 3, now)
			return h.th, l
		}, false},
		{"its third continue lost, the first long ago", func() (engine.Thread, lostRun) {
			h, l := lostThread(t, api.ID{}, 3, now.Add(-chainWindow))
			return h.th, l
		}, true},
	} {
		th, l := tc.make()
		if got := mayContinue(th, l, now); got != tc.want {
			t.Errorf("%s: continued %v, want %v (chain %d)", tc.name, got, tc.want, chain(th, now))
		}
	}
}
