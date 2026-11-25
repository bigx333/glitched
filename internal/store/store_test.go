package store_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
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

// A receipt carries its thread's revision after the commit, which is the
// view's that shows what the command did. The stored receipt says the
// same after the thread has moved on, and after a reopen.
func TestAReceiptCarriesItsThreadsRevision(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "host.db")
	s := open(t, path)
	tr := newTree(t, s)
	thread := tr.ids[0]
	revision := func(id api.ID) uint64 {
		t.Helper()
		snap, err := s.Read(ctx, api.Query{Thread: id})
		if err != nil {
			t.Fatal(err)
		}
		return snap.Threads[0].Revision
	}
	submit := func(c api.Command) api.Receipt {
		t.Helper()
		r, err := s.Submit(ctx, c, time.Now())
		if err != nil {
			t.Fatalf("%T: %v", c.Body, err)
		}
		return r
	}
	before := revision(thread)
	full := api.Command{ID: api.NewID(), Target: thread, Body: whole(sonnet, api.AccessFull)}
	took := submit(full)
	if took.Revision != revision(thread) || took.Revision <= before {
		t.Fatalf("the configure's receipt says revision %d; the thread went from %d to %d", took.Revision, before, revision(thread))
	}
	// A configure that changes nothing commits at the revision that shows
	// it already.
	if same := submit(api.Command{ID: api.NewID(), Target: thread, Body: full.Body}); same.Revision != took.Revision {
		t.Errorf("a configure that changes nothing says revision %d, want the thread's, %d", same.Revision, took.Revision)
	}
	// One that takes the tree says its own thread's.
	if narrowed := submit(api.Command{ID: api.NewID(), Target: thread, Body: api.ThreadConfigure{Access: new(api.AccessAsk)}}); narrowed.Revision != revision(thread) {
		t.Errorf("a configure that narrows the tree says revision %d, want its thread's, %d", narrowed.Revision, revision(thread))
	}
	if again := submit(full); again != took {
		t.Errorf("the stored receipt, the thread moved on: %+v, want %+v", again, took)
	}
	s.Close()
	s = open(t, path)
	if after := submit(full); after != took {
		t.Errorf("the stored receipt after a reopen: %+v, want %+v", after, took)
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
	s.OnCommit(func(c store.Commit) { woke += len(c.Events) })
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
		e, ok, err := s.Lease(ctx, nil)
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
// A Defer is cancelled with them: a send or a resume releases it, so it
// may have run in the history the rewind lost.
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
		e, ok, err := s.Lease(ctx, nil)
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
		if m, _ := th.Message(held[i]); m.State != engine.QueueCancelled {
			t.Errorf("thread %d: deferred message is %s after the rewind, want cancelled", i, m.State)
		}
		submit(thread, api.TurnInterrupt{}) // the user moves on from the uncertain run
	}
	if e, ok, err := s.Lease(ctx, nil); err != nil || ok {
		t.Fatalf("abandoning the uncertain runs leased %+v %v, want nothing to start", e.Body, err)
	}
}

// What a stop held may have run in the history a restore lost, after a
// resume: a restored backup, taken while the run stopped or once it ended,
// cancels it, and the next send runs only what it sends.
func TestARestoreCancelsWhatAStopHeld(t *testing.T) {
	ctx := context.Background()
	for _, idle := range []bool{false, true} {
		t.Run(map[bool]string{false: "stopping", true: "idle"}[idle], func(t *testing.T) {
			dir := t.TempDir()
			path, backup := filepath.Join(dir, "host.db"), filepath.Join(dir, "backup.db")
			s := open(t, path)
			thread := api.NewID()
			submit := func(body api.Intent) api.ID {
				t.Helper()
				c := api.Command{ID: api.NewID(), Target: thread, Body: body}
				if _, err := s.Submit(ctx, c, time.Now()); err != nil {
					t.Fatal(err)
				}
				return c.ID
			}
			observe := func(o engine.Observation, effect int64) {
				t.Helper()
				if _, err := s.Observe(ctx, thread, o, time.Now(), effect); err != nil {
					t.Fatal(err)
				}
			}
			lease := func() int64 {
				t.Helper()
				e, ok, err := s.Lease(ctx, nil)
				if !ok || err != nil {
					t.Fatalf("lease: %v %v", ok, err)
				}
				return e.ID
			}
			submit(create(thread).Body)
			run := submit(api.MessageDispatch{Text: "first", Mode: api.StartNow})
			observe(engine.Started{Run: run}, lease())
			next := submit(api.MessageDispatch{Text: "append to the release notes", Mode: api.AfterActive})
			submit(api.TurnInterrupt{})
			observe(engine.InterruptSent{Run: run}, lease())
			if idle {
				observe(engine.Ended{Run: run, Outcome: api.OutcomeInterrupted}, 0)
			}
			s.Close()
			copyDB(t, path, backup)
			s = open(t, path)
			if !idle {
				observe(engine.Ended{Run: run, Outcome: api.OutcomeInterrupted}, 0)
			}
			submit(api.QueueResume{})
			observe(engine.Started{Run: next}, lease())
			observe(engine.Ended{Run: next, Outcome: api.OutcomeCompleted}, 0)
			s.Close()
			copyDB(t, backup, path)
			s = open(t, path)
			if err := s.Rewind(ctx); err != nil {
				t.Fatal(err)
			}

			th, err := s.Thread(ctx, thread)
			if err != nil {
				t.Fatal(err)
			}
			if m, _ := th.Message(next); m.State != engine.QueueCancelled {
				t.Fatalf("after the restore the held message is %s, want cancelled", m.State)
			}
			if !idle {
				submit(api.TurnInterrupt{})
			}
			sent := submit(api.MessageDispatch{Text: "What's the status?", Mode: api.AfterActive})
			if th, err = s.Thread(ctx, thread); err != nil {
				t.Fatal(err)
			}
			if r := th.Live(); r == nil || r.ID != sent {
				t.Fatalf("the send after the restore runs %+v, want the new message's turn", r)
			}
		})
	}
}

// A deleted thread's held Defer is nothing a restore can run again: the
// rewind leaves the tombstone alone, and the store opens.
func TestARewindLeavesADeletedThreadsHeldMessage(t *testing.T) {
	ctx := context.Background()
	s := open(t, filepath.Join(t.TempDir(), "host.db"))
	thread := api.NewID()
	for _, body := range []api.Intent{create(thread).Body, api.MessageDispatch{Text: "later", Mode: api.Defer}, api.ThreadDelete{}} {
		if _, err := s.Submit(ctx, api.Command{ID: api.NewID(), Target: thread, Body: body}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Rewind(ctx); err != nil {
		t.Fatalf("rewind with a deleted thread holding a Defer: %v", err)
	}
}

// A rename is in the projection that a reopened store reads.
func TestRenameIsKept(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "host.db")
	s := open(t, path)
	thread := api.NewID()
	for _, body := range []api.Intent{create(thread).Body, api.ThreadRename{Title: "Websocket loop"}} {
		if _, err := s.Submit(ctx, api.Command{ID: api.NewID(), Target: thread, Body: body}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	s = open(t, path)
	snap, err := s.Read(ctx, api.Query{})
	if err != nil || len(snap.Threads) != 1 || snap.Threads[0].Title != "Websocket loop" {
		t.Fatalf("after reopen: %+v %v", snap.Threads, err)
	}
}

// A queued message's new words and place are the stored thread's after a
// reopen, as the log has them: the projection Decide reads next.
func TestAnEditedAndMovedMessageIsKept(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "host.db")
	s := open(t, path)
	thread := api.NewID()
	submit := func(body api.Intent) api.ID {
		t.Helper()
		c := api.Command{ID: api.NewID(), Target: thread, Body: body}
		if _, err := s.Submit(ctx, c, time.Now()); err != nil {
			t.Fatal(err)
		}
		return c.ID
	}
	submit(create(thread).Body)
	submit(api.MessageDispatch{Text: "first", Mode: api.StartNow})
	a := submit(api.MessageDispatch{Text: "a", Mode: api.AfterActive})
	b := submit(api.MessageDispatch{Text: "b", Mode: api.AfterActive})
	c := submit(api.MessageDispatch{Text: "c", Mode: api.AfterActive})
	submit(api.QueueEdit{Message: a, Text: "a, but shorter"})
	submit(api.QueueMove{Message: c, Before: b})
	s.Close()
	s = open(t, path)

	got, err := s.Thread(ctx, thread)
	if err != nil {
		t.Fatal(err)
	}
	log, err := s.ReadLog(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var want engine.Thread
	for _, e := range log {
		body, err := e.Decode()
		if err != nil {
			t.Fatal(err)
		}
		want = engine.Apply(want, engine.Event{Thread: e.Thread, Seq: e.Cursor.Seq, At: e.At, Body: body})
	}
	if !reflect.DeepEqual(got.Queue, want.Queue) {
		t.Fatalf("stored queue %+v, the log's %+v", got.Queue, want.Queue)
	}
	ma, _ := got.Message(a)
	mb, _ := got.Message(b)
	mc, _ := got.Message(c)
	if ma.Text != "a, but shorter" || !(ma.Pos < mc.Pos && mc.Pos < mb.Pos) {
		t.Fatalf("after a reopen the queue is %+v; want a's new words, and a, c, b in that order", got.Queue)
	}
}

// A database from before the tombstone upgrades on open: its threads,
// runs and messages stay.
func TestAnOlderDatabaseUpgrades(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "host.db")
	s := open(t, path)
	thread := api.NewID()
	for _, body := range []api.Intent{create(thread).Body, api.MessageDispatch{Text: "go", Mode: api.StartNow}} {
		if _, err := s.Submit(ctx, api.Command{ID: api.NewID(), Target: thread, Body: body}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF;
		CREATE TABLE threads_v1 (
			id BLOB PRIMARY KEY, workspace TEXT NOT NULL, title TEXT NOT NULL, model TEXT NOT NULL,
			access INTEGER NOT NULL CHECK (access BETWEEN 0 AND 2), revision INTEGER NOT NULL,
			owner_epoch INTEGER NOT NULL CHECK (owner_epoch > 0), generation INTEGER NOT NULL,
			disposition INTEGER NOT NULL CHECK (disposition BETWEEN 0 AND 2), until INTEGER,
			updated_at INTEGER NOT NULL, CHECK ((disposition = 1) = (until IS NOT NULL))) STRICT;
		INSERT INTO threads_v1 SELECT id, workspace, title, model, access, revision, owner_epoch, generation, disposition, until, updated_at FROM threads;
		DROP TABLE threads;
		ALTER TABLE threads_v1 RENAME TO threads;` + runsV6 + runsV12 + runsV13 + runsV14 + commandsV7 + `
		PRAGMA user_version = 1;`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	s = open(t, path)
	settle := api.Command{ID: api.NewID(), Target: thread, Body: api.InboxSet{Disposition: api.Disposition{State: api.Settled}}}
	if _, err := s.Submit(ctx, settle, time.Now()); err != nil {
		t.Fatalf("settle after the upgrade: %v", err)
	}
	snap, err := s.Read(ctx, api.Query{Thread: thread})
	if err != nil {
		t.Fatal(err)
	}
	if v := snap.Threads[0]; v.Disposition.State != api.Settled || v.Run != api.RunStopping {
		t.Fatalf("after the upgrade: disposition %v, run %v; want settled, its run stopping", v.Disposition.State, v.Run)
	}
}

// A database from when threads could be archived upgrades on open with
// its archived threads settled.
func TestAnArchivedThreadUpgradesSettled(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "host.db")
	s := open(t, path)
	thread := api.NewID()
	if _, err := s.Submit(ctx, create(thread), time.Now()); err != nil {
		t.Fatal(err)
	}
	s.Close()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF;
		CREATE TABLE threads_v3 (
			id BLOB PRIMARY KEY, workspace TEXT NOT NULL, title TEXT NOT NULL, model TEXT NOT NULL,
			access INTEGER NOT NULL CHECK (access BETWEEN 0 AND 2), revision INTEGER NOT NULL,
			owner_epoch INTEGER NOT NULL CHECK (owner_epoch > 0), generation INTEGER NOT NULL,
			disposition INTEGER NOT NULL CHECK (disposition BETWEEN 0 AND 3), until INTEGER,
			updated_at INTEGER NOT NULL, deleted INTEGER NOT NULL DEFAULT 0 CHECK (deleted IN (0, 1)),
			CHECK ((disposition = 1) = (until IS NOT NULL))) STRICT;
		INSERT INTO threads_v3 SELECT id, workspace, title, model, access, revision, owner_epoch, generation,
			disposition, until, updated_at, deleted FROM threads;
		DROP TABLE threads;
		ALTER TABLE threads_v3 RENAME TO threads;
		UPDATE threads SET disposition = 3;` + runsV6 + runsV12 + runsV13 + runsV14 + commandsV7 + `
		PRAGMA user_version = 3;`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	s = open(t, path)
	snap, err := s.Read(ctx, api.Query{Thread: thread})
	if err != nil {
		t.Fatal(err)
	}
	if v := snap.Threads[0]; v.Disposition.State != api.Settled {
		t.Fatalf("an archived thread upgraded %v, want settled", v.Disposition.State)
	}
}

// A deleted thread is in no read, after a reopen too, and refuses what
// is sent to it.
func TestADeletedThreadIsGone(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "host.db")
	s := open(t, path)
	gone, kept := api.NewID(), api.NewID()
	for _, c := range []api.Command{create(gone), create(kept), {ID: api.NewID(), Target: gone, Body: api.ThreadDelete{}}} {
		if _, err := s.Submit(ctx, c, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	s = open(t, path)
	snap, err := s.Read(ctx, api.Query{})
	if err != nil || len(snap.Threads) != 1 || snap.Threads[0].ID != kept {
		t.Fatalf("the shell after deleting one of two: %+v %v", snap.Threads, err)
	}
	if _, err := s.Read(ctx, api.Query{Thread: gone}); !errors.Is(err, api.ErrNotFound) {
		t.Fatalf("reading the deleted thread: %v, want ErrNotFound", err)
	}
	if _, err := s.Submit(ctx, api.Command{ID: api.NewID(), Target: gone, Body: api.ThreadRename{Title: "x"}}, time.Now()); !errors.Is(err, api.ErrNotFound) {
		t.Fatalf("renaming the deleted thread: %v, want ErrNotFound", err)
	}
	events, _ := s.ReadLog(ctx, 0, 0)
	if last := events[len(events)-1]; last.Thread != gone || last.Type != "thread.deleted" {
		t.Fatalf("the log ends %s on %v, want the tombstone", last.Type, last.Thread)
	}
}

// table is a table's shape as SQLite has it: its columns as table_info
// lists them, its CHECKs, whitespace aside, and whether it's STRICT.
type table struct {
	columns []string
	checks  []string
	strict  bool
}

func tableOf(t *testing.T, path, name string) table {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var tt table
	rows, err := db.Query(`SELECT cid, name, type, "notnull", dflt_value, pk FROM pragma_table_info(?) ORDER BY cid`, name)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		tt.columns = append(tt.columns, fmt.Sprintf("%d %s %s notnull=%d default=%v pk=%d", cid, name, typ, notnull, dflt, pk))
	}
	rows.Close()
	var ddl string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&ddl); err != nil {
		t.Fatal(err)
	}
	for rest := ddl; ; {
		i := strings.Index(rest, "CHECK")
		if i < 0 {
			break
		}
		rest = rest[i:]
		open := strings.IndexByte(rest, '(')
		depth, end := 0, -1
		for j := open; j < len(rest) && end < 0; j++ {
			switch rest[j] {
			case '(':
				depth++
			case ')':
				if depth--; depth == 0 {
					end = j + 1
				}
			}
		}
		if open < 0 || end < 0 {
			t.Fatalf("an unbalanced CHECK in %s", ddl)
		}
		tt.checks = append(tt.checks, strings.Join(strings.Fields(rest[:end]), " "))
		rest = rest[end:]
	}
	slices.Sort(tt.checks)
	if err := db.QueryRow(`SELECT strict FROM pragma_table_list WHERE name = ? AND schema = 'main'`, name).Scan(&tt.strict); err != nil {
		t.Fatal(err)
	}
	return tt
}

// downgrade rewrites a database this build created into an older build's:
// sql rebuilds its threads table, its runs and commands lose what the
// versions after version gave them, and the version is set to version.
func downgrade(t *testing.T, path, sqlText string, version int) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if version < 7 {
		sqlText += runsV6
	}
	if version < 8 {
		sqlText += commandsV7
	}
	if version < 9 {
		sqlText = threadsV8 + sqlText
	}
	if version < 10 {
		sqlText = threadsV9 + sqlText
	}
	if version < 11 {
		sqlText = threadsV10 + sqlText
	}
	if version < 12 {
		sqlText = threadsV11 + sqlText
	}
	if version < 13 {
		sqlText = threadsV12 + sqlText + runsV12
	}
	if version < 14 {
		sqlText += runsV13
	}
	if version < 15 {
		sqlText += runsV14
	}
	if version < 16 {
		sqlText = threadsV15 + sqlText
	}
	if version < 18 {
		sqlText += indexesV17
	}
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF;` + sqlText + fmt.Sprintf(`PRAGMA user_version = %d;`, version)); err != nil {
		t.Fatal(err)
	}
}

const indexesV17 = `
	DROP INDEX IF EXISTS events_by_thread_type;
	DROP INDEX IF EXISTS threads_live_updated;
	DROP INDEX IF EXISTS threads_by_parent;
`

// The runs table up to version 6, before a run kept its model and access.
const runsV6 = `
	ALTER TABLE runs DROP COLUMN model;
	ALTER TABLE runs DROP COLUMN access;
`

// The threads table up to version 8, before a thread kept its provider's
// session.
const threadsV8 = `
	ALTER TABLE threads DROP COLUMN session;
`

// The threads table up to version 10, before a thread kept how full its
// context is.
const threadsV10 = `
	ALTER TABLE threads DROP COLUMN context_used;
	ALTER TABLE threads DROP COLUMN context_window;
	ALTER TABLE threads DROP COLUMN context_stale;
	ALTER TABLE threads DROP COLUMN context_model;
`

// The threads table up to version 11, before a thread kept what its
// provider confirmed it runs at.
const threadsV11 = `
	ALTER TABLE threads DROP COLUMN granted;
`

// The threads and runs tables up to version 12, before idle compaction
// tracked cache expiry and silent runs.
const threadsV12 = `
	ALTER TABLE threads DROP COLUMN context_cache_expires;
`

const runsV12 = `
	ALTER TABLE runs DROP COLUMN silent;
`

// The runs table up to version 13, before a run kept the usage limit that
// ended it.
const runsV13 = `
	ALTER TABLE runs DROP COLUMN limit_resets;
	ALTER TABLE runs DROP COLUMN resume_cancelled;
`

func TestUpgradeRetiresCheckpointAndRevertEffects(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "host.db")
	s := open(t, path)
	thread := api.NewID()
	if _, err := s.Submit(ctx, create(thread), time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Submit(ctx, api.Command{ID: api.NewID(), Target: thread, Body: api.MessageDispatch{Text: "go", Mode: api.StartNow}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	s.Close()
	downgrade(t, path, `
		INSERT INTO effects (thread, kind, payload, state) VALUES
			(zeroblob(16), 'checkpoint.capture', x'7b7d', 'pending'),
			(zeroblob(16), 'checkpoint.drop', x'7b7d', 'leased'),
			(zeroblob(16), 'thread.revert', x'7b7d', 'pending'),
			(zeroblob(16), 'thread.revert', CAST('{"KeepFiles":true}' AS BLOB), 'leased');
		INSERT INTO effects (thread, kind, payload, state)
			SELECT id, 'thread.revert', x'7b7d', 'leased' FROM threads;
	`, 16)
	var warnings bytes.Buffer
	logger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&warnings, nil)))
	t.Cleanup(func() { slog.SetDefault(logger) })
	s = open(t, path)
	defer s.Close()
	if got := warnings.String(); strings.Count(got, "unfinished file restore") != 2 || !strings.Contains(got, "workspace=w") {
		t.Fatalf("upgrade warnings: %q", got)
	}
	if leased, err := s.Leased(ctx); err != nil || len(leased) != 0 {
		t.Fatalf("leased effects after upgrade: %v, %v", leased, err)
	}
	e, ok, err := s.Lease(ctx, nil)
	if _, start := e.Body.(engine.StartTurn); err != nil || !ok || !start {
		t.Fatalf("expected existing turn start after upgrade: %+v, %v, %v", e, ok, err)
	}
	if e, ok, err := s.Lease(ctx, nil); err != nil || ok {
		t.Fatalf("remaining pending effect after upgrade: %+v, %v, %v", e, ok, err)
	}
}

// The runs table up to version 14, before a rewind marked the runs it
// dropped.
const runsV14 = `
	ALTER TABLE runs DROP COLUMN dropped;
`

// The threads table up to version 15, before a thread could be a remote
// child's placeholder or come from another machine.
const threadsV15 = `
	ALTER TABLE threads DROP COLUMN remote;
	ALTER TABLE threads DROP COLUMN origin;
`

// The threads table up to version 9, before a thread in a worktree kept
// its project and branch.
const threadsV9 = `
	ALTER TABLE threads DROP COLUMN project;
	ALTER TABLE threads DROP COLUMN branch;
`

// The commands table up to version 7, before a receipt kept its thread's
// revision.
const commandsV7 = `
	ALTER TABLE commands DROP COLUMN revision;
`

// The threads table of version 3, archived threads and all.
const threadsV3 = `
	CREATE TABLE threads_v3 (
		id BLOB PRIMARY KEY, workspace TEXT NOT NULL, title TEXT NOT NULL, model TEXT NOT NULL,
		access INTEGER NOT NULL CHECK (access BETWEEN 0 AND 2), revision INTEGER NOT NULL,
		owner_epoch INTEGER NOT NULL CHECK (owner_epoch > 0), generation INTEGER NOT NULL,
		disposition INTEGER NOT NULL CHECK (disposition BETWEEN 0 AND 3), until INTEGER,
		updated_at INTEGER NOT NULL, deleted INTEGER NOT NULL DEFAULT 0 CHECK (deleted IN (0, 1)),
		CHECK ((disposition = 1) = (until IS NOT NULL))) STRICT;
	INSERT INTO threads_v3 SELECT id, workspace, title, model, access, revision, owner_epoch, generation,
		disposition, until, updated_at, deleted FROM threads;
	DROP TABLE threads;
	ALTER TABLE threads_v3 RENAME TO threads;
`

// The threads table of version 4, main's before child threads.
const threadsV4 = `
	CREATE TABLE threads_v4 (
		id BLOB PRIMARY KEY, workspace TEXT NOT NULL, title TEXT NOT NULL, model TEXT NOT NULL,
		access INTEGER NOT NULL CHECK (access BETWEEN 0 AND 2), revision INTEGER NOT NULL,
		owner_epoch INTEGER NOT NULL CHECK (owner_epoch > 0), generation INTEGER NOT NULL,
		disposition INTEGER NOT NULL CHECK (disposition BETWEEN 0 AND 2), until INTEGER,
		updated_at INTEGER NOT NULL, deleted INTEGER NOT NULL DEFAULT 0 CHECK (deleted IN (0, 1)),
		CHECK ((disposition = 1) = (until IS NOT NULL))) STRICT;
	INSERT INTO threads_v4 SELECT id, workspace, title, model, access, revision, owner_epoch, generation,
		disposition, until, updated_at, deleted FROM threads;
	DROP TABLE threads;
	ALTER TABLE threads_v4 RENAME TO threads;
`

// A database upgraded from version 3, from when threads could be archived,
// or from main's version 4 ends with the threads table a new one has.
func TestAnUpgradedThreadsTableIsANewOnes(t *testing.T) {
	fresh := filepath.Join(t.TempDir(), "host.db")
	open(t, fresh).Close()
	want := tableOf(t, fresh, "threads")
	if !want.strict || len(want.checks) == 0 {
		t.Fatalf("a new threads table: %+v", want)
	}
	for _, tc := range []struct {
		name    string
		sql     string
		version int
	}{{"version 3", threadsV3, 3}, {"version 4", threadsV4, 4}} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "host.db")
			open(t, path).Close()
			downgrade(t, path, tc.sql, tc.version)
			open(t, path).Close()
			got := tableOf(t, path, "threads")
			if !slices.Equal(got.columns, want.columns) {
				t.Errorf("columns\n%s\nwant a new table's\n%s", strings.Join(got.columns, "\n"), strings.Join(want.columns, "\n"))
			}
			if !slices.Equal(got.checks, want.checks) {
				t.Errorf("checks %q, want %q", got.checks, want.checks)
			}
			if got.strict != want.strict {
				t.Errorf("strict %v, want %v", got.strict, want.strict)
			}
		})
	}
}

// After an upgrade from version 3, a child thread can be created, its turn's
// tokens kept and its report recorded on its parent, and they read back.
func TestAChildThreadAfterTheUpgradeFromVersion3(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "host.db")
	s := open(t, path)
	parent := api.NewID()
	if _, err := s.Submit(ctx, create(parent), time.Now()); err != nil {
		t.Fatal(err)
	}
	s.Close()
	downgrade(t, path, threadsV3, 3)

	s = open(t, path)
	child := api.NewID()
	submit := func(body api.Intent) api.ID {
		t.Helper()
		c := api.Command{ID: api.NewID(), Target: child, Body: body}
		if _, err := s.Submit(ctx, c, time.Now()); err != nil {
			t.Fatal(err)
		}
		return c.ID
	}
	spawner := api.NewID()
	if _, err := s.Submit(ctx, api.Command{ID: spawner, Target: parent, Body: api.MessageDispatch{Text: "split it", Mode: api.StartNow}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	submit(api.ThreadCreate{Workspace: "w", Title: "child", Model: api.ModelRef{Provider: "fake"}, Parent: parent, Run: spawner})
	if _, err := s.Observe(ctx, parent, engine.Ended{Run: spawner, Outcome: api.OutcomeCompleted}, time.Now(), 0); err != nil {
		t.Fatal(err)
	}
	run := submit(api.MessageDispatch{Text: "go", Mode: api.StartNow})
	e, ok, err := s.Lease(ctx, func() []api.ID { return []api.ID{parent} })
	if err != nil || !ok {
		t.Fatalf("lease: %v %v", ok, err)
	}
	if _, err := s.Observe(ctx, child, engine.Started{Run: run}, time.Now(), e.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Observe(ctx, child, engine.Ended{Run: run, Outcome: api.OutcomeCompleted, Tokens: 1200}, time.Now(), 0); err != nil {
		t.Fatal(err)
	}
	report := api.ChildReported{Child: child, Run: run, Kind: api.ReportDone, Title: "child", Outcome: "done"}
	if _, err := s.Observe(ctx, parent, engine.Reported{Report: report}, time.Now(), 0); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s = open(t, path)
	read := func(id api.ID) api.Thread {
		t.Helper()
		snap, err := s.Read(ctx, api.Query{Thread: id})
		if err != nil {
			t.Fatal(err)
		}
		return snap.Threads[0]
	}
	if v := read(child); v.Parent != parent || v.Tokens != 1200 {
		t.Errorf("the child after a reopen: parent %v, tokens %d; want %v, 1200", v.Parent, v.Tokens, parent)
	}
	if v := read(parent); v.Reported.IsZero() || !v.Parent.IsZero() {
		t.Errorf("the parent after a reopen: reported %v, parent %v; want its report's time, and no parent", v.Reported, v.Parent)
	}
}

// fixture is the path of a copy of testdata's database name.
func fixture(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "host.db")
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// testdata/v5.db is a database main's store wrote at version 5: a parent
// with three reports from two children, the last Blocked, and a thread
// with none. Opening it upgrades it to the projection its log folds to,
// the parent's last report filled in from its log, and the threads table
// a new database has.
func TestAVersion5DatabaseUpgradesWithEachLastReport(t *testing.T) {
	ctx := context.Background()
	path := fixture(t, "v5.db")
	s := open(t, path)
	log, err := s.ReadLog(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	folded := map[api.ID]engine.Thread{}
	var last api.Event
	for _, e := range log {
		body, err := e.Decode()
		if err != nil {
			t.Fatal(err)
		}
		folded[e.Thread] = engine.Apply(folded[e.Thread], engine.Event{Thread: e.Thread, Seq: e.Cursor.Seq, At: e.At, Body: body})
		if _, ok := body.(api.ChildReported); ok {
			last = e
		}
	}
	if len(folded) != 4 || last.Cursor.Seq == 0 {
		t.Fatalf("the fixture's log holds %d threads, last report at %d; want 4 threads and a report", len(folded), last.Cursor.Seq)
	}
	for id, want := range folded {
		got, err := s.Thread(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("thread %s upgraded to\n%+v\nwant its log's fold\n%+v", want.Title, got, want)
		}
	}
	snap, err := s.Read(ctx, api.Query{Thread: last.Thread})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := last.Decode()
	want := api.Report{Child: body.(api.ChildReported).Child, Kind: api.ReportBlocked, Cursor: api.Cursor{Log: s.LogID(), Seq: last.Cursor.Seq}}
	if got := snap.Threads[0].LastReport; got == nil || *got != want {
		t.Errorf("the parent's last report %+v, want %+v", got, want)
	}
	s.Close()
	fresh := filepath.Join(t.TempDir(), "host.db")
	open(t, fresh).Close()
	if got, want := tableOf(t, path, "threads"), tableOf(t, fresh, "threads"); !slices.Equal(got.columns, want.columns) {
		t.Errorf("columns\n%s\nwant a new table's\n%s", strings.Join(got.columns, "\n"), strings.Join(want.columns, "\n"))
	}
}

// testdata/v6.db is a database the store wrote at version 6, when a thread
// could be configured and its runs kept neither model nor access: a thread
// never configured, one configured between its runs and widened under its
// live one, one configured with no run, and a parent narrowed under its
// live run, its child with it. Opening it upgrades it to the projection
// its log folds to, each run at the model and access it was admitted at,
// and the runs table a new database has.
func TestAVersion6DatabaseUpgradesWithEachRunsModelAndAccess(t *testing.T) {
	ctx := context.Background()
	path := fixture(t, "v6.db")
	s := open(t, path)
	log, err := s.ReadLog(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	folded := map[api.ID]engine.Thread{}
	for _, e := range log {
		body, err := e.Decode()
		if err != nil {
			t.Fatal(err)
		}
		folded[e.Thread] = engine.Apply(folded[e.Thread], engine.Event{Thread: e.Thread, Seq: e.Cursor.Seq, At: e.At, Body: body})
	}
	type admitted struct {
		model  api.ModelRef
		access api.Access
	}
	opus := api.ModelRef{Provider: "fake", Model: "Claude Opus 5.5", Effort: "high", Fast: true}
	sonnet := api.ModelRef{Provider: "fake", Model: "Claude Sonnet 5.5", Effort: "medium"}
	haiku := api.ModelRef{Provider: "fake", Model: "Claude Haiku 5.5"}
	runs := map[string][]admitted{
		"plain":      {{opus, api.AccessFull}, {opus, api.AccessFull}},
		"configured": {{haiku, api.AccessAsk}, {sonnet, api.AccessAutoEdits}, {sonnet, api.AccessAutoEdits}},
		"idle":       nil,
		"parent":     {{opus, api.AccessFull}},
		"child":      {{sonnet, api.AccessAutoEdits}},
	}
	if len(folded) != len(runs) {
		t.Fatalf("the fixture's log holds %d threads, want %d", len(folded), len(runs))
	}
	var widened api.ID
	for id, want := range folded {
		got, err := s.Thread(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("thread %s upgraded to\n%+v\nwant its log's fold\n%+v", want.Title, got, want)
		}
		var at []admitted
		for _, r := range got.Runs {
			at = append(at, admitted{r.Model, r.Access})
		}
		if !slices.Equal(at, runs[got.Title]) {
			t.Errorf("thread %s's runs upgraded at %+v, want %+v", got.Title, at, runs[got.Title])
		}
		if got.Title == "configured" {
			widened = id
		}
	}

	// The thread widened under its live run: the run starts a child as
	// wide as it was admitted, and none as wide as the thread is now.
	snap, err := s.Read(ctx, api.Query{Thread: widened})
	if err != nil {
		t.Fatal(err)
	}
	v := snap.Threads[0]
	if v.Access != api.AccessFull || v.Turn == nil || v.Turn.Model != sonnet || v.Turn.Access != api.AccessAutoEdits {
		t.Fatalf("the widened thread reads at %v, its turn %+v; want it at Full, its turn at %+v and AutoEdits", v.Access, v.Turn, sonnet)
	}
	child := func(a api.Access) error {
		_, err := s.Submit(ctx, api.Command{ID: api.NewID(), Target: api.NewID(), Body: api.ThreadCreate{
			Workspace: "w", Title: "child", Model: sonnet, Access: a, Parent: widened, Run: v.Turn.Run, Text: "work",
		}}, time.Now())
		return err
	}
	var re *api.RejectedError
	if err := child(api.AccessFull); !errors.As(err, &re) {
		t.Errorf("a child at the access set for the next turn: %v, want refused", err)
	}
	if err := child(api.AccessAutoEdits); err != nil {
		t.Errorf("a child as wide as its run: %v", err)
	}
	s.Close()
	fresh := filepath.Join(t.TempDir(), "host.db")
	open(t, fresh).Close()
	if got, want := tableOf(t, path, "runs"), tableOf(t, fresh, "runs"); !reflect.DeepEqual(got, want) {
		t.Errorf("the runs table\n%+v\nwant a new database's\n%+v", got, want)
	}
}

// A database from before receipts kept their thread's revision upgrades
// to the commands table a new one has. A receipt stored before says no
// revision, and one stored after says its thread's.
func TestAVersion7DatabaseUpgradesWithItsReceipts(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "host.db")
	s := open(t, path)
	thread := api.NewID()
	old := create(thread)
	first, err := s.Submit(ctx, old, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	downgrade(t, path, "", 7)

	s = open(t, path)
	first.Revision = 0
	if again, err := s.Submit(ctx, old, time.Now()); err != nil || again != first {
		t.Errorf("a receipt stored before the upgrade: %+v %v, want %+v", again, err, first)
	}
	rename := api.Command{ID: api.NewID(), Target: thread, Body: api.ThreadRename{Title: "renamed"}}
	took, err := s.Submit(ctx, rename, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if th := threadOf(t, s, thread); took.Revision == 0 || took.Revision != th.Revision {
		t.Errorf("a receipt after the upgrade says revision %d, want the thread's, %d", took.Revision, th.Revision)
	}
	if again, err := s.Submit(ctx, rename, time.Now()); err != nil || again != took {
		t.Errorf("that receipt again: %+v %v, want %+v", again, err, took)
	}
	s.Close()
	fresh := filepath.Join(t.TempDir(), "host.db")
	open(t, fresh).Close()
	if got, want := tableOf(t, path, "commands"), tableOf(t, fresh, "commands"); !reflect.DeepEqual(got, want) {
		t.Errorf("the commands table\n%+v\nwant a new database's\n%+v", got, want)
	}
}

// A wake a stopped parent holds is a stored message like any other: after
// a reopen it still goes in with the user's next message, and a report
// replayed then logs nothing more.
func TestAHeldWakeOutlivesAReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "host.db")
	s := open(t, path)
	parent := api.NewID()
	submit := func(body api.Intent) api.ID {
		t.Helper()
		c := api.Command{ID: api.NewID(), Target: parent, Body: body}
		if _, err := s.Submit(ctx, c, time.Now()); err != nil {
			t.Fatal(err)
		}
		return c.ID
	}
	observe := func(o engine.Observation) {
		t.Helper()
		if _, err := s.Observe(ctx, parent, o, time.Now(), 0); err != nil {
			t.Fatal(err)
		}
	}
	submit(create(parent).Body)
	run := submit(api.MessageDispatch{Text: "go", Mode: api.StartNow})
	observe(engine.Started{Run: run})
	submit(api.TurnInterrupt{})
	observe(engine.Ended{Run: run, Outcome: api.OutcomeInterrupted})
	report := engine.Reported{Report: api.ChildReported{Child: api.NewID(), Run: api.NewID(), Kind: api.ReportDone, Title: "child", Outcome: "done"}}
	observe(report)
	s.Close()

	s = open(t, path)
	observe(report)
	next := submit(api.MessageDispatch{Text: "carry on", Mode: api.StartNow})
	snap, err := s.Read(ctx, api.Query{Thread: parent})
	if err != nil {
		t.Fatal(err)
	}
	var reports, wakes, claimed int
	for _, e := range snap.Events {
		b, _ := e.Decode()
		switch b := b.(type) {
		case api.ChildReported:
			reports++
		case api.MessageQueued:
			if !b.Report.IsZero() {
				wakes++
			}
		case api.MessageClaimed:
			if b.Run == next {
				claimed++
			}
		}
	}
	if reports != 1 || wakes != 1 || claimed != 1 {
		t.Errorf("%d reports, %d wakes, %d claimed by the next message; want one of each", reports, wakes, claimed)
	}
}

// A database from before threads kept their provider's session upgrades
// to the threads table a new one has. Its threads have none, a session a
// run binds after is kept across a reopen, and a stale run binds nothing.
func TestAVersion8DatabaseUpgradesWithASessionPerThread(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "host.db")
	s := open(t, path)
	thread := api.NewID()
	if _, err := s.Submit(ctx, create(thread), time.Now()); err != nil {
		t.Fatal(err)
	}
	s.Close()
	downgrade(t, path, "", 8)

	s = open(t, path)
	if th := threadOf(t, s, thread); th.Session != "" {
		t.Errorf("an upgraded thread has session %q, want none", th.Session)
	}
	run := api.NewID()
	if _, err := s.Submit(ctx, api.Command{ID: run, Target: thread, Body: api.MessageDispatch{Text: "go", Mode: api.StartNow}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, o := range []engine.Observation{
		engine.Bound{Run: run, Session: "sess-1"},
		engine.Bound{Run: run, Session: "sess-1"},
		engine.Bound{Run: run, Session: ""},
		engine.Bound{Run: api.NewID(), Session: "stale"},
	} {
		if _, err := s.Observe(ctx, thread, o, time.Now(), 0); err != nil {
			t.Fatal(err)
		}
	}
	snap, err := s.Read(ctx, api.Query{Thread: thread})
	if err != nil {
		t.Fatal(err)
	}
	var bound int
	for _, e := range snap.Events {
		if b, _ := e.Decode(); b == (api.SessionBound{Run: run, Session: "sess-1"}) {
			bound++
		} else if _, ok := b.(api.SessionBound); ok {
			t.Errorf("an empty, repeated or stale bind logged %+v", b)
		}
	}
	if bound != 1 {
		t.Errorf("%d session.bound events, want 1", bound)
	}
	s.Close()
	s = open(t, path)
	if th := threadOf(t, s, thread); th.Session != "sess-1" {
		t.Errorf("after a reopen the thread has session %q, want sess-1", th.Session)
	}
	s.Close()
	fresh := filepath.Join(t.TempDir(), "host.db")
	open(t, fresh).Close()
	if got, want := tableOf(t, path, "threads"), tableOf(t, fresh, "threads"); !reflect.DeepEqual(got, want) {
		t.Errorf("the threads table\n%+v\nwant a new database's\n%+v", got, want)
	}
}

// A version 9 database upgrades with no project or branch on its threads,
// and a thread in a worktree created after keeps both across a reopen.
func TestAVersion9DatabaseUpgradesWithAProjectAndBranchPerThread(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "host.db")
	s := open(t, path)
	old := api.NewID()
	if _, err := s.Submit(ctx, create(old), time.Now()); err != nil {
		t.Fatal(err)
	}
	s.Close()
	downgrade(t, path, "", 9)

	s = open(t, path)
	if th := threadOf(t, s, old); th.Project != "" || th.Branch != "" {
		t.Errorf("an upgraded thread has project %q and branch %q, want neither", th.Project, th.Branch)
	}
	wt := api.NewID()
	body := api.ThreadCreate{Workspace: "/wt/glitched/fix-1", Project: "/src/glitched", Branch: "glitched/fix-1", Title: "fix", Model: api.ModelRef{Provider: "fake"}}
	if _, err := s.Submit(ctx, api.Command{ID: wt, Target: wt, Body: body}, time.Now()); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s = open(t, path)
	defer s.Close()
	if th := threadOf(t, s, wt); th.Workspace != body.Workspace || th.Project != body.Project || th.Branch != body.Branch {
		t.Errorf("after a reopen the thread is in %q of %q on %q, want %q of %q on %q", th.Workspace, th.Project, th.Branch, body.Workspace, body.Project, body.Branch)
	}
	fresh := filepath.Join(t.TempDir(), "host.db")
	open(t, fresh).Close()
	if got, want := tableOf(t, path, "threads"), tableOf(t, fresh, "threads"); !reflect.DeepEqual(got, want) {
		t.Errorf("the threads table\n%+v\nwant a new database's\n%+v", got, want)
	}
}

// A database a newer build wrote is refused with what to do, and left as
// it was.
func TestADatabaseFromANewerBuildIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "host.db")
	open(t, path).Close()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 99;`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	_, err = store.Open(path)
	if err == nil || !strings.Contains(err.Error(), "from a newer Glitched") || !strings.Contains(err.Error(), "schema version 99") {
		t.Fatalf("opening a newer database: %v", err)
	}
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 99 {
		t.Fatalf("the refused database is at version %d (%v)", version, err)
	}
}

// A database from before threads kept their context upgrades to the
// threads table a new one has. Its threads have none; what a live run
// reports after is kept across a reopen, a report that leaves the window
// out keeps the one known, and a compaction marks it stale.
func TestAVersion10DatabaseUpgradesWithAContextPerThread(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "host.db")
	s := open(t, path)
	thread := api.NewID()
	if _, err := s.Submit(ctx, create(thread), time.Now()); err != nil {
		t.Fatal(err)
	}
	s.Close()
	downgrade(t, path, "", 10)

	s = open(t, path)
	if th := threadOf(t, s, thread); th.Context != (api.Context{}) {
		t.Errorf("an upgraded thread has context %+v, want none", th.Context)
	}
	run := api.NewID()
	if _, err := s.Submit(ctx, api.Command{ID: run, Target: thread, Body: api.MessageDispatch{Text: "go", Mode: api.StartNow}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, o := range []engine.Observation{
		engine.Context{Run: run, Used: 30000, Window: 200000, Model: "claude-haiku-5-5"},
		engine.Context{Run: run, Used: 41000, Model: "claude-haiku-5-5"},
		engine.Context{Run: run, Compacted: true},
		engine.Context{Run: api.NewID(), Used: 9, Window: 9},
	} {
		if _, err := s.Observe(ctx, thread, o, time.Now(), 0); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	s = open(t, path)
	if th, want := threadOf(t, s, thread), (api.Context{Used: 41000, Window: 200000, Model: "claude-haiku-5-5", Stale: true}); th.Context != want {
		t.Errorf("after a reopen the thread's context is %+v, want %+v", th.Context, want)
	}
	s.Close()
	fresh := filepath.Join(t.TempDir(), "host.db")
	open(t, fresh).Close()
	if got, want := tableOf(t, path, "threads"), tableOf(t, fresh, "threads"); !reflect.DeepEqual(got, want) {
		t.Errorf("the threads table\n%+v\nwant a new database's\n%+v", got, want)
	}
}

// A database from before threads kept what their provider confirmed
// upgrades to the threads table a new one has. Its threads have nothing
// confirmed; what a live run's provider confirms after is kept across a
// reopen, with what the run asked, and a confirmation of a run that isn't
// the thread's is dropped.
func TestAVersion11DatabaseUpgradesWithWhatEachThreadsProviderConfirmed(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "host.db")
	s := open(t, path)
	thread := api.NewID()
	if _, err := s.Submit(ctx, create(thread), time.Now()); err != nil {
		t.Fatal(err)
	}
	s.Close()
	downgrade(t, path, "", 11)

	s = open(t, path)
	if th := threadOf(t, s, thread); th.Confirmed != (api.Confirmed{}) {
		t.Errorf("an upgraded thread has %+v confirmed, want nothing", th.Confirmed)
	}
	run := api.NewID()
	if _, err := s.Submit(ctx, api.Command{ID: run, Target: thread, Body: api.MessageDispatch{Text: "go", Mode: api.StartNow}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, o := range []engine.Observation{
		engine.Granted{Run: run, Access: api.AccessAutoEdits, Mode: "acceptEdits", Why: "auto mode unavailable for this model"},
		engine.Ran{Run: run, Model: "claude-sonnet-5-5", Effort: "high"},
		engine.Granted{Run: api.NewID(), Access: api.AccessFull},
	} {
		if _, err := s.Observe(ctx, thread, o, time.Now(), 0); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	s = open(t, path)
	th := threadOf(t, s, thread)
	if want := (api.ConfirmedAccess{Run: run, Asked: th.Access, Access: api.AccessAutoEdits, Mode: "acceptEdits", Why: "auto mode unavailable for this model"}); th.Confirmed.Access == nil || *th.Confirmed.Access != want {
		t.Errorf("after a reopen the confirmed access is %+v, want %+v", th.Confirmed.Access, want)
	}
	if want := (api.ConfirmedModel{Run: run, Asked: th.Model, Model: "claude-sonnet-5-5", Effort: "high"}); th.Confirmed.Model == nil || *th.Confirmed.Model != want {
		t.Errorf("after a reopen the confirmed model is %+v, want %+v", th.Confirmed.Model, want)
	}
	s.Close()
	fresh := filepath.Join(t.TempDir(), "host.db")
	open(t, fresh).Close()
	if got, want := tableOf(t, path, "threads"), tableOf(t, fresh, "threads"); !reflect.DeepEqual(got, want) {
		t.Errorf("the threads table\n%+v\nwant a new database's\n%+v", got, want)
	}
}

// A version 13 database upgrades to the runs table a new one has: its runs
// read as no usage limit ended them, and a run that a limit ends after,
// whether its provider said when the limit resets or not, keeps it and the
// user's cancel across reopen.
func TestAVersion13DatabaseUpgradesWithUsageLimits(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "host.db")
	s := open(t, path)
	thread := api.NewID()
	base := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	if _, err := s.Submit(ctx, api.Command{
		ID: api.NewID(), Target: thread,
		Body: api.ThreadCreate{Workspace: "w", Title: "t", Model: api.ModelRef{Provider: "claude", Model: "claude-opus-4-6"}},
	}, base); err != nil {
		t.Fatal(err)
	}
	turn := func(limit *api.UsageLimit) api.ID {
		t.Helper()
		run := api.NewID()
		if _, err := s.Submit(ctx, api.Command{ID: run, Target: thread, Body: api.MessageDispatch{Text: "go", Mode: api.StartNow}}, base); err != nil {
			t.Fatal(err)
		}
		outcome := api.OutcomeCompleted
		if limit != nil {
			outcome = api.OutcomeFailed
		}
		for _, o := range []engine.Observation{engine.Started{Run: run}, engine.Ended{Run: run, Outcome: outcome, Limit: limit}} {
			if _, err := s.Observe(ctx, thread, o, base, 0); err != nil {
				t.Fatal(err)
			}
		}
		return run
	}
	turn(nil)
	s.Close()
	downgrade(t, path, "", 13)

	s = open(t, path)
	if th := threadOf(t, s, thread); th.Runs[0].Limit != nil || th.Runs[0].ResumeCancelled {
		t.Fatalf("an upgraded run reads %+v, want no usage limit", th.Runs[0])
	}
	resets := base.Add(time.Hour)
	turn(&api.UsageLimit{})
	last := turn(&api.UsageLimit{Resets: resets})
	if _, err := s.Submit(ctx, api.Command{ID: api.NewID(), Target: thread, Body: api.ResumeCancel{Run: last}}, base); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s = open(t, path)
	th := threadOf(t, s, thread)
	s.Close()
	if r := th.Runs[1]; r.Limit == nil || !r.Limit.Resets.IsZero() || r.ResumeCancelled {
		t.Errorf("after reopen the run a limit of no known reset ended reads %+v", r)
	}
	if r := th.Runs[2]; r.Limit == nil || !r.Limit.Resets.Equal(resets) || !r.ResumeCancelled {
		t.Errorf("after reopen the run a limit resetting at %v ended, cancelled, reads %+v", resets, r)
	}
	fresh := filepath.Join(t.TempDir(), "host.db")
	open(t, fresh).Close()
	if got, want := tableOf(t, path, "runs"), tableOf(t, fresh, "runs"); !reflect.DeepEqual(got, want) {
		t.Errorf("the runs table\n%+v\nwant a new database's\n%+v", got, want)
	}
}

// A version 12 database upgrades to the threads and runs tables a new one
// has: cache expiry and silent runs persist across reopen, and
// IdleCompactCandidates finds eligible threads.
func TestAVersion12DatabaseUpgradesWithIdleCompactionFields(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "host.db")
	s := open(t, path)
	thread := api.NewID()
	if _, err := s.Submit(ctx, api.Command{
		ID: api.NewID(), Target: thread,
		Body: api.ThreadCreate{Workspace: "w", Title: "t", Model: api.ModelRef{Provider: "claude", Model: "claude-opus-4-6"}},
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	s.Close()
	downgrade(t, path, "", 12)

	s = open(t, path)
	base := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	run := api.NewID()
	if _, err := s.Submit(ctx, api.Command{ID: run, Target: thread, Body: api.MessageDispatch{Text: "go", Mode: api.StartNow}}, base); err != nil {
		t.Fatal(err)
	}
	expires := base.Add(time.Hour)
	for _, o := range []engine.Observation{
		engine.Started{Run: run},
		engine.Bound{Run: run, Session: "sess-1"},
		engine.Context{Run: run, Used: engine.MinIdleCompactTokens, Window: 1000000, Model: "claude-opus-4-6", CacheExpires: expires},
		engine.Ended{Run: run, Outcome: api.OutcomeCompleted},
	} {
		if _, err := s.Observe(ctx, thread, o, base.Add(time.Second), 0); err != nil {
			t.Fatal(err)
		}
	}
	cands, err := s.IdleCompactCandidates(ctx, engine.MinIdleCompactTokens)
	if err != nil || !slices.Equal(cands, []api.ID{thread}) {
		t.Fatalf("candidates %v (%v), want [%v]", cands, err, thread)
	}

	compactRun := api.NewID()
	if _, err := s.Observe(ctx, thread, engine.IdleCompact{Run: compactRun, AfterRun: run}, expires.Add(-engine.ClaudeCacheTTL/10+time.Second), 0); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s = open(t, path)
	th := threadOf(t, s, thread)
	if !th.Context.CacheExpires.Equal(expires) {
		t.Errorf("after reopen cache expires %v, want %v", th.Context.CacheExpires, expires)
	}
	if len(th.Runs) != 2 || !th.Runs[1].Silent {
		t.Errorf("after reopen runs %+v, want second run silent", th.Runs)
	}
	s.Close()

	fresh := filepath.Join(t.TempDir(), "host.db")
	open(t, fresh).Close()
	for _, tbl := range []string{"threads", "runs"} {
		if got, want := tableOf(t, path, tbl), tableOf(t, fresh, tbl); !reflect.DeepEqual(got, want) {
			t.Errorf("the %s table\n%+v\nwant a new database's\n%+v", tbl, got, want)
		}
	}
}

// A version 15 database upgrades to the threads table a new one has. Its
// threads read as no remote child's placeholder, as their logs fold, and
// its new columns hold nothing.
func TestAVersion15DatabaseUpgradesWithNoRemoteChild(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "host.db")
	s := open(t, path)
	thread := api.NewID()
	if _, err := s.Submit(ctx, create(thread), time.Now()); err != nil {
		t.Fatal(err)
	}
	s.Close()
	downgrade(t, path, "", 15)

	s = open(t, path)
	got := threadOf(t, s, thread)
	log, err := s.ReadLog(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	var want engine.Thread
	for _, e := range log {
		body, err := e.Decode()
		if err != nil {
			t.Fatal(err)
		}
		want = engine.Apply(want, engine.Event{Thread: e.Thread, Seq: e.Cursor.Seq, At: e.At, Body: body})
	}
	if got.Remote != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("the upgraded thread reads\n%+v\nwant its log's fold, no placeholder\n%+v", got, want)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var nulls int
	if err := db.QueryRow(`SELECT COUNT(*) FROM threads WHERE remote IS NULL AND origin IS NULL`).Scan(&nulls); err != nil || nulls != 1 {
		t.Fatalf("%d threads with no remote and no origin (%v); want the one", nulls, err)
	}
	fresh := filepath.Join(t.TempDir(), "host.db")
	open(t, fresh).Close()
	if got, want := tableOf(t, path, "threads"), tableOf(t, fresh, "threads"); !reflect.DeepEqual(got, want) {
		t.Errorf("the threads table\n%+v\nwant a new database's\n%+v", got, want)
	}
}

// A cancelled write stops, and doesn't wait out the write lock another
// connection holds, as a host that closes would.
func TestACancelledWriteStops(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "host.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	thread := api.NewID()
	if _, err := s.Submit(ctx, api.Command{ID: api.NewID(), Target: thread, Body: api.ThreadCreate{}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	other, err := sql.Open("sqlite", path+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	locked, err := other.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.Rollback()

	closing, cancel := context.WithCancel(ctx)
	time.AfterFunc(50*time.Millisecond, cancel)
	began := time.Now()
	settle := api.Command{ID: api.NewID(), Target: thread, Body: api.InboxSet{Disposition: api.Disposition{State: api.Settled}}}
	_, err = s.Submit(closing, settle, time.Now())
	if took := time.Since(began); !errors.Is(err, context.Canceled) || took > time.Second {
		t.Fatalf("a write cancelled waiting for the lock returned %v after %s, want context.Canceled at once", err, took)
	}
}

func TestAVersion17DatabaseUpgradesWithIndexes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "host.db")
	s := open(t, path)
	if _, err := s.Submit(context.Background(), create(api.NewID()), time.Now()); err != nil {
		t.Fatal(err)
	}
	s.Close()
	downgrade(t, path, "", 17)

	s = open(t, path)
	s.Close()

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 18 {
		t.Fatalf("user_version = %d (%v), want 18", version, err)
	}
	for _, idx := range []string{"events_by_thread_type", "threads_live_updated", "threads_by_parent"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, idx).Scan(&count); err != nil || count != 1 {
			t.Errorf("index %s missing after upgrade (count=%d, err=%v)", idx, count, err)
		}
	}
}

func TestReadBulkMatchesSingleThreadLoad(t *testing.T) {
	ctx := context.Background()
	s := open(t, filepath.Join(t.TempDir(), "host.db"))
	now := time.Now().UTC().Truncate(time.Millisecond)

	// Thread 1: live run with queued message and pending request.
	t1 := api.NewID()
	if _, err := s.Submit(ctx, create(t1), now); err != nil {
		t.Fatal(err)
	}
	r1 := api.NewID()
	if _, err := s.Submit(ctx, api.Command{ID: r1, Target: t1, Body: api.MessageDispatch{Text: "first", Mode: api.StartNow}}, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Submit(ctx, api.Command{ID: api.NewID(), Target: t1, Body: api.MessageDispatch{Text: "queued", Mode: api.AfterActive}}, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Observe(ctx, t1, engine.Started{Run: r1}, now.Add(2*time.Second), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Observe(ctx, t1, engine.Surfaced{Run: r1, Request: api.NewID(), Dedup: "d1", Kind: api.KindApproval, Body: []byte(`{"tool":"edit"}`)}, now.Add(3*time.Second), 0); err != nil {
		t.Fatal(err)
	}

	// Thread 2: child of t1, finished run.
	t2 := api.NewID()
	if _, err := s.Submit(ctx, api.Command{ID: api.NewID(), Target: t2, Body: api.ThreadCreate{
		Workspace: "w", Title: "child", Model: api.ModelRef{Provider: "fake"}, Parent: t1, Run: r1, Text: "sub",
	}}, now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Observe(ctx, t2, engine.Started{Run: t2}, now.Add(5*time.Second), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Observe(ctx, t2, engine.Ended{Run: t2, Outcome: api.OutcomeCompleted}, now.Add(6*time.Second), 0); err != nil {
		t.Fatal(err)
	}

	// Thread 3: deleted thread (must be excluded from bulk Read).
	t3 := api.NewID()
	if _, err := s.Submit(ctx, create(t3), now.Add(7*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Submit(ctx, api.Command{ID: api.NewID(), Target: t3, Body: api.ThreadDelete{}}, now.Add(8*time.Second)); err != nil {
		t.Fatal(err)
	}

	bulk, err := s.Read(ctx, api.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(bulk.Threads) != 2 {
		t.Fatalf("bulk returned %d threads, want 2", len(bulk.Threads))
	}
	for i, th := range bulk.Threads {
		single, err := s.Read(ctx, api.Query{Thread: th.ID})
		if err != nil || len(single.Threads) != 1 {
			t.Fatalf("single read %s: %v", th.ID, err)
		}
		if !reflect.DeepEqual(th, single.Threads[0]) {
			t.Errorf("thread %d mismatch:\nbulk:   %+v\nsingle: %+v", i, th, single.Threads[0])
		}
	}
}

func TestMaintainPrunesDoneEffectsOldCommandsAndCheckpointsWAL(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "host.db")
	s := open(t, path)

	for i := range 6 {
		th := api.NewID()
		if _, err := s.Submit(ctx, create(th), time.Now()); err != nil {
			t.Fatal(err)
		}
		msg := api.NewID()
		if _, err := s.Submit(ctx, api.Command{ID: msg, Target: th, Body: api.MessageDispatch{Text: fmt.Sprintf("m%d", i), Mode: api.StartNow}}, time.Now()); err != nil {
			t.Fatal(err)
		}
		e, ok, err := s.Lease(ctx, nil)
		if err != nil || !ok {
			t.Fatalf("lease %d: %v %v", i, ok, err)
		}
		if i < 4 {
			if err := s.Complete(ctx, e.ID); err != nil {
				t.Fatal(err)
			}
		} else if i == 4 {
			if err := s.Release(ctx, e.ID); err != nil {
				t.Fatal(err)
			}
		}
		// i == 5 stays leased.
	}

	res, err := s.Maintain(ctx, store.MaintainOptions{KeepCommands: 5, Batch: 2})
	if err != nil {
		t.Fatal(err)
	}
	if res.PrunedEffects != 4 || res.PrunedCommands != 7 {
		t.Fatalf("first Maintain = %+v, want PrunedEffects=4 PrunedCommands=7, in batches of 2", res)
	}

	res2, err := s.Maintain(ctx, store.MaintainOptions{KeepCommands: 5, Batch: 10})
	if err != nil {
		t.Fatal(err)
	}
	if res2.PrunedEffects != 0 || res2.PrunedCommands != 0 {
		t.Fatalf("second Maintain = %+v, want nothing left to prune", res2)
	}

	// Pending and leased effects remain untouched.
	leased, err := s.Leased(ctx)
	if err != nil || len(leased) != 1 {
		t.Fatalf("leased after Maintain: %+v (%v), want 1", leased, err)
	}
	if _, ok, err := s.Lease(ctx, nil); err != nil || !ok {
		t.Fatalf("pending effect after Maintain: ok=%v err=%v", ok, err)
	}
}

// seedReceipts writes n receipts, under the IDs id gives, straight into the
// commands table: the receipts a long-lived engine holds.
func seedReceipts(t *testing.T, path string, n int, id func(i int) api.ID) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for i := range n {
		c := id(i)
		if _, err := tx.Exec(`INSERT INTO commands (id, hash, seq, revision) VALUES (?, zeroblob(32), 1, NULL)`, c[:]); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// hashID is an ID as MCP delegation and linked machines derive one: a hash,
// whose order says nothing about when it was made.
func hashID(i int) api.ID {
	sum := sha256.Sum256(fmt.Appendf(nil, "parent/delegate/child/%d", i))
	return api.ID(sum[:16])
}

// renamed is how far thread's log reached after a rename to title under id.
func renamed(t *testing.T, s *store.Store, thread, id api.ID, title string) {
	t.Helper()
	ctx := context.Background()
	_, before, err := s.Since(ctx, thread, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Submit(ctx, api.Command{ID: id, Target: thread, Body: api.ThreadRename{Title: title}}, time.Now()); err != nil {
		t.Fatalf("rename to %q: %v", title, err)
	}
	if _, after, err := s.Since(ctx, thread, 0, 1000); err != nil || after <= before {
		t.Fatalf("rename to %q wrote no event: head %d, was %d (%v)", title, after, before, err)
	}
}

// Receipts under hash-derived IDs outnumber what any retention would keep:
// after Maintain, a GUI's fresh command and a fresh hash-derived one both
// run, and every receipt still answers its retry.
func TestMaintainLetsAFreshCommandRunAmongHashOrderedReceipts(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "host.db")
	s := open(t, path)
	thread := api.NewID()
	if _, err := s.Submit(ctx, create(thread), time.Now()); err != nil {
		t.Fatal(err)
	}
	const seeded = 10200
	seedReceipts(t, path, seeded, hashID)
	if _, err := s.Maintain(ctx, store.MaintainOptions{}); err != nil {
		t.Fatal(err)
	}
	renamed(t, s, thread, api.NewID(), "from the GUI")
	renamed(t, s, thread, api.ID{}, "a hash below every other")
	for _, i := range []int{0, seeded / 2, seeded - 1} {
		if done, err := s.Committed(ctx, hashID(i)); err != nil || !done {
			t.Errorf("receipt %d after Maintain: Committed = %v, %v; want true", i, done, err)
		}
	}
}

// A command minted long before it reaches the engine, as a sleeping GUI's
// or a plugin's with an older At, runs the first time it arrives.
func TestMaintainLetsALateFirstSubmissionRun(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "host.db")
	s := open(t, path)
	thread := api.NewID()
	if _, err := s.Submit(ctx, create(thread), time.Now()); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	seedReceipts(t, path, 10200, func(i int) api.ID {
		id, err := api.IDAt(now.Add(time.Duration(i)*time.Millisecond), [10]byte{byte(i), byte(i >> 8)})
		if err != nil {
			t.Fatal(err)
		}
		return id
	})
	if _, err := s.Maintain(ctx, store.MaintainOptions{}); err != nil {
		t.Fatal(err)
	}
	late, err := api.IDAt(now.Add(-time.Hour), [10]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	renamed(t, s, thread, late, "minted an hour ago")
}

// A retried command gets its receipt back after Maintain and is not
// applied again.
func TestMaintainKeepsARecentCommandsReceipt(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "host.db")
	s := open(t, path)
	thread := api.NewID()
	if _, err := s.Submit(ctx, create(thread), time.Now()); err != nil {
		t.Fatal(err)
	}
	seedReceipts(t, path, 10200, hashID)
	msg := api.Command{ID: api.NewID(), Target: thread, Body: api.MessageDispatch{Text: "once", Mode: api.AfterActive}}
	first, err := s.Submit(ctx, msg, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	_, before, err := s.Since(ctx, thread, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Maintain(ctx, store.MaintainOptions{}); err != nil {
		t.Fatal(err)
	}
	again, err := s.Submit(ctx, msg, time.Now())
	if err != nil || !reflect.DeepEqual(again, first) {
		t.Fatalf("the retry after Maintain: %+v, %v; want %+v", again, err, first)
	}
	if _, after, err := s.Since(ctx, thread, 0, 1000); err != nil || after != before {
		t.Fatalf("the retry wrote events: head %d, want %d (%v)", after, before, err)
	}
	if done, err := s.Committed(ctx, msg.ID); err != nil || !done {
		t.Fatalf("Committed after Maintain = %v, %v; want true", done, err)
	}
}

// A database an earlier build of this change left at version 18, with an
// index on commands.seq and a pruned-receipt watermark, loses both, and a
// command the watermark covered runs.
func TestAVersion18DatabaseLosesTheReceiptPruningLeftovers(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "host.db")
	s := open(t, path)
	thread := api.NewID()
	if _, err := s.Submit(ctx, create(thread), time.Now()); err != nil {
		t.Fatal(err)
	}
	s.Close()
	downgrade(t, path, `
		CREATE INDEX commands_by_seq ON commands (seq);
		INSERT INTO meta (key, value) VALUES ('commands_pruned', x'ffffffffffffffffffffffffffffffff');
	`, 18)

	s = open(t, path)
	renamed(t, s, thread, api.NewID(), "after the upgrade")
	s.Close()

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version, index, mark int
	if err := db.QueryRow(`SELECT (SELECT user_version FROM pragma_user_version),
		(SELECT COUNT(*) FROM sqlite_master WHERE name = 'commands_by_seq'),
		(SELECT COUNT(*) FROM meta WHERE key = 'commands_pruned')`).Scan(&version, &index, &mark); err != nil {
		t.Fatal(err)
	}
	if version != 19 || index != 0 || mark != 0 {
		t.Fatalf("after the upgrade: user_version %d, commands_by_seq %d, watermark %d; want 19, 0, 0", version, index, mark)
	}
}

// The list of threads reads what each thread's view shows, not the text
// of every message its threads were ever sent.
func TestReadingTheThreadListSkipsMessageText(t *testing.T) {
	ctx := context.Background()
	s := open(t, filepath.Join(t.TempDir(), "host.db"))
	text := strings.Repeat("x", 1<<20)
	const threads, messages = 20, 2
	for range threads {
		th := api.NewID()
		if _, err := s.Submit(ctx, create(th), time.Now()); err != nil {
			t.Fatal(err)
		}
		for range messages {
			if _, err := s.Submit(ctx, api.Command{ID: api.NewID(), Target: th, Body: api.MessageDispatch{Text: text, Mode: api.AfterActive}}, time.Now()); err != nil {
				t.Fatal(err)
			}
		}
	}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	snap, err := s.Read(ctx, api.Query{})
	runtime.ReadMemStats(&after)
	if err != nil || len(snap.Threads) != threads {
		t.Fatalf("Read: %d threads, %v", len(snap.Threads), err)
	}
	const sent = threads * messages << 20
	if got := after.TotalAlloc - before.TotalAlloc; got > sent/8 {
		t.Fatalf("reading the thread list allocated %d MiB, with %d MiB of message text stored", got>>20, sent>>20)
	}
}
