package engine

import (
	"errors"
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

	// Child threads cannot hold RoleManager or sweeps.
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

	// Regular thread rejects scheduling sweeps.
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

	// Clearing sweep on Manager sets Sweep to nil.
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

// mgr is a thread under test: its state, and the clock its commands and
// observations land at.
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

// sweep starts a due sweep, an interval after the thread last changed.
func (m *mgr) sweep() api.ID {
	m.t.Helper()
	at, ok := NextSweep(m.th)
	if !ok {
		m.t.Fatal("no sweep is due")
	}
	m.at = at
	run := api.NewID()
	m.observe(SweepDue{Run: run})
	m.observe(Started{Run: run})
	return run
}

// report is a worker's report: done, or blocked on an approval.
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

// A dismissed sweep leaves the thread where it was in the list, with no
// turn to read, and survives the log's replay as it was decided.
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

// Only a Manager's wake is dismissed: a plain or child thread's run, or
// a turn the user asked for or joined, is the user's to read.
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

// A dismissed wake still runs as its Manager's turn: it steers its
// workers, and a request it opens is the user's to see and answer.
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

// An engine restart during a dismissed wake leaves it uncertain, as any
// turn of the user's: it is no compaction.
func TestARestartDuringADismissedWakeLeavesItUncertain(t *testing.T) {
	m := newManager(t, &api.SweepSchedule{IntervalSeconds: 60})
	run := m.sweep()
	m.dismiss()
	evs, _ := m.observe(Reconciled{Run: run})
	if !emitted[api.TurnUncertain](evs) || emitted[api.TurnEnded](evs) {
		t.Fatalf("a restart during a dismissed wake: %v, want it uncertain", evs)
	}
}

// Dismissing a wake dismisses only the reports it took: a sweep's none,
// and a report's wake never a blocked worker's or a newer report.
func TestDismissingAWakeKeepsTheReportsItDidNotTake(t *testing.T) {
	t.Run("sweep", func(t *testing.T) {
		m := newManager(t, &api.SweepSchedule{IntervalSeconds: 60})
		m.report(api.ReportBlocked)
		alerted := m.th.Reported
		m.sweep()
		m.report(api.ReportDone) // waits for the sweep
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
		m.report(api.ReportDone) // waits for the wake
		reported := m.th.Reported
		m.dismiss()
		if !m.th.Reported.Equal(reported) {
			t.Fatalf("a dismissal erased a newer report: Reported %v, want %v", m.th.Reported, reported)
		}
	})
}

// A snoozed Manager's wake decides whether the user sees the report: the
// thread stays snoozed while it runs, and as it is when dismissed.
func TestASnoozedManagersWakeDecidesWhetherItComesBack(t *testing.T) {
	snoozed := func(t *testing.T) (*mgr, api.Disposition) {
		m := newManager(t, nil)
		d := api.Disposition{State: api.Snoozed, Until: m.at.Add(4 * time.Hour)}
		m.must(api.InboxSet{Disposition: d})
		return m, d
	}
	t.Run("dismissed", func(t *testing.T) {
		m, d := snoozed(t)
		m.report(api.ReportDone)
		if m.th.Live() == nil || m.th.Disposition != d {
			t.Fatalf("the report's wake: run %v, disposition %+v, want it running snoozed", m.th.Live(), m.th.Disposition)
		}
		m.dismiss()
		m.end()
		if m.th.Disposition != d {
			t.Fatalf("a dismissed wake left the thread %+v, want %+v", m.th.Disposition, d)
		}
	})
	t.Run("ended", func(t *testing.T) {
		m, _ := snoozed(t)
		m.report(api.ReportDone)
		m.end()
		if m.th.Disposition.State != api.Open {
			t.Fatalf("an undismissed wake left the thread %+v, want it back in the inbox", m.th.Disposition)
		}
	})
	t.Run("asked", func(t *testing.T) {
		m, _ := snoozed(t)
		m.report(api.ReportDone)
		m.observe(Started{Run: m.th.Live().ID})
		m.dismiss()
		m.observe(Surfaced{Run: m.th.Live().ID, Request: api.NewID(), Dedup: "q", Kind: api.KindApproval, Body: []byte(`{}`)})
		if m.th.Disposition.State != api.Open {
			t.Fatalf("a wake's request left the thread %+v, want it back in the inbox", m.th.Disposition)
		}
	})
	t.Run("asked before dismiss", func(t *testing.T) {
		m, _ := snoozed(t)
		m.report(api.ReportDone)
		m.observe(Started{Run: m.th.Live().ID})
		m.observe(Surfaced{Run: m.th.Live().ID, Request: api.NewID(), Dedup: "q", Kind: api.KindApproval, Body: []byte(`{}`)})
		if m.th.Disposition.State != api.Open {
			t.Fatalf("a wake's request left the thread %+v, want it back in the inbox", m.th.Disposition)
		}
		if _, err := m.submit(api.WakeDismiss{Run: m.th.Live().ID}); !errors.As(err, new(*api.RejectedError)) {
			t.Fatalf("WakeDismiss after Surfaced = %v, want RejectedError", err)
		}
	})
	t.Run("approval", func(t *testing.T) {
		m, _ := snoozed(t)
		m.report(api.ReportBlocked)
		if m.th.Disposition.State != api.Open {
			t.Fatalf("a worker's approval left the thread %+v, want it back in the inbox", m.th.Disposition)
		}
	})
	t.Run("stopped", func(t *testing.T) {
		m, _ := snoozed(t)
		m.must(api.MessageDispatch{Text: "go", Mode: api.StartNow})
		m.must(api.TurnInterrupt{})
		m.observe(Ended{Run: m.th.Live().ID, Outcome: api.OutcomeInterrupted})
		m.report(api.ReportDone)
		if m.th.Live() != nil || m.th.Disposition.State != api.Open {
			t.Fatalf("a wake a stop holds: run %v, disposition %+v, want the report back in the inbox", m.th.Live(), m.th.Disposition)
		}
	})
}

// A sweep waits out the continue of a turn a usage limit ended.
func TestASweepWaitsForALimitsContinue(t *testing.T) {
	m := newManager(t, &api.SweepSchedule{IntervalSeconds: 60})
	m.must(api.MessageDispatch{Text: "go", Mode: api.StartNow})
	resets := m.at.Add(time.Hour)
	m.observe(Ended{Run: m.th.Live().ID, Outcome: api.OutcomeFailed, Limit: &api.UsageLimit{Resets: resets}})
	at, ok := NextSweep(m.th)
	if !ok {
		t.Fatal("no sweep is scheduled")
	}
	for _, now := range []time.Time{at, resets.Add(time.Second)} {
		if _, _, err := Observe(m.th, SweepDue{Run: api.NewID()}, now); !errors.Is(err, ErrRefused) {
			t.Fatalf("a sweep at %v while the limit's continue waits = %v, want ErrRefused", now, err)
		}
	}
	if _, _, err := Observe(m.th, SweepDue{Run: api.NewID()}, resets.Add(api.LimitOverdue+time.Second)); err != nil {
		t.Fatalf("a sweep once the continue is overdue: %v", err)
	}
}
