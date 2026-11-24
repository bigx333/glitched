package runtime_test

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
)

// failsReconcileOnce fails its first reconcile of run.
type failsReconcileOnce struct {
	*fake.Adapter
	run       api.ID
	attempted atomic.Bool
}

func (a *failsReconcileOnce) Reconcile(ctx context.Context, id provider.RuntimeIdentity) (provider.Recovery, error) {
	if id.Run == a.run && !a.attempted.Swap(true) {
		return nil, os.ErrPermission
	}
	return a.Adapter.Reconcile(ctx, id)
}

func TestOnlyAStopSomeoneAskedForSilencesALostChild(t *testing.T) {
	for _, tc := range []struct {
		name           string
		deliberateStop bool
		failReconcile  bool
		wantReports    int
	}{
		{"acknowledged-user-stop", true, false, 1},
		{"acknowledged-user-stop-reconcile-error", true, true, 1},
		{"restart-active-is-not-user-stop", false, false, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := dbPath(t)
			old := deaf{fake.New(fake.Turn(true))}
			n := start(t, path, old)
			parent := newThread(t, n.h)
			pr := submit(t, n.h, parent, dispatch("parent", api.StartNow)).Command
			until(t, n.h, parent, "parent waiting", waiting)
			child := api.NewID()
			cr := submit(t, n.h, child, api.ThreadCreate{
				Parent: parent, Run: pr, Text: "child task",
				Title: "child", Workspace: "/src/app", Model: api.ModelRef{Provider: "fake", Model: "fake"},
			}).Command
			until(t, n.h, child, "child waiting", waiting)
			until(t, n.h, parent, "original child approval report", func(api.Thread) bool {
				return count(events(t, n.h, parent), "child.reported") == 1
			})
			if tc.deliberateStop {
				submit(t, n.h, child, api.TurnInterrupt{})
			} else {
				submit(t, n.h, child, dispatch("Do this instead", api.RestartActive))
			}
			until(t, n.h, child, "provider acknowledged the interrupt", func(th api.Thread) bool {
				return th.Run == api.RunStopping && count(events(t, n.h, child), "turn.signalled") == 1
			})
			stopAsked := count(events(t, n.h, child), "stop.asked")
			if (stopAsked != 0) != tc.deliberateStop {
				t.Fatalf("bad fixture: explicit StopAsked events=%d", stopAsked)
			}
			n.kill()

			next := fake.New(fake.Turn(true))
			var ad provider.Adapter = next
			if tc.failReconcile {
				ad = &failsReconcileOnce{Adapter: next, run: cr}
			}
			n = autoHost(t, path, provider.Mux{"fake": ad})
			<-n.h.LostContinued()
			db := sideDB(t, path)
			for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
				var open int
				if err := db.QueryRow("SELECT count(*) FROM effects WHERE (kind = 'child.report' OR kind = 'agent.close') AND state <> 'done'").Scan(&open); err != nil {
					t.Fatal(err)
				}
				if open == 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("pending close and child report effects did not drain")
				}
			}
			var reports []string
			for _, e := range events(t, n.h, parent) {
				b, err := e.Decode()
				if err != nil {
					t.Fatal(err)
				}
				if q, ok := b.(api.ChildReported); ok && q.Run == cr && q.Kind == api.ReportBlocked {
					reports = append(reports, q.Outcome)
				}
			}
			reason := lostFor(t, n.h, child)
			t.Logf("StopAsked=%d, uncertainty=%d, reports=%q", stopAsked, reason, reports)
			if len(reports) != tc.wantReports {
				t.Fatalf("got %d blocked reports, want %d", len(reports), tc.wantReports)
			}
		})
	}
}

func TestAChainOfContinuesFromBeforeAnUpgradeStillCounts(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy=%v", legacy), func(t *testing.T) {
			path := dbPath(t)
			original := held()
			n := start(t, path, original)
			id := newThread(t, n.h)
			submit(t, n.h, id, dispatch("initial work", api.StartNow))
			startsReach(t, original, 1)
			n.kill()

			for range 3 {
				ad := held()
				n = start(t, path, ad)
				th := until(t, n.h, id, "previous lost run", func(th api.Thread) bool { return th.Run == api.RunUncertain })
				submit(t, n.h, id, api.MessageDispatch{Text: api.ContinueText, Mode: api.RestartActive, Replaces: th.Turn.Run})
				startsReach(t, ad, 1)
				n.kill()
			}
			if legacy {
				db := sideDB(t, path)
				exec(t, db, "UPDATE events SET payload = CAST(json_remove(CAST(payload AS TEXT), '$.replaces', '$.auto') AS BLOB) WHERE type = 'message.queued'")
			}
			next := fake.New(fake.Turn(true))
			n = autoHost(t, path, provider.Mux{"fake": next})
			<-n.h.LostContinued()
			th := until(t, n.h, id, "read", func(api.Thread) bool { return true })
			var requests int
			for _, e := range events(t, n.h, id) {
				if b, err := e.Decode(); err != nil {
					t.Fatal(err)
				} else if q, ok := b.(api.MessageQueued); ok && q.Text == api.ContinueText && q.Mode == api.RestartActive {
					requests++
				}
			}
			t.Logf("%d Continue messages, %d total admissions, run state=%v", requests, count(events(t, n.h, id), "turn.admitted"), th.Run)
			if th.Run != api.RunUncertain || requests != 3 || next.Starts() != 0 {
				t.Fatal("three Continue requests in the last five minutes should block a fourth")
			}
		})
	}
}
