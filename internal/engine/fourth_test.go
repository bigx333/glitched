package engine_test

import (
	"glitched/internal/api"
	"glitched/internal/engine"
	"testing"
	"time"
)

func TestFourthStoppedChildParity(t *testing.T) {
	for _, remote := range []bool{false, true} {
		name := "local"
		if remote {
			name = "link"
		}
		t.Run(name, func(t *testing.T) {
			now := time.Now().UTC()
			after := now.Add(54 * time.Minute)
			parent, id, msg := api.NewID(), api.NewID(), api.NewID()
			c := api.ThreadCreated{Workspace: "/w", Project: "/repo", Title: "child", Parent: parent, Model: api.ModelRef{Provider: "claude", Model: "m"}}
			if remote {
				c.Parent = api.ID{}
				c.Origin = &api.Origin{Thread: parent, Link: api.NewID(), Depth: 1}
			}
			var th engine.Thread
			env := engine.Env{Ceiling: func() (api.Access, error) { return api.AccessAsk, nil }}
			emit := func(b api.EventBody) { th = engine.Apply(th, engine.Event{Thread: id, At: now, Body: b}) }
			emit(c)
			emit(api.MessageQueued{Message: msg, Text: "work", Mode: api.StartNow})
			emit(api.TurnAdmitted{Run: msg, Generation: 1})
			emit(api.TurnStarted{Run: msg})
			emit(api.ContextReported{Run: msg, Used: 150000, Window: 1000000, Model: "m", CacheExpires: now.Add(time.Hour)})
			fold := func(e []engine.Event, _ []engine.Effect, err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
				for _, v := range e {
					th = engine.Apply(th, v)
				}
			}
			command := func(b api.Intent) {
				fold(engine.DecideWith(th, api.Command{ID: api.NewID(), Target: id, Body: b}, env, now))
			}
			observe := func(o engine.Observation) { fold(engine.ObserveWith(th, o, env, now)) }
			command(api.TurnInterrupt{})
			observe(engine.Ended{Run: msg, Outcome: api.OutcomeCompleted})
			if !th.Runs[0].Signalled {
				t.Fatal("run was not stopped")
			}
			e, _, err := engine.ObserveWith(th, engine.IdleCompact{Run: api.NewID(), AfterRun: msg}, env, after)
			if err == nil {
				t.Errorf("stopped %s child admitted silent compaction: %d events", name, len(e))
			}
			observe(engine.Reported{Report: api.ChildReported{Child: api.NewID(), Run: api.NewID(), Kind: api.ReportDone}, Reply: "done"})
			if v := engine.View(th); v.Live() || v.Queued != 1 || !v.Resumable {
				t.Fatalf("held wake: %+v", v)
			}
			command(api.MessageDispatch{Text: "later", Mode: api.Defer})
			if v := engine.View(th); v.Live() || v.Queued != 2 || !v.Resumable {
				t.Fatalf("defer: %+v", v)
			}
			command(api.QueueResume{})
			if !engine.View(th).Live() {
				t.Fatal("resume failed to admit")
			}
		})
	}
}

func TestFourthStopDuringMaintenance(t *testing.T) {
	base := time.Now().UTC()
	expires := base.Add(time.Hour)
	now := expires.Add(-6 * time.Minute)
	th, last := makeIdleThread("claude", "claude-test", "", 150000, base, expires)
	th.Origin = &engine.Origin{Origin: api.Origin{Thread: api.NewID(), Access: api.AccessAsk, Link: api.NewID()}}
	env := engine.Env{Ceiling: func() (api.Access, error) { return api.AccessAsk, nil }}
	fold := func(es []engine.Event, _ []engine.Effect, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range es {
			th = engine.Apply(th, e)
		}
	}
	if _, _, err := engine.ObserveWith(th, engine.IdleCompact{Run: api.NewID(), AfterRun: last}, env, now); err == nil {
		t.Fatal("a link's thread was compacted on its own, so a stop could land on a silent run")
	}
	fold(engine.DecideWith(th, api.Command{ID: api.NewID(), Target: th.ID, Body: api.TurnInterrupt{}, Caller: &api.Caller{Thread: th.Origin.Thread}}, env, now))
	fold(engine.ObserveWith(th, engine.Reported{Report: api.ChildReported{Child: api.NewID(), Run: api.NewID(), Kind: api.ReportDone}, Reply: "done"}, env, now))
	if live := th.Live(); live != nil && !live.Silent {
		t.Fatal("link stop while compaction ran: next child report started a non-silent turn without resume or message")
	}
}
