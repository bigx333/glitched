package runtime_test

import (
	"context"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
)

func TestReviewMissingProviderCloseSurvivesRestart(t *testing.T) {
	path := dbPath(t)
	b := &unreachable{
		Adapter: fake.New(fake.Turn(true)),
		tried:   make(chan struct{}, 1),
	}
	n := serve(t, path, provider.Mux{"b": b})
	thread := threadOn(t, n.h, "b")
	run := submit(t, n.h, thread, dispatch("go", api.StartNow)).Command
	until(t, n.h, thread, "approval", waiting)

	b.fail.Store(true)
	submit(t, n.h, thread, api.ThreadDelete{})
	select {
	case <-b.tried:
	case <-time.After(5 * time.Second):
		t.Fatal("close not attempted")
	}
	n.kill()
	b.fail.Store(false)

	n = serve(t, path, provider.Mux{})
	db := sideDB(t, path)
	deadline := time.Now().Add(5 * time.Second)
	var state string
	for {
		err := db.QueryRow(
			"SELECT state FROM effects WHERE kind = 'agent.close'",
		).Scan(&state)
		if err != nil {
			t.Fatal(err)
		}
		if state == "done" || time.Now().After(deadline) {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	n.kill()

	serve(t, path, provider.Mux{"b": b})
	deadline = time.Now().Add(5 * time.Second)
	for {
		rec, err := b.Reconcile(context.Background(), provider.RuntimeIdentity{
			Thread: thread, Run: run, Provider: "b",
		})
		if _, lost := rec.(provider.Lost); lost && err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf(
				"agent.close=%s; after re-registering b, deleted run recovery=%T, err=%v; want closed agent",
				state, rec, err,
			)
		}
		time.Sleep(2 * time.Millisecond)
	}
}
