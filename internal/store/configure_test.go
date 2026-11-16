package store_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
	"glitched/internal/store"
)

func configure(s *store.Store, thread api.ID, m api.ModelRef, a api.Access) error {
	_, err := s.Submit(context.Background(), api.Command{ID: api.NewID(), Target: thread, Body: api.ThreadConfigure{Model: m, Access: a}}, time.Now())
	return err
}

func threadOf(t *testing.T, s *store.Store, id api.ID) engine.Thread {
	t.Helper()
	th, err := s.Thread(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return th
}

var sonnet = api.ModelRef{Provider: "fake", Model: "Claude Sonnet 5.5", Effort: "medium"}

// A thread's model and access, as configured, outlive a reopen.
func TestAConfigureIsKept(t *testing.T) {
	path := filepath.Join(t.TempDir(), "host.db")
	s := open(t, path)
	thread := api.NewID()
	submitTo(t, s, create(thread))
	if err := configure(s, thread, sonnet, api.AccessFull); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s = open(t, path)
	if th := threadOf(t, s, thread); th.Model != sonnet || th.Access != api.AccessFull {
		t.Fatalf("after reopen: %+v at %v", th.Model, th.Access)
	}
}

// A child's access can't be configured wider than its parent's, and
// narrowing a thread narrows the threads under it, their models kept.
func TestAConfigureKeepsChildrenWithinTheirParent(t *testing.T) {
	s := open(t, filepath.Join(t.TempDir(), "host.db"))
	tr := newTree(t, s)
	var re *api.RejectedError
	if err := configure(s, tr.ids[1], api.ModelRef{Provider: "fake", Model: "m"}, api.AccessFull); !errors.As(err, &re) {
		t.Fatalf("a child wider than its parent: %v, not refused", err)
	}
	for _, id := range tr.ids {
		if err := configure(s, id, sonnet, api.AccessFull); err != nil {
			t.Fatal(err)
		}
	}
	if err := configure(s, tr.ids[0], api.ModelRef{Provider: "fake", Model: "top"}, api.AccessAutoEdits); err != nil {
		t.Fatal(err)
	}
	for i, id := range tr.ids[1:] {
		if th := threadOf(t, s, id); th.Access != api.AccessAutoEdits || th.Model != sonnet {
			t.Errorf("thread %d under the narrowed one: %+v at %v", i+1, th.Model, th.Access)
		}
	}
	// Widening the top widens none under it.
	if err := configure(s, tr.ids[0], sonnet, api.AccessFull); err != nil {
		t.Fatal(err)
	}
	if th := threadOf(t, s, tr.ids[1]); th.Access != api.AccessAutoEdits {
		t.Errorf("a child widened with its parent, to %v", th.Access)
	}
}

// A run keeps the model and access it was admitted at across a reopen:
// its thread widened since, its view still says them, and it starts a
// child as wide as itself and none wider.
func TestARunsModelAndAccessAreKept(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "host.db")
	s := open(t, path)
	thread, run := api.NewID(), api.NewID()
	opus := api.ModelRef{Provider: "fake", Model: "Claude Opus 5.5", Effort: "high", Fast: true}
	submitTo(t, s, api.Command{ID: api.NewID(), Target: thread, Body: api.ThreadCreate{Workspace: "w", Title: "t", Model: opus, Access: api.AccessAutoEdits}})
	submitTo(t, s, api.Command{ID: run, Target: thread, Body: api.MessageDispatch{Text: "go", Mode: api.StartNow}})
	if err := configure(s, thread, sonnet, api.AccessFull); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s = open(t, path)
	snap, err := s.Read(ctx, api.Query{Thread: thread})
	if err != nil {
		t.Fatal(err)
	}
	if v, want := snap.Threads[0], (api.Turn{Run: run, Model: opus, Access: api.AccessAutoEdits}); v.Turn == nil || *v.Turn != want {
		t.Errorf("after reopen the turn is %+v, want %+v as admitted", v.Turn, want)
	}
	child := func(a api.Access) error {
		_, err := s.Submit(ctx, api.Command{ID: api.NewID(), Target: api.NewID(), Body: api.ThreadCreate{
			Workspace: "w", Title: "child", Model: opus, Access: a, Parent: thread, Run: run, Text: "work",
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
}
