package store_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
	"glitched/internal/store"
)

func open(t *testing.T, path string) *store.Store {
	t.Helper()
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func create(thread api.ID) api.Command {
	return api.Command{ID: api.NewID(), Target: thread, Body: api.ThreadCreate{Workspace: "w", Title: "t", Model: api.ModelRef{Provider: "fake"}}}
}

func TestSubmitReturnsTheStoredReceipt(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "host.db")
	s := open(t, path)
	thread := api.NewID()
	c := create(thread)
	first, err := s.Submit(ctx, c, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.Submit(ctx, c, time.Now())
	if err != nil || again != first {
		t.Fatalf("resubmit: %+v %v, want %+v", again, err, first)
	}
	events, _ := s.ReadLog(ctx, 0, 0)
	if len(events) != 1 {
		t.Fatalf("resubmit wrote events: %d in log", len(events))
	}

	changed := c
	changed.Body = api.ThreadCreate{Workspace: "w", Title: "other"}
	if _, err := s.Submit(ctx, changed, time.Now()); !errors.Is(err, api.ErrConflict) {
		t.Fatalf("changed body under a known id: %v, want ErrConflict", err)
	}

	s.Close()
	s = open(t, path)
	if after, err := s.Submit(ctx, c, time.Now()); err != nil || after != first {
		t.Fatalf("receipt after reopen: %+v %v, want %+v", after, err, first)
	}
}

func TestSubmitRejectsStaleCommands(t *testing.T) {
	ctx := context.Background()
	s := open(t, filepath.Join(t.TempDir(), "host.db"))
	thread := api.NewID()
	if _, err := s.Submit(ctx, create(thread), time.Now()); err != nil {
		t.Fatal(err)
	}
	inbox := func(rev uint64, expires time.Time, target api.ID) api.Command {
		return api.Command{ID: api.NewID(), Target: target, Revision: rev, Expires: expires, Body: api.InboxSet{Disposition: api.Disposition{State: api.Settled}}}
	}
	for name, tc := range map[string]struct {
		c    api.Command
		want error
	}{
		"revision": {inbox(7, time.Time{}, thread), api.ErrRevision},
		"expired":  {inbox(0, time.Now().Add(-time.Second), thread), api.ErrExpired},
		"unknown":  {inbox(0, time.Time{}, api.NewID()), api.ErrNotFound},
	} {
		if _, err := s.Submit(ctx, tc.c, time.Now()); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", name, err, tc.want)
		}
	}
	snap, err := s.Read(ctx, api.Query{Thread: thread})
	if err != nil {
		t.Fatal(err)
	}
	if rev := snap.Threads[0].Revision; rev != 1 {
		t.Fatalf("rejected commands changed the thread: revision %d", rev)
	}
	if _, err := s.Submit(ctx, inbox(1, time.Now().Add(time.Minute), thread), time.Now()); err != nil {
		t.Fatalf("current revision: %v", err)
	}
}

func TestReadLogIsDenseAndStable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "host.db")
	s := open(t, path)
	var woke int
	s.OnCommit(func(events []api.Event) { woke += len(events) })
	for range 3 {
		if _, err := s.Submit(ctx, create(api.NewID()), time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if woke != 3 {
		t.Fatalf("hook saw %d events, want 3", woke)
	}
	page, err := s.ReadLog(ctx, 0, 2)
	if err != nil || len(page) != 2 || page[0].Cursor.Seq != 1 || page[1].Cursor.Seq != 2 {
		t.Fatalf("first page: %+v %v", page, err)
	}
	rest, err := s.ReadLog(ctx, 2, 10)
	if err != nil || len(rest) != 1 || rest[0].Cursor.Seq != 3 || rest[0].Cursor.Log != s.LogID() {
		t.Fatalf("rest: %+v %v", rest, err)
	}
	log := s.LogID()
	s.Close()
	if s = open(t, path); s.LogID() != log {
		t.Fatal("log id changed on reopen")
	}
}

// copyDB copies a closed database file by file, the way a user restores a
// backup.
func copyDB(t *testing.T, from, to string) {
	t.Helper()
	for _, sfx := range []string{"", "-wal", "-shm"} {
		b, err := os.ReadFile(from + sfx)
		if errors.Is(err, fs.ErrNotExist) {
			os.Remove(to + sfx)
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(to+sfx, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRestoredDatabaseGetsANewLog(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path, backup := filepath.Join(dir, "host.db"), filepath.Join(dir, "backup.db")
	s := open(t, path)
	thread := api.NewID()
	if _, err := s.Submit(ctx, create(thread), time.Now()); err != nil {
		t.Fatal(err)
	}
	s.Close()
	copyDB(t, path, backup)

	s = open(t, path)
	settle := api.Command{ID: api.NewID(), Target: thread, Body: api.InboxSet{Disposition: api.Disposition{State: api.Settled}}}
	seen, err := s.Submit(ctx, settle, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	s.Close()

	copyDB(t, backup, path)
	s = open(t, path)
	if s.LogID() == seen.Committed.Log {
		t.Fatalf("restored database kept log %s, so cursor %+v still looks current", s.LogID(), seen.Committed)
	}
	restored := s.LogID()
	events, err := s.ReadLog(ctx, 0, 0)
	if err != nil || len(events) != 1 || events[0].Cursor.Log != restored {
		t.Fatalf("restored log: %+v %v, want the one event under %s", events, err, restored)
	}
	s.Close()
	if s = open(t, path); s.LogID() != restored {
		t.Fatal("log id changed on a plain reopen")
	}
}

func TestARefusedObservationStillCompletesItsEffect(t *testing.T) {
	ctx := context.Background()
	s := open(t, filepath.Join(t.TempDir(), "host.db"))
	thread := api.NewID()
	submit := func(body api.Intent) api.ID {
		t.Helper()
		c := api.Command{ID: api.NewID(), Target: thread, Body: body}
		if _, err := s.Submit(ctx, c, time.Now()); err != nil {
			t.Fatal(err)
		}
		return c.ID
	}
	lease := func() int64 {
		t.Helper()
		e, ok, err := s.Lease(ctx)
		if err != nil || !ok {
			t.Fatalf("lease: %v %v", ok, err)
		}
		return e.ID
	}
	submit(create(thread).Body)
	run := submit(api.MessageDispatch{Text: "go", Mode: api.StartNow})
	if _, err := s.Observe(ctx, thread, engine.Started{Run: run}, time.Now(), lease()); err != nil {
		t.Fatal(err)
	}
	steer := submit(api.MessageDispatch{Text: "hurry", Mode: api.SteerActive})
	steering := lease()
	if _, err := s.Observe(ctx, thread, engine.Ended{Run: run, Outcome: api.OutcomeCompleted}, time.Now(), 0); err != nil {
		t.Fatal(err)
	}

	before, _ := s.ReadLog(ctx, 0, 0)
	if _, err := s.Observe(ctx, thread, engine.Steered{Message: steer}, time.Now(), steering); !errors.Is(err, engine.ErrRefused) {
		t.Fatalf("a steer that landed after its run ended: %v, want engine.ErrRefused", err)
	}
	after, _ := s.ReadLog(ctx, 0, 0)
	leased, err := s.Leased(ctx)
	if err != nil || len(leased) != 0 || len(after) != len(before) {
		t.Fatalf("refused observation: %d events added, leased %+v %v; want none and its effect done", len(after)-len(before), leased, err)
	}
}

// A message still queued in a restored database may have run in the history
// the restore lost, so the restore must not leave it to start again. That
// holds behind a run the restore makes uncertain and behind one that already
// was.
func TestRewindCancelsReadyMessagesBehindLiveRuns(t *testing.T) {
	ctx := context.Background()
	s := open(t, filepath.Join(t.TempDir(), "host.db"))
	submit := func(thread api.ID, body api.Intent) api.ID {
		t.Helper()
		c := api.Command{ID: api.NewID(), Target: thread, Body: body}
		if _, err := s.Submit(ctx, c, time.Now()); err != nil {
			t.Fatal(err)
		}
		return c.ID
	}
	running, unsure := api.NewID(), api.NewID()
	var ready, held []api.ID
	for _, thread := range []api.ID{running, unsure} {
		submit(thread, create(thread).Body)
		run := submit(thread, api.MessageDispatch{Text: "go", Mode: api.StartNow})
		e, ok, err := s.Lease(ctx)
		if err != nil || !ok {
			t.Fatalf("lease: %v %v", ok, err)
		}
		if _, err := s.Observe(ctx, thread, engine.Started{Run: run}, time.Now(), e.ID); err != nil {
			t.Fatal(err)
		}
		if thread == unsure {
			if _, err := s.Observe(ctx, thread, engine.Reconciled{Run: run}, time.Now(), 0); err != nil {
				t.Fatal(err)
			}
		}
		ready = append(ready, submit(thread, api.MessageDispatch{Text: "delete the old branch", Mode: api.AfterActive}))
		held = append(held, submit(thread, api.MessageDispatch{Text: "later", Mode: api.Defer}))
	}

	if err := s.Rewind(ctx); err != nil {
		t.Fatal(err)
	}
	for i, thread := range []api.ID{running, unsure} {
		th, err := s.Thread(ctx, thread)
		if err != nil {
			t.Fatal(err)
		}
		if m, _ := th.Message(ready[i]); m.State != engine.QueueCancelled {
			t.Errorf("thread %d: ready message is %s after the rewind, want cancelled", i, m.State)
		}
		if m, _ := th.Message(held[i]); m.State != engine.QueueHeld {
			t.Errorf("thread %d: held message is %s after the rewind, want held", i, m.State)
		}
		submit(thread, api.TurnInterrupt{}) // the user moves on from the uncertain run
	}
	if e, ok, err := s.Lease(ctx); err != nil || ok {
		t.Fatalf("abandoning the uncertain runs leased %+v %v, want nothing to start", e.Body, err)
	}
}
