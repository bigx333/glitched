package engine_test

import (
	"glitched/internal/api"
	"glitched/internal/engine"
	"testing"
	"time"
)

func TestR3QuietSurvivesMaintenance(t *testing.T) {
	base := time.Now().UTC()
	expires := base.Add(time.Hour)
	th, last := makeIdleThread("claude", "claude-test", "", 150000, base, expires)
	th.Origin = &engine.Origin{Origin: api.Origin{Thread: api.NewID(), Access: api.AccessAsk, Link: api.NewID()}}
	env := engine.Env{Ceiling: func() (api.Access, error) { return api.AccessAsk, nil }}
	fold := func(es []engine.Event, fx []engine.Effect, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range es {
			th = engine.Apply(th, e)
		}
	}
	fold(engine.DecideWith(th, api.Command{ID: api.NewID(), Target: th.ID, Body: api.TurnInterrupt{}, Caller: &api.Caller{Thread: th.Origin.Thread}}, env, base))
	if v := engine.View(th); v.Run != api.RunIdle {
		t.Fatalf("an idle stop started something: %v", v.Run)
	}
	when := expires.Add(-6 * time.Minute)
	silent := api.NewID()
	fold(engine.ObserveWith(th, engine.IdleCompact{Run: silent, AfterRun: last}, env, when))
	fold(engine.ObserveWith(th, engine.Started{Run: silent}, env, when))
	fold(engine.ObserveWith(th, engine.Ended{Run: silent, Outcome: api.OutcomeCompleted}, env, when))
	fold(engine.ObserveWith(th, engine.Reported{Report: api.ChildReported{Child: api.NewID(), Run: api.NewID(), Kind: api.ReportDone}, Reply: "done"}, env, when))
	if live := th.Live(); live != nil && !live.Silent {
		t.Fatal("idle stop, silent maintenance, then child report: ordinary turn admitted without a user message or resume")
	}
}
