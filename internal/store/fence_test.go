package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
)

func TestACommitTheFenceCannotCoverFails(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "host.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	thread := api.NewID()
	if _, err := s.Submit(ctx, api.Command{ID: api.NewID(), Target: thread, Body: api.ThreadCreate{}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	hooked := 0
	s.OnCommit(func(c Commit) { hooked += len(c.Events) })
	s.fence.Close()

	settle := api.Command{ID: api.NewID(), Target: thread, Body: api.InboxSet{Disposition: api.Disposition{State: api.Settled}}}
	r, err := s.Submit(ctx, settle, time.Now())
	if err == nil {
		t.Fatalf("commit returned cursor %+v that the fence never covered, so a restore below it would hand the seq out again", r.Committed)
	}
	if evs, _ := s.ReadLog(ctx, 0, 0); len(evs) != 1 || hooked != 0 {
		t.Fatalf("a failed commit left %d events readable and %d hooked, want 1 and 0", len(evs), hooked)
	}
}

// cancelling is a fence whose sync cancels the write it fences, as a host
// that closes then does.
type cancelling struct {
	fenceFile
	cancel context.CancelFunc
}

func (c cancelling) Sync() error {
	err := c.fenceFile.Sync()
	c.cancel()
	return err
}

// A write cancelled once its fence is synced still commits: rolled back,
// it would leave the fence ahead, and the next open would take that for a
// restore and cancel the queue behind the live run.
func TestAWriteCancelledAfterItsFenceStillCommits(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "host.db")
	reopen := func() *Store {
		t.Helper()
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		return s
	}
	s := reopen()
	thread := api.NewID()
	submit := func(ctx context.Context, body api.Intent) (api.ID, error) {
		c := api.Command{ID: api.NewID(), Target: thread, Body: body}
		_, err := s.Submit(ctx, c, time.Now())
		return c.ID, err
	}
	must := func(body api.Intent) api.ID {
		t.Helper()
		id, err := submit(ctx, body)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	must(api.ThreadCreate{Workspace: "w", Title: "t", Model: api.ModelRef{Provider: "fake"}})
	run := must(api.MessageDispatch{Text: "go", Mode: api.StartNow})
	e, ok, err := s.Lease(ctx, nil)
	if err != nil || !ok {
		t.Fatalf("lease: %v %v", ok, err)
	}
	if _, err := s.Observe(ctx, thread, engine.Started{Run: run}, time.Now(), e.ID); err != nil {
		t.Fatal(err)
	}
	queued := must(api.MessageDispatch{Text: "then this", Mode: api.AfterActive})
	log := s.LogID()

	closing, cancel := context.WithCancel(ctx)
	defer cancel()
	s.fence = cancelling{s.fence, cancel}
	last, cancelled := submit(closing, api.MessageDispatch{Text: "and this", Mode: api.AfterActive})
	s.Close()

	s = reopen()
	if s.LogID() != log {
		t.Fatalf("the open took the cancelled write for a restore and started log %s, want %s kept", s.LogID(), log)
	}
	th, err := s.Thread(ctx, thread)
	if err != nil {
		t.Fatal(err)
	}
	if m, _ := th.Message(queued); m.State != engine.QueueReady {
		t.Fatalf("the queued message is %s after the reopen, want it still waiting its turn", m.State)
	}
	if m, ok := th.Message(last); cancelled != nil || !ok || m.State != engine.QueueReady {
		t.Fatalf("the write cancelled after its fence returned %v and left its message %+v, want it committed and waiting", cancelled, m)
	}
}

// A write cancelled before its fence stops there, and doesn't wait out the
// write lock another connection holds, as a host that closes would.
func TestAWriteCancelledBeforeItsFenceStops(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "host.db")
	s, err := Open(path)
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
