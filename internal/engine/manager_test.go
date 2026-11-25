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

func TestSweepDueAndWakeDismiss(t *testing.T) {
	now := time.Unix(2000, 0).UTC()
	mgrID := api.NewID()
	evs, _, err := Decide(Thread{}, api.Command{
		ID:     api.NewID(),
		Target: mgrID,
		Body: api.ThreadCreate{
			Workspace: "/repo",
			Title:     "Sweeper",
			Model:     api.ModelRef{Provider: "claude", Model: "sonnet"},
			Role:      api.RoleManager,
			Sweep:     &api.SweepSchedule{IntervalSeconds: 60},
		},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	var th Thread
	for _, e := range evs {
		th = Apply(th, e)
	}
	th.Reported = now.Add(5 * time.Second)

	// Too early -> ErrRefused.
	if _, _, err := Observe(th, SweepDue{Run: api.NewID()}, now.Add(30*time.Second)); !errors.Is(err, ErrRefused) {
		t.Fatalf("early sweep err = %v, want ErrRefused", err)
	}

	// Due at +61s -> starts run and sets LastSweep.
	sweepAt := now.Add(61 * time.Second)
	sweepRun := api.NewID()
	sEvs, sEffs, err := Observe(th, SweepDue{Run: sweepRun}, sweepAt)
	if err != nil {
		t.Fatalf("due sweep: %v", err)
	}
	if len(sEffs) != 1 {
		t.Fatalf("sweep effects = %d, want 1 StartTurn", len(sEffs))
	}
	for _, e := range sEvs {
		th = Apply(th, e)
	}
	if !th.LastSweep.Equal(sweepAt) {
		t.Fatalf("LastSweep = %v, want %v", th.LastSweep, sweepAt)
	}
	prevUpdated := th.UpdatedAt

	// Settled/busy threads refuse SweepDue.
	if _, _, err := Observe(th, SweepDue{Run: api.NewID()}, sweepAt.Add(120*time.Second)); !errors.Is(err, ErrRefused) {
		t.Fatalf("busy sweep err = %v, want ErrRefused", err)
	}

	// Dismissing the wake flips live run to Silent, clears Reported, and does not bump UpdatedAt.
	dismissAt := sweepAt.Add(2 * time.Second)
	dEvs, _, err := Decide(th, api.Command{
		ID:     api.NewID(),
		Target: mgrID,
		Body:   api.WakeDismiss{Run: sweepRun, Reason: "all clear"},
	}, dismissAt)
	if err != nil {
		t.Fatalf("WakeDismiss: %v", err)
	}
	for _, e := range dEvs {
		th = Apply(th, e)
	}
	if !th.UpdatedAt.Equal(prevUpdated) {
		t.Fatalf("UpdatedAt bumped by WakeDismiss: got %v want %v", th.UpdatedAt, prevUpdated)
	}
	if !th.Reported.IsZero() {
		t.Fatalf("Reported not cleared: %v", th.Reported)
	}
	v := View(th)
	if v.Turn == nil || !v.Turn.Silent || v.Working() {
		t.Fatalf("expected silent non-working turn view, got %+v", v.Turn)
	}

	// TurnEnded after WakeDismiss leaves LastTurn intact as nil and does not bump UpdatedAt.
	eEvs, _, err := Observe(th, Ended{Run: sweepRun, Outcome: api.OutcomeCompleted}, dismissAt.Add(time.Second))
	if err != nil {
		t.Fatalf("Ended after dismiss: %v", err)
	}
	for _, e := range eEvs {
		th = Apply(th, e)
	}
	if !th.UpdatedAt.Equal(prevUpdated) {
		t.Fatalf("UpdatedAt bumped on silent run end: got %v want %v", th.UpdatedAt, prevUpdated)
	}
	if View(th).LastTurn != nil {
		t.Fatalf("expected nil LastTurn after silent run end, got %+v", View(th).LastTurn)
	}
}
