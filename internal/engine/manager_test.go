package engine

import (
	"errors"
	"fmt"
	"slices"
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

// quiet is whether the Manager's live run shows as a quiet wake.
func (m *mgr) quiet() bool {
	v := View(m.th)
	return v.Turn != nil && v.Turn.Silent && v.Turn.Dismissed
}

func TestASweepIsQuietUntilRaised(t *testing.T) {
	m := newManager(t, &api.SweepSchedule{IntervalSeconds: 60})
	before := m.th.UpdatedAt
	m.sweep()
	if !m.quiet() || !m.th.UpdatedAt.Equal(before) {
		t.Fatalf("a live sweep: turn %+v, UpdatedAt %v; want quiet, %v", View(m.th).Turn, m.th.UpdatedAt, before)
	}
	m.observe(Work{Item: api.WorkItem{Run: m.th.Live().ID, Item: "tool-1", Kind: "tool"}})
	m.end()
	if !m.th.UpdatedAt.Equal(before) {
		t.Fatalf("UpdatedAt after a quiet sweep = %v, want %v", m.th.UpdatedAt, before)
	}
	v := View(m.th)
	if v.LastTurn != nil || v.Live() {
		t.Fatalf("a quiet sweep left LastTurn %+v, run %v", v.LastTurn, v.Run)
	}
	if !m.th.LastSweep.Equal(m.th.Runs[0].Woke) {
		t.Fatalf("LastSweep %v, want the sweep's admission %v", m.th.LastSweep, m.th.Runs[0].Woke)
	}
}

// A worker's report to a Manager, and the wake it starts, move nothing:
// the Manager vets it quietly. A plain thread's report wake is the user's.
func TestAReportWakeIsQuietOnlyOnAManager(t *testing.T) {
	m := newManager(t, nil)
	before := m.th.UpdatedAt
	r := m.report(api.ReportDone)
	if live := m.th.Live(); live == nil || live.Wake != api.WakeReport || !m.quiet() {
		t.Fatalf("the report's wake = %+v, want a quiet report wake", live)
	}
	if !m.th.UpdatedAt.Equal(before) || m.th.Reported.IsZero() || m.th.LastReport == nil || m.th.LastReport.Child != r.Child {
		t.Fatalf("after the report: UpdatedAt %v (want %v), Reported %v, LastReport %+v", m.th.UpdatedAt, before, m.th.Reported, m.th.LastReport)
	}
	m.end()
	if !m.th.UpdatedAt.Equal(before) || View(m.th).LastTurn != nil {
		t.Fatalf("a quiet report wake left UpdatedAt %v, LastTurn %+v", m.th.UpdatedAt, View(m.th).LastTurn)
	}

	p := &mgr{t: t, at: time.Unix(10_000, 0).UTC()}
	p.must(api.ThreadCreate{Workspace: "/repo", Title: "Plain", Model: api.ModelRef{Provider: "claude", Model: "sonnet"}})
	before = p.th.UpdatedAt
	evs, _ := p.observe(Reported{Report: api.ChildReported{Child: api.NewID(), Run: api.NewID(), Kind: api.ReportDone, Title: "worker"}})
	for _, e := range evs {
		if a, ok := e.Body.(api.TurnAdmitted); ok && a.Quiet {
			t.Fatal("a plain thread's report wake was admitted quiet")
		}
	}
	if p.th.Live() == nil || !p.th.UpdatedAt.After(before) || View(p.th).Turn.Silent {
		t.Fatalf("a plain thread's report wake: run %+v, UpdatedAt %v; want it the user's", p.th.Live(), p.th.UpdatedAt)
	}
}

func TestAManagerRaisesItsWake(t *testing.T) {
	m := newManager(t, &api.SweepSchedule{IntervalSeconds: 60})
	run := m.sweep()
	evs := m.must(api.WakeRaise{Reason: "  a worker needs a key  "})
	if len(evs) != 1 || evs[0].Body != (api.WakeRaised{Run: run, Reason: "a worker needs a key"}) {
		t.Fatalf("WakeRaise emitted %v, want one WakeRaised with the trimmed reason", evs)
	}
	if m.quiet() || !m.th.UpdatedAt.Equal(m.at) {
		t.Fatalf("a raised wake: turn %+v, UpdatedAt %v; want the user's, %v", View(m.th).Turn, m.th.UpdatedAt, m.at)
	}
	if evs := m.must(api.WakeRaise{Run: run}); len(evs) != 0 {
		t.Fatalf("raising a raised wake emitted %v, want nothing", evs)
	}
	m.end()
	if v := View(m.th); v.LastTurn == nil || v.LastTurn.Run != run || !m.th.UpdatedAt.Equal(m.at) {
		t.Fatalf("a raised wake's end: LastTurn %+v, UpdatedAt %v", v.LastTurn, m.th.UpdatedAt)
	}

	u := newManager(t, nil)
	u.must(api.MessageDispatch{Text: "go", Mode: api.StartNow})
	if evs := u.must(api.WakeRaise{}); len(evs) != 0 {
		t.Fatalf("raising the user's own turn emitted %v, want nothing", evs)
	}
}

func TestAWakeRaiseIsRefused(t *testing.T) {
	model := api.ModelRef{Provider: "claude", Model: "sonnet"}
	refused := func(t *testing.T, m *mgr, b api.WakeRaise) {
		t.Helper()
		if evs, err := m.submit(b); !errors.As(err, new(*api.RejectedError)) || len(evs) != 0 {
			t.Fatalf("WakeRaise = %v, %v; want RejectedError", evs, err)
		}
	}
	t.Run("plain", func(t *testing.T) {
		m := &mgr{t: t, at: time.Unix(10_000, 0).UTC()}
		m.must(api.ThreadCreate{Workspace: "/repo", Title: "Plain", Model: model, Text: "work"})
		refused(t, m, api.WakeRaise{})
	})
	t.Run("child", func(t *testing.T) {
		m := &mgr{t: t, at: time.Unix(10_000, 0).UTC()}
		m.must(api.ThreadCreate{Workspace: "/repo", Title: "Worker", Model: model, Parent: api.NewID(), Run: api.NewID(), Text: "work"})
		m.th.Role = api.RoleManager
		refused(t, m, api.WakeRaise{})
	})
	t.Run("idle", func(t *testing.T) {
		refused(t, newManager(t, nil), api.WakeRaise{})
	})
	t.Run("another run", func(t *testing.T) {
		m := newManager(t, &api.SweepSchedule{IntervalSeconds: 60})
		m.sweep()
		refused(t, m, api.WakeRaise{Run: api.NewID()})
	})
}

// What needs the user raises a quiet wake: a request of its own, the
// user's message, whatever its mode, and a failure. Ending as it should
// does not.
func TestAQuietWakeIsRaisedByWhatNeedsTheUser(t *testing.T) {
	raised := func(t *testing.T, m *mgr, evs []Event) {
		t.Helper()
		if !emitted[api.WakeRaised](evs) {
			t.Fatalf("emitted %v, want WakeRaised", evs)
		}
	}
	for _, kind := range []string{api.KindApproval, api.KindQuestion, api.KindCredentials} {
		t.Run("a "+kind, func(t *testing.T) {
			m := newManager(t, &api.SweepSchedule{IntervalSeconds: 60})
			run := m.sweep()
			evs, _ := m.observe(Surfaced{Run: run, Request: api.NewID(), Dedup: kind, Kind: kind, Body: []byte(`{}`)})
			raised(t, m, evs)
			if m.quiet() || !m.th.UpdatedAt.Equal(m.at) {
				t.Fatalf("after a %s: turn %+v, UpdatedAt %v", kind, View(m.th).Turn, m.th.UpdatedAt)
			}
		})
	}
	for _, mode := range []api.DispatchMode{api.StartNow, api.AfterActive, api.SteerActive, api.Defer, api.RestartActive} {
		t.Run(fmt.Sprint("the user's message, mode ", mode), func(t *testing.T) {
			m := newManager(t, nil)
			m.report(api.ReportDone)
			raised(t, m, m.must(api.MessageDispatch{Text: "and this", Mode: mode}))
		})
	}
	t.Run("a failure", func(t *testing.T) {
		m := newManager(t, &api.SweepSchedule{IntervalSeconds: 60})
		run := m.sweep()
		evs, _ := m.observe(Ended{Run: run, Outcome: api.OutcomeFailed, Error: "boom"})
		raised(t, m, evs)
		if i := slices.IndexFunc(evs, func(e Event) bool { _, ok := e.Body.(api.TurnEnded); return ok }); i < 0 || !emitted[api.WakeRaised](evs[:i]) {
			t.Fatalf("emitted %v, want WakeRaised before TurnEnded", evs)
		}
		if v := View(m.th); v.LastTurn == nil || v.LastTurn.Run != run || v.LastTurn.Outcome != api.OutcomeFailed || !m.th.UpdatedAt.Equal(m.at) {
			t.Fatalf("a failed wake: LastTurn %+v, UpdatedAt %v", v.LastTurn, m.th.UpdatedAt)
		}
	})
	t.Run("a completion", func(t *testing.T) {
		m := newManager(t, &api.SweepSchedule{IntervalSeconds: 60})
		run := m.sweep()
		if evs, _ := m.observe(Ended{Run: run, Outcome: api.OutcomeCompleted}); emitted[api.WakeRaised](evs) {
			t.Fatalf("a completed wake was raised: %v", evs)
		}
	})
}

// A log an older engine wrote, whose wakes were admitted loud and then
// dismissed, replays as it did.
func TestALegacyDismissedWakeReplays(t *testing.T) {
	m := newManager(t, &api.SweepSchedule{IntervalSeconds: 60})
	before, run := m.th.UpdatedAt, api.NewID()
	at := before.Add(time.Minute)
	for _, b := range []api.EventBody{
		api.MessageQueued{Message: run, Text: "sweep", Mode: api.StartNow, Auto: true},
		api.TurnAdmitted{Run: run, Generation: 1, Wake: api.WakeSweep, Prior: before},
	} {
		m.th = Apply(m.th, Event{At: at, Body: b})
	}
	if m.quiet() || !m.th.UpdatedAt.Equal(at) {
		t.Fatalf("a legacy wake: turn %+v, UpdatedAt %v; want it loud", View(m.th).Turn, m.th.UpdatedAt)
	}
	evs := m.must(api.WakeDismiss{Run: run})
	if len(evs) != 1 || !emitted[api.WakeDismissed](evs) || !m.quiet() || !m.th.UpdatedAt.Equal(before) {
		t.Fatalf("dismissing a legacy wake: %v, turn %+v, UpdatedAt %v", evs, View(m.th).Turn, m.th.UpdatedAt)
	}
	m.th = Apply(m.th, Event{At: at.Add(time.Minute), Body: api.WakeRaised{Run: run}})
	if m.quiet() || m.th.Live().Wake != api.NotWake {
		t.Fatalf("raising a legacy dismissed wake left %+v", m.th.Live())
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

func TestAQuietWakeKeepsWorkingAndAsksTheUser(t *testing.T) {
	m := newManager(t, &api.SweepSchedule{IntervalSeconds: 60})
	before := m.th.UpdatedAt
	run := m.sweep()
	worker := Thread{ID: api.NewID(), Parent: m.th.ID}
	if err := CheckCaller([]Thread{worker, m.th}, api.Caller{Thread: m.th.ID, Run: run}, false); err != nil {
		t.Fatalf("CheckCaller in a quiet wake: %v", err)
	}
	evs, effs := m.observe(Surfaced{Run: run, Request: api.NewID(), Dedup: "edit", Kind: api.KindApproval, Body: []byte(`{"tool":"Edit"}`)})
	if emitted[api.TurnStopping](evs) || len(effs) != 0 {
		t.Fatalf("a quiet wake's request stopped it: %v, %v", evs, effs)
	}
	v := View(m.th)
	if len(v.Requests) != 1 || v.Turn == nil || v.Turn.Silent || !m.th.UpdatedAt.After(before) {
		t.Fatalf("the request shows nothing: requests %v, turn %+v, UpdatedAt %v", v.Requests, v.Turn, m.th.UpdatedAt)
	}
	if _, err := m.submit(api.WakeDismiss{Run: run}); !errors.As(err, new(*api.RejectedError)) {
		t.Fatalf("dismissing a wake the user was asked about = %v, want RejectedError", err)
	}
}

func TestARestartDuringAQuietWakeLeavesItUncertain(t *testing.T) {
	m := newManager(t, &api.SweepSchedule{IntervalSeconds: 60})
	run := m.sweep()
	evs, _ := m.observe(Reconciled{Run: run})
	if !emitted[api.TurnUncertain](evs) || emitted[api.TurnEnded](evs) {
		t.Fatalf("a restart during a quiet wake: %v, want it uncertain", evs)
	}
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

// A quiet sweep a usage limit ended is raised, as any failure: the user's
// turn now, which the next sweep waits behind until its continue is due.
func TestALimitAQuietSweepHitRaisesItAndHoldsTheNextSweep(t *testing.T) {
	m := newManager(t, &api.SweepSchedule{IntervalSeconds: 60})
	run := m.sweep()
	resets := m.at.Add(time.Hour)
	evs, _ := m.observe(Ended{Run: run, Outcome: api.OutcomeFailed, Limit: &api.UsageLimit{Resets: resets}})
	if !emitted[api.WakeRaised](evs) {
		t.Fatalf("a limit's end of a quiet sweep: %v, want it raised", evs)
	}
	if v := View(m.th); v.LastTurn == nil || v.LastTurn.Limit == nil {
		t.Fatalf("LastTurn = %+v, want the sweep the limit ended", v.LastTurn)
	}
	for _, now := range []time.Time{m.at.Add(time.Minute), resets.Add(time.Second)} {
		if _, _, err := Observe(m.th, SweepDue{Run: api.NewID()}, now); !errors.Is(err, ErrRefused) {
			t.Fatalf("a sweep at %v, before the limit's continue is overdue = %v, want ErrRefused", now, err)
		}
	}
}

// A worker's report during a quiet sweep moves nothing either; the
// Manager still knows when it came, and that it was blocked.
func TestAReportDuringAQuietSweepMovesNothing(t *testing.T) {
	m := newManager(t, &api.SweepSchedule{IntervalSeconds: 60})
	before := m.th.UpdatedAt
	m.sweep()
	m.report(api.ReportBlocked)
	blocked := m.at
	m.end()
	if !m.th.UpdatedAt.Equal(before) || !m.th.Reported.Equal(blocked) || !m.th.Alerted.Equal(blocked) {
		t.Fatalf("after the sweep UpdatedAt %v, Reported %v, Alerted %v; want %v, then the report's %v", m.th.UpdatedAt, m.th.Reported, m.th.Alerted, before, blocked)
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
		t.Run("replayed "+fmt.Sprint(d.State), func(t *testing.T) {
			m := newManager(t, nil)
			m.th = Apply(m.th, Event{At: m.at.Add(time.Second), Body: api.DispositionChanged{Disposition: d}})
			if m.th.Disposition != (api.Disposition{}) {
				t.Fatalf("replaying %v on a Manager left it %+v, want Open", d.State, m.th.Disposition)
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
			m.end()
			if !m.th.UpdatedAt.Equal(before) || View(m.th).LastTurn != nil {
				t.Fatalf("a quiet sweep left UpdatedAt %v (was %v), LastTurn %+v", m.th.UpdatedAt, before, View(m.th).LastTurn)
			}
		})
	}
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
	t.Run("stopped with held report", func(t *testing.T) {
		m := newManager(t, nil)
		m.must(api.MessageDispatch{Text: "go", Mode: api.StartNow})
		m.must(api.TurnInterrupt{})
		m.at = m.at.Add(time.Second)
		evs, _, err := Observe(m.th, Ended{Run: m.th.Live().ID, Outcome: api.OutcomeInterrupted}, m.at)
		if err != nil {
			t.Fatal(err)
		}
		m.fold(evs)
		m.at = m.at.Add(time.Second)
		evs, _, err = Observe(m.th, Reported{Report: api.ChildReported{Child: api.NewID(), Run: api.NewID(), Title: "Worker", Kind: api.ReportDone}}, m.at)
		if err != nil {
			t.Fatal(err)
		}
		m.fold(evs)
		if !View(m.th).Resumable {
			t.Fatalf("expected stopped Manager with a worker report to be resumable")
		}
		refused(t, m)
	})
	t.Run("queued deferred message", func(t *testing.T) {
		m := newManager(t, nil)
		m.must(api.MessageDispatch{Text: "later", Mode: api.Defer})
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
