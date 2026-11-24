package runtime

import (
	"encoding/json"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
)

var at = time.Unix(1_700_000_000, 0).UTC()

type history struct {
	t   *testing.T
	id  api.ID
	th  engine.Thread
	log []engine.Event
}

func (h *history) fold(evs []engine.Event, err error) {
	h.t.Helper()
	if err != nil {
		h.t.Fatal(err)
	}
	for _, e := range evs {
		h.th = engine.Apply(h.th, e)
	}
	h.log = append(h.log, evs...)
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

// continues is whether a boot at now continues h's live run unasked: the
// log has it due and its chain short, and the engine takes the continue.
func (h *history) continues(now time.Time) bool {
	r := h.th.Live()
	if r == nil || r.State != engine.RunUncertain || !dueContinue(h.log, r.ID) || chain(h.log, r.ID, now) >= chainCap {
		return false
	}
	_, _, err := engine.Decide(h.th, api.Command{ID: api.NewID(), Target: h.id, Body: api.MessageDispatch{Text: api.ContinueText, Mode: api.RestartActive, Replaces: r.ID, Auto: true}}, now)
	return err == nil
}

// lostThread is a thread whose first run a restart lost while it ran,
// then continued and lost again continues times, the last continue asked
// for at now and each before it a minute before the next. before runs on
// each run before its loss.
func lostThread(t *testing.T, parent api.ID, continues int, now time.Time, before func(h *history, run api.ID, when time.Time)) *history {
	h := &history{t: t, id: api.NewID()}
	h.submit(api.ThreadCreate{Title: "t"}, at)
	h.th.Parent = parent
	when := now.Add(-time.Duration(continues) * time.Minute)
	run := h.submit(api.MessageDispatch{Text: "go", Mode: api.StartNow}, when)
	for i := range continues + 1 {
		h.observe(engine.Started{Run: run}, when)
		if before != nil {
			before(h, run, when)
		}
		h.observe(engine.Reconciled{Run: run, Continue: true}, when)
		if i == continues {
			break
		}
		when = when.Add(time.Minute)
		run = h.submit(api.MessageDispatch{Text: api.ContinueText, Mode: api.RestartActive, Replaces: run, Auto: true}, when)
	}
	return h
}

func TestWhichLostRunsAreContinued(t *testing.T) {
	now := at.Add(time.Hour)
	for _, tc := range []struct {
		name  string
		make  func() *history
		after time.Duration // after now, when the boot looks
		want  bool
	}{
		{"a thread of the user's lost while it worked", func() *history {
			return lostThread(t, api.ID{}, 0, now, nil)
		}, 0, true},
		{"lost with continuing off", func() *history {
			h := &history{t: t, id: api.NewID()}
			h.submit(api.ThreadCreate{Title: "t"}, at)
			run := h.submit(api.MessageDispatch{Text: "go", Mode: api.StartNow}, now)
			h.observe(engine.Started{Run: run}, now)
			h.observe(engine.Reconciled{Run: run}, now)
			return h
		}, 0, false},
		{"a child", func() *history {
			return lostThread(t, api.NewID(), 0, now, nil)
		}, 0, false},
		{"lost while it waited on the user", func() *history {
			return lostThread(t, api.ID{}, 0, now, func(h *history, run api.ID, when time.Time) {
				h.observe(engine.Surfaced{Run: run, Request: api.NewID(), Dedup: "1", Kind: api.KindApproval, Body: json.RawMessage("{}")}, when)
			})
		}, 0, false},
		{"lost while it stopped", func() *history {
			return lostThread(t, api.ID{}, 0, now, func(h *history, run api.ID, when time.Time) {
				h.submit(api.TurnInterrupt{}, when)
			})
		}, 0, false},
		{"lost, then rewound", func() *history {
			h := lostThread(t, api.ID{}, 0, now, nil)
			h.observe(engine.Rewound{}, now)
			return h
		}, 0, false},
		{"snoozed", func() *history {
			h := lostThread(t, api.ID{}, 0, now, nil)
			h.submit(api.InboxSet{Disposition: api.Disposition{State: api.Snoozed, Until: now.Add(time.Hour)}}, now)
			return h
		}, 0, false},
		{"snoozed until before now", func() *history {
			h := lostThread(t, api.ID{}, 0, now, nil)
			h.submit(api.InboxSet{Disposition: api.Disposition{State: api.Snoozed, Until: now.Add(time.Minute)}}, now)
			return h
		}, 2 * time.Minute, true},
		{"settled", func() *history {
			h := lostThread(t, api.ID{}, 0, now, nil)
			h.submit(api.InboxSet{Disposition: api.Disposition{State: api.Settled}}, now)
			return h
		}, 0, false},
		{"replaced since", func() *history {
			h := lostThread(t, api.ID{}, 0, now, nil)
			h.submit(api.MessageDispatch{Text: "instead", Mode: api.RestartActive}, now)
			return h
		}, 0, false},
		{"its second continue lost", func() *history {
			return lostThread(t, api.ID{}, 2, now, nil)
		}, 0, true},
		{"its third continue lost", func() *history {
			return lostThread(t, api.ID{}, 3, now, nil)
		}, 0, false},
		{"its third continue lost, the first asked for over five minutes before", func() *history {
			return lostThread(t, api.ID{}, 3, now, nil)
		}, 3*time.Minute + time.Second, true},
	} {
		if got := tc.make().continues(now.Add(tc.after)); got != tc.want {
			t.Errorf("%s: continued %v, want %v", tc.name, got, tc.want)
		}
	}
}
