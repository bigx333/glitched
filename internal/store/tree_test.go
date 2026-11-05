package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
	"glitched/internal/store"
)

var (
	settle = api.InboxSet{Disposition: api.Disposition{State: api.Settled}}
	del    = api.ThreadDelete{}
)

// working is a thread of the user's with a live run, which is the
// command's ID.
func working(t *testing.T, s *store.Store) (thread, run api.ID) {
	t.Helper()
	thread, run = api.NewID(), api.NewID()
	submitTo(t, s, create(thread))
	submitTo(t, s, api.Command{ID: run, Target: thread, Body: api.MessageDispatch{Text: "go", Mode: api.StartNow}})
	return thread, run
}

// spawn has parent's live run start a child with a task, and returns the
// child and its run, the create's ID.
func spawn(s *store.Store, parent, run api.ID) (child, childRun api.ID, err error) {
	child, childRun = api.NewID(), api.NewID()
	_, err = s.Submit(context.Background(), api.Command{ID: childRun, Target: child, Body: api.ThreadCreate{
		Workspace: "w", Title: "child", Model: api.ModelRef{Provider: "fake"}, Parent: parent, Run: run, Text: "work",
	}}, time.Now())
	return child, childRun, err
}

func submitTo(t *testing.T, s *store.Store, c api.Command) {
	t.Helper()
	if _, err := s.Submit(context.Background(), c, time.Now()); err != nil {
		t.Fatalf("%T: %v", c.Body, err)
	}
}

// tree is a thread of the user's at work, its child at work, and that
// child's child at work, each started by the run above it.
type tree struct {
	ids, runs [3]api.ID
}

func newTree(t *testing.T, s *store.Store) tree {
	t.Helper()
	var tr tree
	tr.ids[0], tr.runs[0] = working(t, s)
	for i := 1; i < 3; i++ {
		var err error
		if tr.ids[i], tr.runs[i], err = spawn(s, tr.ids[i-1], tr.runs[i-1]); err != nil {
			t.Fatal(err)
		}
	}
	return tr
}

// effects leases every pending effect and lists each run's kinds, by
// thread and run.
func effects(t *testing.T, s *store.Store) map[[2]api.ID][]string {
	t.Helper()
	out := map[[2]api.ID][]string{}
	for {
		e, ok, err := s.Lease(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			return out
		}
		var run api.ID
		switch b := e.Body.(type) {
		case engine.StartTurn:
			run = b.Run
		case engine.InterruptTurn:
			run = b.Run
		case engine.CloseAgent:
			run = b.Run
		}
		k := [2]api.ID{e.Thread, run}
		out[k] = append(out[k], engine.EffectKind(e.Body))
	}
}

// Settling or deleting a thread takes every thread under it in the same
// commit, a child its run started a moment before and its own child too:
// each is settled with its run stopping and an interrupt in its own lane,
// or deleted with its run ended and its agent's close in its own lane.
// The commit's views show each, so a client's shell does.
func TestSettleAndDeleteTakeTheTree(t *testing.T) {
	for _, tc := range []struct {
		name   string
		body   api.Intent
		effect string
	}{{"settle", settle, "turn.interrupt"}, {"delete", del, "agent.close"}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s := open(t, filepath.Join(t.TempDir(), "host.db"))
			tr := newTree(t, s)
			effects(t, s)
			var seen []store.Commit
			s.OnCommit(func(c store.Commit) { seen = append(seen, c) })
			submitTo(t, s, api.Command{ID: api.NewID(), Target: tr.ids[0], Body: tc.body})
			if len(seen) != 1 || len(seen[0].Threads) != 3 {
				t.Fatalf("commits told of: %+v; want one, with the 3 threads' views", seen)
			}
			out := effects(t, s)
			for i, id := range tr.ids {
				snap, err := s.Read(ctx, api.Query{Thread: id})
				switch {
				case tc.name == "delete" && !errors.Is(err, api.ErrNotFound):
					t.Errorf("thread %d after the delete: %v, want not found", i, err)
				case tc.name == "settle" && err != nil:
					t.Fatal(err)
				case tc.name == "settle":
					if v := snap.Threads[0]; v.Disposition.State != api.Settled || v.Run != api.RunStopping {
						t.Errorf("thread %d: %v, run %v; want settled, its run stopping", i, v.Disposition.State, v.Run)
					}
				}
				if k := out[[2]api.ID{id, tr.runs[i]}]; !slices.Contains(k, tc.effect) {
					t.Errorf("thread %d's lane holds %v, want its run's %s", i, k, tc.effect)
				}
			}
			for i, v := range seen[0].Threads {
				if settled := v.Disposition.State == api.Settled; tc.name == "settle" && !settled {
					t.Errorf("the commit's view %d is %v, want settled", i, v.Disposition.State)
				}
			}
		})
	}
}

// A child its parent's run starts as the parent is settled or deleted is
// either refused or taken with the tree: never left at work.
func TestAChildRacingItsParentsSettleOrDeleteIsTaken(t *testing.T) {
	for _, tc := range []struct {
		name string
		body api.Intent
	}{{"settle", settle}, {"delete", del}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s := open(t, filepath.Join(t.TempDir(), "host.db"))
			taken, refused := 0, 0
			for range 40 {
				top, run := working(t, s)
				var child api.ID
				var spawnErr, err error
				var wg sync.WaitGroup
				wg.Go(func() { child, _, spawnErr = spawn(s, top, run) })
				wg.Go(func() { _, err = s.Submit(ctx, api.Command{ID: api.NewID(), Target: top, Body: tc.body}, time.Now()) })
				wg.Wait()
				if err != nil {
					t.Fatal(err)
				}
				if spawnErr != nil {
					refused++
					continue
				}
				taken++
				snap, err := s.Read(ctx, api.Query{Thread: child})
				switch {
				case tc.name == "delete" && !errors.Is(err, api.ErrNotFound):
					t.Fatalf("a child started before the delete is still there: %v", err)
				case tc.name == "settle" && err != nil:
					t.Fatal(err)
				case tc.name == "settle" && (snap.Threads[0].Disposition.State != api.Settled || snap.Threads[0].Run != api.RunStopping):
					t.Fatalf("a child started before the settle: %v, run %v; want settled and stopping", snap.Threads[0].Disposition.State, snap.Threads[0].Run)
				}
			}
			t.Logf("children taken with the tree %d, refused %d", taken, refused)
		})
	}
}

// A commit that fails part way through a tree, as a crash would leave it,
// leaves none of the tree changed and tells no client; the same command
// again takes all of it.
func TestATreeCommitIsAllOrNothing(t *testing.T) {
	for _, tc := range []struct {
		name   string
		body   api.Intent
		column string
	}{{"settle", settle, "disposition"}, {"delete", del, "deleted"}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "host.db")
			s := open(t, path)
			tr := newTree(t, s)
			before := effects(t, s)
			db, err := sql.Open("sqlite", path+"?_busy_timeout=5000")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			// The child is written after the grandchild and before the top.
			trigger := fmt.Sprintf(`CREATE TRIGGER crash BEFORE UPDATE OF %s ON threads WHEN NEW.id = X'%x'
				BEGIN SELECT RAISE(ABORT, 'crash'); END`, tc.column, tr.ids[1][:])
			if _, err := db.Exec(trigger); err != nil {
				t.Fatal(err)
			}
			commits := 0
			s.OnCommit(func(store.Commit) { commits++ })
			c := api.Command{ID: api.NewID(), Target: tr.ids[0], Body: tc.body}
			if _, err := s.Submit(ctx, c, time.Now()); err == nil {
				t.Fatal("the commit went through the crash")
			}
			if commits != 0 {
				t.Errorf("%d commits told of", commits)
			}
			for i, id := range tr.ids {
				snap, err := s.Read(ctx, api.Query{Thread: id})
				if err != nil {
					t.Fatalf("thread %d after the failed %s: %v", i, tc.name, err)
				}
				if v := snap.Threads[0]; v.Disposition.State != api.Open || v.Run == api.RunStopping {
					t.Errorf("thread %d after the failed %s: %v, run %v; want open and at work", i, tc.name, v.Disposition.State, v.Run)
				}
			}
			if out := effects(t, s); len(out) != 0 {
				t.Errorf("effects after the failed %s: %v; had %v before", tc.name, out, before)
			}
			if _, err := db.Exec(`DROP TRIGGER crash`); err != nil {
				t.Fatal(err)
			}
			submitTo(t, s, c)
			for i, id := range tr.ids {
				snap, err := s.Read(ctx, api.Query{Thread: id})
				if tc.name == "delete" && !errors.Is(err, api.ErrNotFound) || tc.name == "settle" && (err != nil || snap.Threads[0].Disposition.State != api.Settled) {
					t.Errorf("thread %d after the %s again: %+v, %v", i, tc.name, snap.Threads, err)
				}
			}
		})
	}
}
