package engine

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"glitched/internal/api"
)

func TestManagerCreateAndConfigureSweep(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	mgrID := api.NewID()
	evs, _, err := Decide(Thread{}, api.Command{
		ID:     api.NewID(),
		Target: mgrID,
		Body: api.ThreadCreate{
			Workspace: "/repo",
			Title:     "Repo Manager",
			Model:     api.ModelRef{Provider: "claude", Model: "sonnet"},
			Access:    api.AccessAutoEdits,
			Role:      api.RoleManager,
			Sweep:     &api.SweepSchedule{IntervalSeconds: 1800, Prompt: "Check workers"},
		},
	}, now)
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}
	var th Thread
	for i, e := range evs {
		e.Seq = uint64(i + 1)
		th = Apply(th, e)
	}
	if th.Role != api.RoleManager || th.Sweep == nil || th.Sweep.IntervalSeconds != 1800 {
		t.Fatalf("unexpected manager thread state: %+v", th)
	}

	_, _, err = Decide(Thread{}, api.Command{
		ID:     api.NewID(),
		Target: api.NewID(),
		Body: api.ThreadCreate{
			Workspace: "/repo",
			Title:     "Child",
			Model:     api.ModelRef{Provider: "claude", Model: "sonnet"},
			Parent:    mgrID,
			Role:      api.RoleManager,
		},
	}, now)
	if !errors.As(err, new(*api.RejectedError)) {
		t.Fatalf("child manager err = %v, want RejectedError", err)
	}

	regID := api.NewID()
	regEvs, _, err := Decide(Thread{}, api.Command{
		ID:     api.NewID(),
		Target: regID,
		Body:   api.ThreadCreate{Workspace: "/repo", Title: "Normal", Model: api.ModelRef{Provider: "claude", Model: "sonnet"}},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	var reg Thread
	for i, e := range regEvs {
		e.Seq = uint64(i + 1)
		reg = Apply(reg, e)
	}
	if _, _, err := Decide(reg, api.Command{
		ID:     api.NewID(),
		Target: regID,
		Body:   api.ThreadConfigure{Sweep: &api.SweepSchedule{IntervalSeconds: 60}},
	}, now); !errors.As(err, new(*api.RejectedError)) {
		t.Fatalf("non-manager sweep configure err = %v, want RejectedError", err)
	}

	cfgEvs, _, err := Decide(th, api.Command{
		ID:     api.NewID(),
		Target: mgrID,
		Body:   api.ThreadConfigure{Sweep: &api.SweepSchedule{IntervalSeconds: 0}},
	}, now.Add(time.Second))
	if err != nil {
		t.Fatalf("clear sweep: %v", err)
	}
	for _, e := range cfgEvs {
		th = Apply(th, e)
	}
	if th.Sweep != nil {
		t.Fatalf("expected nil Sweep after disable, got %+v", th.Sweep)
	}
}

type mgr struct {
	t  *testing.T
	th Thread
	at time.Time
}

func newManager(t *testing.T, sweep *api.SweepSchedule) *mgr {
	m := &mgr{t: t, at: time.Unix(10_000, 0).UTC()}
	m.must(api.ThreadCreate{Workspace: "/repo", Title: "Manager", Model: api.ModelRef{Provider: "claude", Model: "sonnet"}, Role: api.RoleManager, Sweep: sweep})
	return m
}

func (m *mgr) fold(evs []Event) []Event {
	for _, e := range evs {
		m.th = Apply(m.th, e)
	}
	return evs
}

func (m *mgr) submit(b api.Intent) ([]Event, error) {
	m.t.Helper()
	m.at = m.at.Add(time.Second)
	target := m.th.ID
	if target.IsZero() {
		target = api.NewID()
	}
	evs, _, err := Decide(m.th, api.Command{ID: api.NewID(), Target: target, Body: b}, m.at)
	return m.fold(evs), err
}

func (m *mgr) must(b api.Intent) []Event {
	m.t.Helper()
	evs, err := m.submit(b)
	if err != nil {
		m.t.Fatalf("%T: %v", b, err)
	}
	return evs
}

func (m *mgr) observe(o Observation) ([]Event, []Effect) {
	m.t.Helper()
	m.at = m.at.Add(time.Second)
	evs, effs, err := Observe(m.th, o, m.at)
	if err != nil {
		m.t.Fatalf("%T: %v", o, err)
	}
	return m.fold(evs), effs
}

func (m *mgr) sweep() api.ID {
	m.t.Helper()
	at, ok := NextSweepAt(m.th, m.at)
	if !ok {
		m.t.Fatal("no sweep is due")
	}
	m.at = at
	run := api.NewID()
	m.observe(SweepDue{Run: run})
	m.observe(Started{Run: run})
	return run
}

func (m *mgr) report(kind api.ReportKind) api.ChildReported {
	m.t.Helper()
	r := api.ChildReported{Child: api.NewID(), Run: api.NewID(), Kind: kind, Title: "worker"}
	m.observe(Reported{Report: r})
	return r
}

func (m *mgr) dismiss() {
	m.t.Helper()
	m.must(api.WakeDismiss{Run: m.th.Live().ID})
}

func (m *mgr) end() {
	m.t.Helper()
	m.observe(Ended{Run: m.th.Live().ID, Outcome: api.OutcomeCompleted})
}

func emitted[B api.EventBody](evs []Event) bool {
	for _, e := range evs {
		if _, ok := e.Body.(B); ok {
			return true
		}
	}
	return false
}

func TestADismissedSweepLeavesTheThreadAsItWas(t *testing.T) {
	m := newManager(t, &api.SweepSchedule{IntervalSeconds: 60})
	before := m.th.UpdatedAt
	m.sweep()
	if !m.th.UpdatedAt.After(before) {
		t.Fatalf("a live sweep shows as new activity: UpdatedAt %v, was %v", m.th.UpdatedAt, before)
	}
	m.dismiss()
	m.observe(Work{Item: api.WorkItem{Run: m.th.Live().ID, Item: "tool-1", Kind: "tool"}})
	m.end()
	if !m.th.UpdatedAt.Equal(before) {
		t.Fatalf("UpdatedAt after a dismissed sweep = %v, want %v", m.th.UpdatedAt, before)
	}
	v := View(m.th)
	if v.LastTurn != nil || v.Live() {
		t.Fatalf("a dismissed sweep left LastTurn %+v, run %v", v.LastTurn, v.Run)
	}
	if !m.th.LastSweep.Equal(m.th.Runs[0].Woke) {
		t.Fatalf("LastSweep %v, want the sweep's admission %v", m.th.LastSweep, m.th.Runs[0].Woke)
	}
}

func TestOnlyAManagersWakeIsDismissed(t *testing.T) {
	refused := func(t *testing.T, m *mgr) {
		t.Helper()
		if _, err := m.submit(api.WakeDismiss{Run: m.th.Live().ID}); !errors.As(err, new(*api.RejectedError)) {
			t.Fatalf("WakeDismiss = %v, want RejectedError", err)
		}
	}
	model := api.ModelRef{Provider: "claude", Model: "sonnet"}
	t.Run("plain", func(t *testing.T) {
		m := &mgr{t: t, at: time.Unix(10_000, 0).UTC()}
		m.must(api.ThreadCreate{Workspace: "/repo", Title: "Plain", Model: model, Text: "work"})
		refused(t, m)
	})
	t.Run("child", func(t *testing.T) {
		m := &mgr{t: t, at: time.Unix(10_000, 0).UTC()}
		m.must(api.ThreadCreate{Workspace: "/repo", Title: "Worker", Model: model, Parent: api.NewID(), Run: api.NewID(), Text: "work"})
		refused(t, m)
		_, effs := m.observe(Ended{Run: m.th.Live().ID, Outcome: api.OutcomeCompleted})
		if len(effs) != 1 {
			t.Fatalf("the worker's end reported %v, want one report to its parent", effs)
		}
	})
	t.Run("manager's user turn", func(t *testing.T) {
		m := newManager(t, nil)
		m.must(api.MessageDispatch{Text: "do it", Mode: api.StartNow})
		refused(t, m)
	})
	t.Run("wake the user steered", func(t *testing.T) {
		m := newManager(t, &api.SweepSchedule{IntervalSeconds: 60})
		m.sweep()
		m.must(api.MessageDispatch{Text: "also this", Mode: api.SteerActive})
		refused(t, m)
	})
}

func TestADismissedWakeKeepsWorkingAndAsksTheUser(t *testing.T) {
	m := newManager(t, &api.SweepSchedule{IntervalSeconds: 60})
	before := m.th.UpdatedAt
	run := m.sweep()
	m.dismiss()
	worker := Thread{ID: api.NewID(), Parent: m.th.ID}
	if err := CheckCaller([]Thread{worker, m.th}, api.Caller{Thread: m.th.ID, Run: run}, false); err != nil {
		t.Fatalf("CheckCaller after a dismissal: %v", err)
	}
	evs, effs := m.observe(Surfaced{Run: run, Request: api.NewID(), Dedup: "edit", Kind: api.KindApproval, Body: []byte(`{"tool":"Edit"}`)})
	if emitted[api.TurnStopping](evs) || len(effs) != 0 {
		t.Fatalf("a dismissed wake's request stopped it: %v, %v", evs, effs)
	}
	v := View(m.th)
	if len(v.Requests) != 1 || v.Turn == nil || v.Turn.Silent || !m.th.UpdatedAt.After(before) {
		t.Fatalf("the request shows nothing: requests %v, turn %+v, UpdatedAt %v", v.Requests, v.Turn, m.th.UpdatedAt)
	}
	if _, err := m.submit(api.WakeDismiss{Run: run}); !errors.As(err, new(*api.RejectedError)) {
		t.Fatalf("dismissing a wake the user was asked about = %v, want RejectedError", err)
	}
}

func TestARestartDuringADismissedWakeLeavesItUncertain(t *testing.T) {
	m := newManager(t, &api.SweepSchedule{IntervalSeconds: 60})
	run := m.sweep()
	m.dismiss()
	evs, _ := m.observe(Reconciled{Run: run})
	if !emitted[api.TurnUncertain](evs) || emitted[api.TurnEnded](evs) {
		t.Fatalf("a restart during a dismissed wake: %v, want it uncertain", evs)
	}
}

func TestDismissingAWakeKeepsTheReportsItDidNotTake(t *testing.T) {
	t.Run("sweep", func(t *testing.T) {
		m := newManager(t, &api.SweepSchedule{IntervalSeconds: 60})
		m.report(api.ReportBlocked)
		alerted := m.th.Reported
		m.sweep()
		m.report(api.ReportDone)
		reported := m.th.Reported
		m.dismiss()
		if !m.th.Reported.Equal(reported) || reported.Equal(alerted) {
			t.Fatalf("a dismissed sweep moved Reported to %v, want %v", m.th.Reported, reported)
		}
	})
	t.Run("report after a blocked one", func(t *testing.T) {
		m := newManager(t, nil)
		m.report(api.ReportBlocked)
		alerted := m.th.Reported
		m.report(api.ReportDone)
		if m.th.Live() == nil || m.th.Live().Wake != api.WakeReport {
			t.Fatal("the report started no wake")
		}
		m.dismiss()
		if !m.th.Reported.Equal(alerted) {
			t.Fatalf("Reported after the wake's dismissal = %v, want the blocked report's %v", m.th.Reported, alerted)
		}
	})
	t.Run("report alone", func(t *testing.T) {
		m := newManager(t, nil)
		m.report(api.ReportDone)
		m.dismiss()
		m.end()
		if !m.th.Reported.IsZero() || View(m.th).LastTurn != nil {
			t.Fatalf("a dismissed report's wake left Reported %v, LastTurn %+v", m.th.Reported, View(m.th).LastTurn)
		}
	})
	t.Run("newer report", func(t *testing.T) {
		m := newManager(t, nil)
		m.report(api.ReportDone)
		m.report(api.ReportDone)
		reported := m.th.Reported
		m.dismiss()
		if !m.th.Reported.Equal(reported) {
			t.Fatalf("a dismissal erased a newer report: Reported %v, want %v", m.th.Reported, reported)
		}
	})
}

func TestASweepWaitsForALimitsContinue(t *testing.T) {
	m := newManager(t, &api.SweepSchedule{IntervalSeconds: 60})
	m.must(api.MessageDispatch{Text: "go", Mode: api.StartNow})
	resets := m.at.Add(time.Hour)
	m.observe(Ended{Run: m.th.Live().ID, Outcome: api.OutcomeFailed, Limit: &api.UsageLimit{Resets: resets}})
	at, ok := NextSweepAt(m.th, m.at)
	if !ok || !at.After(resets.Add(api.LimitOverdue)) {
		t.Fatalf("the next sweep is due at %v (%v), want after the continue's window, %v", at, ok, resets.Add(api.LimitOverdue))
	}
	for _, now := range []time.Time{m.at.Add(time.Minute), resets.Add(time.Second)} {
		if _, _, err := Observe(m.th, SweepDue{Run: api.NewID()}, now); !errors.Is(err, ErrRefused) {
			t.Fatalf("a sweep at %v while the limit's continue waits = %v, want ErrRefused", now, err)
		}
	}
	if _, _, err := Observe(m.th, SweepDue{Run: api.NewID()}, resets.Add(api.LimitOverdue+time.Second)); err != nil {
		t.Fatalf("a sweep once the continue is overdue: %v", err)
	}
}

func TestALimitADismissedSweepHitHoldsTheNextSweepUntilItResets(t *testing.T) {
	m := newManager(t, &api.SweepSchedule{IntervalSeconds: 60})
	run := m.sweep()
	m.dismiss()
	resets := m.at.Add(time.Hour)
	m.observe(Ended{Run: run, Outcome: api.OutcomeFailed, Limit: &api.UsageLimit{Resets: resets}})
	if at, ok := NextSweepAt(m.th, m.at); !ok || !at.Equal(resets) {
		t.Fatalf("the next sweep is due at %v (%v), want when the limit resets, %v", at, ok, resets)
	}
	for _, now := range []time.Time{m.at.Add(time.Minute), resets.Add(-time.Second)} {
		if _, _, err := Observe(m.th, SweepDue{Run: api.NewID()}, now); !errors.Is(err, ErrRefused) {
			t.Fatalf("a sweep at %v, before the limit resets at %v = %v, want ErrRefused", now, resets, err)
		}
	}
	if _, _, err := Observe(m.th, SweepDue{Run: api.NewID()}, resets); err != nil {
		t.Fatalf("a sweep once the limit reset: %v", err)
	}
}

func TestADismissedSweepKeepsAReportThatCameInIt(t *testing.T) {
	m := newManager(t, &api.SweepSchedule{IntervalSeconds: 60})
	m.sweep()
	m.report(api.ReportBlocked)
	blocked := m.th.UpdatedAt
	m.dismiss()
	m.end()
	if !m.th.UpdatedAt.Equal(blocked) || !m.th.Reported.Equal(blocked) {
		t.Fatalf("after the dismissal UpdatedAt %v, Reported %v, want the blocked report's %v", m.th.UpdatedAt, m.th.Reported, blocked)
	}
}

func TestTurningSweepsOffKeepsTheirPrompt(t *testing.T) {
	const prompt = "Only check release blockers"
	m := newManager(t, &api.SweepSchedule{IntervalSeconds: 14400, Prompt: prompt})
	m.must(api.ThreadConfigure{Sweep: &api.SweepSchedule{Prompt: prompt}})
	if s := m.th.Sweep; s == nil || s.IntervalSeconds != 0 || s.Prompt != prompt {
		t.Fatalf("sweeps off left %+v, want the prompt kept", s)
	}
	if _, ok := NextSweepAt(m.th, m.at); ok {
		t.Fatal("a sweep is scheduled with sweeps off")
	}
	m.must(api.ThreadConfigure{Sweep: &api.SweepSchedule{IntervalSeconds: 900, Prompt: prompt}})
	if s := m.th.Sweep; s == nil || s.IntervalSeconds != 900 || s.Prompt != prompt {
		t.Fatalf("sweeps back on = %+v", s)
	}
}

func TestAManagerStaysInTheInbox(t *testing.T) {
	for _, d := range []api.Disposition{
		{State: api.Snoozed, Until: time.Unix(20_000, 0).UTC()},
		{State: api.Settled},
	} {
		m := newManager(t, nil)
		if _, err := m.submit(api.InboxSet{Disposition: d}); !errors.As(err, new(*api.RejectedError)) {
			t.Fatalf("InboxSet %v on a Manager = %v, want RejectedError", d.State, err)
		}
		if m.th.Disposition.State != api.Open {
			t.Fatalf("a refused %v left the Manager %v", d.State, m.th.Disposition.State)
		}
	}
	t.Run("open", func(t *testing.T) {
		m := newManager(t, nil)
		m.must(api.InboxSet{Disposition: api.Disposition{}})
		if m.th.Disposition.State != api.Open {
			t.Fatalf("Open left the Manager %v", m.th.Disposition.State)
		}
	})
	for _, d := range []api.Disposition{
		{State: api.Snoozed, Until: time.Unix(20_000, 0).UTC()},
		{State: api.Settled},
	} {
		t.Run("stored "+fmt.Sprint(d.State), func(t *testing.T) {
			m := newManager(t, nil)
			m.th.Disposition = d // as a store kept it from before Managers stayed open
			m.must(api.InboxSet{Disposition: api.Disposition{}})
			if m.th.Disposition.State != api.Open {
				t.Fatalf("Open left a stored %v Manager %v", d.State, m.th.Disposition.State)
			}
		})
	}
	t.Run("plain thread", func(t *testing.T) {
		m := &mgr{t: t, at: time.Unix(10_000, 0).UTC()}
		m.must(api.ThreadCreate{Workspace: "/repo", Title: "Plain", Model: api.ModelRef{Provider: "claude", Model: "sonnet"}})
		d := api.Disposition{State: api.Snoozed, Until: m.at.Add(time.Hour)}
		m.must(api.InboxSet{Disposition: d})
		if m.th.Disposition != d {
			t.Fatalf("snooze left a plain thread %+v, want %+v", m.th.Disposition, d)
		}
		m.must(api.InboxSet{Disposition: api.Disposition{State: api.Settled}})
		if m.th.Disposition.State != api.Settled {
			t.Fatalf("settle left a plain thread %v", m.th.Disposition.State)
		}
	})
}

func (m *mgr) runSweep(prompt string) ([]Event, []Effect, api.ID, error) {
	m.t.Helper()
	m.at = m.at.Add(time.Second)
	id := api.NewID()
	evs, effs, err := Decide(m.th, api.Command{ID: id, Target: m.th.ID, Body: api.SweepRun{Prompt: prompt}}, m.at)
	m.fold(evs)
	return evs, effs, id, err
}

func TestARunSweepStartsASweepWake(t *testing.T) {
	cases := []struct {
		name   string
		sweep  *api.SweepSchedule
		prompt string
		want   string
	}{
		{"sweeps off, default prompt", nil, "", api.DefaultSweepPrompt},
		{"sweeps off, configured prompt", &api.SweepSchedule{Prompt: "  Check the release  "}, "", "Check the release"},
		{"scheduled, prompt given", &api.SweepSchedule{IntervalSeconds: 3600, Prompt: "Check the release"}, "  Only the blockers ", "Only the blockers"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := newManager(t, c.sweep)
			before := m.th.UpdatedAt
			evs, effs, id, err := m.runSweep(c.prompt)
			if err != nil {
				t.Fatalf("SweepRun: %v", err)
			}
			live := m.th.Live()
			if live == nil || live.ID != id || live.Wake != api.WakeSweep || !live.Prior.Equal(before) {
				t.Fatalf("the sweep's run = %+v, want wake %v under the command's ID, prior %v", live, api.WakeSweep, before)
			}
			if !emitted[api.SweepStarted](evs) || !m.th.LastSweep.Equal(m.at) {
				t.Fatalf("LastSweep = %v, want %v", m.th.LastSweep, m.at)
			}
			if len(effs) != 1 {
				t.Fatalf("effects = %v, want one StartTurn", effs)
			}
			if st, ok := effs[0].(StartTurn); !ok || st.Run != id || st.Text != c.want {
				t.Fatalf("effect = %+v, want StartTurn of %q", effs[0], c.want)
			}
			m.dismiss()
			m.end()
			if !m.th.UpdatedAt.Equal(before) || View(m.th).LastTurn != nil {
				t.Fatalf("a dismissed sweep left UpdatedAt %v (was %v), LastTurn %+v", m.th.UpdatedAt, before, View(m.th).LastTurn)
			}
		})
	}
	t.Run("stored settled", func(t *testing.T) {
		m := newManager(t, nil)
		m.th.Disposition = api.Disposition{State: api.Settled}
		if _, _, _, err := m.runSweep(""); err != nil {
			t.Fatalf("SweepRun: %v", err)
		}
		if m.th.Disposition.State != api.Open || m.th.Live() == nil {
			t.Fatalf("a sweep on a stored settled Manager left it %v, run %v", m.th.Disposition.State, m.th.Live())
		}
	})
}

func TestARunSweepIsRefused(t *testing.T) {
	model := api.ModelRef{Provider: "claude", Model: "sonnet"}
	refused := func(t *testing.T, m *mgr) {
		t.Helper()
		evs, effs, _, err := m.runSweep("")
		if !errors.As(err, new(*api.RejectedError)) || len(evs) != 0 || len(effs) != 0 {
			t.Fatalf("SweepRun = %v, %v, %v, want RejectedError", evs, effs, err)
		}
	}
	t.Run("plain thread", func(t *testing.T) {
		m := &mgr{t: t, at: time.Unix(10_000, 0).UTC()}
		m.must(api.ThreadCreate{Workspace: "/repo", Title: "Plain", Model: model})
		refused(t, m)
	})
	t.Run("child", func(t *testing.T) {
		m := &mgr{t: t, at: time.Unix(10_000, 0).UTC()}
		m.must(api.ThreadCreate{Workspace: "/repo", Title: "Worker", Model: model, Parent: api.NewID()})
		m.th.Role = api.RoleManager
		refused(t, m)
	})
	t.Run("busy", func(t *testing.T) {
		m := newManager(t, nil)
		m.must(api.MessageDispatch{Text: "go", Mode: api.StartNow})
		refused(t, m)
	})
	t.Run("sweeping", func(t *testing.T) {
		m := newManager(t, &api.SweepSchedule{IntervalSeconds: 60})
		m.sweep()
		refused(t, m)
	})
	t.Run("agent", func(t *testing.T) {
		m := newManager(t, nil)
		parent := api.NewID()
		m.at = m.at.Add(time.Second)
		_, _, err := Decide(m.th, api.Command{ID: api.NewID(), Target: m.th.ID, Body: api.SweepRun{}, Caller: &api.Caller{Thread: parent}}, m.at)
		if !errors.As(err, new(*api.RejectedError)) {
			t.Fatalf("an agent's SweepRun = %v, want RejectedError", err)
		}
	})
}
