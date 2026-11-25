package store_test

import (
	"context"
	"glitched/internal/api"
	"glitched/internal/engine"
	"glitched/internal/store"
	"path/filepath"
	"testing"
	"time"
)

func TestFourthStopSettleAcrossReopen(t *testing.T) {
	for _, placement := range []string{"local", "linked"} {
		for _, action := range []string{"stop", "settle"} {
			t.Run(placement+"/"+action, func(t *testing.T) {
				ctx := context.Background()
				path := filepath.Join(t.TempDir(), "db")
				tab := &links{}
				l := api.Link{ID: api.NewID(), Access: api.AccessAsk}
				tab.set(l)
				re := func() *store.Store { v := open(t, path); v.SetLinks(tab.find); return v }
				s := re()
				send := func(c api.Command) {
					t.Helper()
					if _, err := s.Submit(ctx, c, time.Now()); err != nil {
						t.Fatal(err)
					}
				}
				var id, run api.ID
				if placement == "linked" {
					c := linkCreate(l.ID, api.AccessAsk, 1)
					send(c)
					id, run = c.Target, c.ID
					if _, err := s.Observe(ctx, id, engine.WorktreeReady{Workspace: "/wt", Branch: "tmp"}, time.Now(), leaseMake(t, s, id)); err != nil {
						t.Fatal(err)
					}
				} else {
					parent := api.NewID()
					pc := create(parent)
					pb := pc.Body.(api.ThreadCreate)
					pb.Text = "start"
					pc.Body = pb
					send(pc)
					cc := create(api.NewID())
					cb := cc.Body.(api.ThreadCreate)
					cb.Parent, cb.Run, cb.Text = parent, pc.ID, "start child"
					cc.Body = cb
					send(cc)
					id, run = cc.Target, cc.ID
				}
				cmd := func(b api.Intent) { send(api.Command{ID: api.NewID(), Target: id, Body: b}) }
				obs := func(o engine.Observation) {
					t.Helper()
					if _, err := s.Observe(ctx, id, o, time.Now(), 0); err != nil {
						t.Fatal(err)
					}
				}
				obs(engine.Started{Run: run})
				wake := func() {
					obs(engine.Reported{Report: api.ChildReported{Child: api.NewID(), Run: api.NewID(), Kind: api.ReportDone}, Reply: "complete"})
				}
				wake()
				cmd(api.MessageDispatch{Text: "held", Mode: api.Defer})
				if action == "stop" {
					cmd(api.TurnInterrupt{})
				} else {
					cmd(api.InboxSet{Disposition: api.Disposition{State: api.Settled}})
				}
				obs(engine.Ended{Run: run, Outcome: api.OutcomeInterrupted})
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				s = re()
				wake()
				view := func() api.Thread { return engine.View(sameAsFold(t, s, id, "reopened child")) }
				if action == "stop" {
					v := view()
					if v.Live() || v.Queued != 3 || !v.Resumable {
						t.Fatalf("stop persisted %+v", v)
					}
					cmd(api.MessageDispatch{Text: "save for later", Mode: api.Defer})
					if v = view(); v.Live() || v.Queued != 4 || !v.Resumable {
						t.Fatalf("defer %+v", v)
					}
					cmd(api.QueueResume{})
					if !view().Live() {
						t.Fatal("resume didn't start denied wake")
					}
				} else {
					v := view()
					if v.Live() || v.Queued != 0 || v.Resumable || v.Disposition.State != api.Settled {
						t.Fatalf("settle persisted %+v", v)
					}
					cmd(api.QueueResume{})
					if view().Live() {
						t.Fatal("resume started a settled thread")
					}
				}
			})
		}
	}
}
