package store_test

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
)

// A fork's create resolves its origin in its own commit, from the source
// as it is then; the same command again gets the same receipt, and
// another body under its ID conflicts. The fork waits to take its source
// up until a run of its binds a session that says how it did.
func TestAForkCommitsItsOrigin(t *testing.T) {
	ctx := context.Background()
	s := open(t, filepath.Join(t.TempDir(), "host.db"))
	src, run := api.NewID(), api.NewID()
	fake := api.ModelRef{Provider: "fake", Model: "fake"}
	if _, err := s.Submit(ctx, api.Command{ID: run, Target: src, Body: api.ThreadCreate{Title: "the source", Model: fake, Text: "one"}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	fork := api.NewID()
	c := api.Command{ID: api.NewID(), Target: fork, Body: api.ThreadCreate{Title: "the fork", Model: fake, Text: "go on", Fork: &api.Fork{Thread: src, Run: run}}}
	var refused *api.RejectedError
	if _, err := s.Submit(ctx, c, time.Now()); !errors.As(err, &refused) || !strings.Contains(err.Error(), "only a finished turn") {
		t.Fatalf("a fork from the live run: %v", err)
	}
	for _, o := range []engine.Observation{engine.Started{Run: run}, engine.Ended{Run: run, Outcome: api.OutcomeCompleted}} {
		if _, err := s.Observe(ctx, src, o, time.Now(), 0); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.Submit(ctx, c, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if again, err := s.Submit(ctx, c, time.Now()); err != nil || again != first {
		t.Fatalf("the same fork again: %+v, %v; want %+v", again, err, first)
	}
	other := c
	other.Body = api.ThreadCreate{Title: "another", Model: fake, Fork: &api.Fork{Thread: src, Run: run}}
	if _, err := s.Submit(ctx, other, time.Now()); !errors.Is(err, api.ErrConflict) {
		t.Fatalf("another body under the fork's ID: %v", err)
	}
	want := &api.Forked{Thread: src, Run: run, Title: "the source", Provider: "fake", Runs: []api.ID{run}}
	snap, err := s.Read(ctx, api.Query{Thread: fork})
	if err != nil {
		t.Fatal(err)
	}
	if body, _ := snap.Events[0].Decode(); !reflect.DeepEqual(body.(api.ThreadCreated).Fork, want) {
		t.Fatalf("thread.created %+v; want the fork's origin %+v", body, want)
	}
	if snap.Threads[0].Turn == nil {
		t.Fatal("the fork's first message didn't start its turn in the create's commit")
	}
	if got, err := s.Forked(ctx, fork); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Forked = %+v, %v; want %+v", got, err, want)
	}
	if got, err := s.Forked(ctx, src); err != nil || got != nil {
		t.Fatalf("the source's Forked = %+v, %v", got, err)
	}
	if _, err := s.Observe(ctx, fork, engine.Bound{Run: c.ID, Session: "s"}, time.Now(), 0); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Forked(ctx, fork); got == nil {
		t.Fatal("a session that took nothing up ended the fork's wait")
	}
	if _, err := s.Observe(ctx, fork, engine.Bound{Run: c.ID, Session: "s", Rewound: api.RewindPortable}, time.Now(), 0); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Forked(ctx, fork); err != nil || got != nil {
		t.Fatalf("Forked once a run took it up = %+v, %v", got, err)
	}
	if _, err := s.Submit(ctx, api.Command{ID: api.NewID(), Target: api.NewID(), Body: api.ThreadCreate{Title: "x", Model: fake, Fork: &api.Fork{Thread: api.NewID(), Run: run}}}, time.Now()); err == nil || !strings.Contains(err.Error(), "gone") {
		t.Fatalf("a fork of no thread: %v", err)
	}
}
