package engine_test

import (
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
)

func TestReviewCancelWhileWorktreeIsBeingPrepared(t *testing.T) {
	origin := api.Origin{Engine: api.NewID(), Thread: api.NewID(), Link: api.NewID(), Access: api.AccessAsk}
	target := api.NewID()
	env := engine.Env{Ceiling: func() (api.Access, error) { return api.AccessAsk, nil }}
	var th engine.Thread
	run := func(body api.Intent, caller *api.Caller) {
		t.Helper()
		ev, _, err := engine.DecideWith(th, api.Command{ID: api.NewID(), Target: target, Body: body, Caller: caller}, env, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range ev {
			th = engine.Apply(th, e)
		}
	}
	run(api.ThreadCreate{Title: "on B", Text: "go", Project: "/repo", Worktree: &api.NewWorktree{Base: "base"}, Origin: &origin}, nil)
	run(api.TurnInterrupt{Tree: true}, &api.Caller{Thread: origin.Thread})
	ev, _, err := engine.ObserveWith(th, engine.WorktreeReady{Workspace: "/work", Branch: "child"}, env, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ev {
		if _, ok := e.Body.(api.TurnAdmitted); ok {
			t.Fatal("B acknowledged the interrupt during worktree preparation, then admitted the cancelled task")
		}
	}
}

func TestReviewUpgradeDoesNotTakeOverExistingChild(t *testing.T) {
	origin := api.Origin{Engine: api.NewID(), Link: api.NewID(), Thread: api.NewID()}
	events := []api.Event{on(t, 1, api.ThreadCreated{Origin: &origin}), on(t, 2, api.MessageQueued{Message: api.NewID(), Text: "first task", Mode: api.StartNow})}
	child, _, _, err := engine.Follow(engine.Remote{}, events, true)
	if err != nil {
		t.Fatal(err)
	}
	if child.Taken {
		t.Fatal("replaying the previous version's link create marks B's person as the owner without any action by that person")
	}
}

func TestReviewLostUnboundSettleKeepsItsFence(t *testing.T) {
	th, c := creating(t, true)
	ev, _, err := engine.Observe(th, engine.RemoteLost{Reason: "three failed tries"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ev {
		th = engine.Apply(th, e)
	}
	_, fx, err := decide(t, th, settle, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := stops(fx)
	if len(got) != 1 || got[0].Create == nil || got[0].Create.Target != c.Target {
		t.Fatal("A dropped the still-unbound create fence from the stop after recording RemoteLost")
	}
}
