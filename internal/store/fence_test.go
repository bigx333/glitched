package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"glitched/internal/api"
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
