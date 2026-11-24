package runtime_test

import (
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
	"glitched/internal/store"
)

func lostBeforeARestore(t *testing.T, auto bool) (*store.Store, string, api.ID) {
	t.Helper()
	path := dbPath(t)
	ad := held()
	n := start(t, path, ad)
	id := newThread(t, n.h)
	run := submit(t, n.h, id, dispatch("work before the restore", api.StartNow)).Command
	startsReach(t, ad, 1)
	n.kill()
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	leases, err := st.Leased(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range leases {
		if b, ok := e.Body.(engine.StartTurn); e.Thread == id && ok && b.Run == run {
			_, err := st.Observe(ctx, id, engine.Reconciled{Run: run, Continue: auto}, time.Now().Add(-time.Hour), e.ID)
			if err != nil {
				t.Fatal(err)
			}
			return st, path, id
		}
	}
	t.Fatal("original start was not leased")
	return nil, "", api.ID{}
}

// A restore says again that a run already uncertain is, which leaves the
// thread where it was among the user's recent ones.
func TestARestoreLeavesAnUncertainThreadWhereItWas(t *testing.T) {
	st, _, lost := lostBeforeARestore(t, false)
	recent := api.NewID()
	_, err := st.Submit(ctx, api.Command{ID: api.NewID(), Target: recent, Body: api.ThreadCreate{Workspace: "/src/app", Title: "recent work"}}, time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	before, err := st.Read(ctx, api.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Threads) != 2 || before.Threads[0].ID != recent || before.Threads[1].ID != lost || before.Threads[1].Run != api.RunUncertain {
		t.Fatalf("bad fixture: got %+v", before.Threads)
	}
	if err := st.Rewind(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := st.Read(ctx, api.Query{})
	if err != nil {
		t.Fatal(err)
	}
	old, err := st.Read(ctx, api.Query{Thread: lost})
	if err != nil {
		t.Fatal(err)
	}
	if after.Threads[0].ID != recent || !old.Threads[0].UpdatedAt.Equal(before.Threads[1].UpdatedAt) {
		t.Fatal("restore should not replace the activity time of an unchanged thread with no pending continuation")
	}
}
