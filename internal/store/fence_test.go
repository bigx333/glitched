package store

import (
	"context"
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

// A write whose fence was synced but whose transaction never committed,
// because the engine was killed or closed between the two, loses only that
// write: nobody read its seqs, so the next open is no restore and the queue
// behind the live run stays as the user left it.
func TestAWriteLostBetweenItsFenceAndItsCommitIsNoRestore(t *testing.T) {
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
	submit := func(body api.Intent) api.ID {
		t.Helper()
		c := api.Command{ID: api.NewID(), Target: thread, Body: body}
		if _, err := s.Submit(ctx, c, time.Now()); err != nil {
			t.Fatal(err)
		}
		return c.ID
	}
	submit(api.ThreadCreate{Workspace: "w", Title: "t", Model: api.ModelRef{Provider: "fake"}})
	run := submit(api.MessageDispatch{Text: "go", Mode: api.StartNow})
	e, ok, err := s.Lease(ctx, nil)
	if err != nil || !ok {
		t.Fatalf("lease: %v %v", ok, err)
	}
	if _, err := s.Observe(ctx, thread, engine.Started{Run: run}, time.Now(), e.ID); err != nil {
		t.Fatal(err)
	}
	queued := submit(api.MessageDispatch{Text: "then this", Mode: api.AfterActive})
	log := s.LogID()
	evs, err := s.ReadLog(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	if err := s.writeFence(uint64(len(evs)) + 2); err != nil {
		t.Fatal(err)
	}
	s.mu.Unlock()
	s.Close()

	s = reopen()
	if s.LogID() != log {
		t.Fatalf("the open took the lost write for a restore and started log %s, want %s kept", s.LogID(), log)
	}
	th, err := s.Thread(ctx, thread)
	if err != nil {
		t.Fatal(err)
	}
	if m, _ := th.Message(queued); m.State != engine.QueueReady {
		t.Fatalf("the queued message is %s after the reopen, want it still waiting its turn", m.State)
	}
}
