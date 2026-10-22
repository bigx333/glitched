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
