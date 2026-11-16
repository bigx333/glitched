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

func configure(s *store.Store, thread api.ID, c api.ThreadConfigure) error {
	_, err := s.Submit(context.Background(), api.Command{ID: api.NewID(), Target: thread, Body: c}, time.Now())
	return err
}

// whole is the configure that gives all a configure can: m's name, effort
// and fast mode, and a.
func whole(m api.ModelRef, a api.Access) api.ThreadConfigure {
	return api.ThreadConfigure{Model: new(m.Model), Effort: new(m.Effort), Fast: new(m.Fast), Access: new(a)}
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
	if err := configure(s, thread, whole(sonnet, api.AccessFull)); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s = open(t, path)
	if th := threadOf(t, s, thread); th.Model != sonnet || th.Access != api.AccessFull {
		t.Fatalf("after reopen: %+v at %v", th.Model, th.Access)
	}
}

// A configure gives only what was chosen, which the engine sets on what
// the thread has when it commits: one of the effort alone, sent by a
// client that last saw the thread at Full, leaves the access another
// narrowed since, on the thread and on those under it.
func TestAConfigureLeavesWhatAnotherChangedSince(t *testing.T) {
	s := open(t, filepath.Join(t.TempDir(), "host.db"))
	tr := newTree(t, s)
	for _, id := range tr.ids {
		if err := configure(s, id, whole(sonnet, api.AccessFull)); err != nil {
			t.Fatal(err)
		}
	}
	if err := configure(s, tr.ids[0], api.ThreadConfigure{Access: new(api.AccessAutoEdits)}); err != nil {
		t.Fatal(err)
	}
	low := sonnet
	low.Effort = "low"
	if err := configure(s, tr.ids[0], api.ThreadConfigure{Effort: new("low")}); err != nil {
		t.Fatal(err)
	}
	if th := threadOf(t, s, tr.ids[0]); th.Model != low || th.Access != api.AccessAutoEdits {
		t.Errorf("the thread is at %+v, %v; want the effort chosen at the access it was narrowed to", th.Model, th.Access)
	}
	for i, id := range tr.ids[1:] {
		if th := threadOf(t, s, id); th.Model != sonnet || th.Access != api.AccessAutoEdits {
			t.Errorf("thread %d under it is at %+v, %v; want its own model at the access it was narrowed to", i+1, th.Model, th.Access)
		}
	}
}

// A child's access can't be configured wider than its parent's, and
// narrowing a thread narrows the threads under it, their models kept. A
// configure that gives no access changes none under it.
func TestAConfigureKeepsChildrenWithinTheirParent(t *testing.T) {
	s := open(t, filepath.Join(t.TempDir(), "host.db"))
	tr := newTree(t, s)
	var re *api.RejectedError
	if err := configure(s, tr.ids[1], api.ThreadConfigure{Access: new(api.AccessFull)}); !errors.As(err, &re) {
		t.Fatalf("a child wider than its parent: %v, not refused", err)
	}
	for _, id := range tr.ids {
		if err := configure(s, id, whole(sonnet, api.AccessFull)); err != nil {
			t.Fatal(err)
		}
	}
	if err := configure(s, tr.ids[0], api.ThreadConfigure{Model: new("top"), Access: new(api.AccessAutoEdits)}); err != nil {
		t.Fatal(err)
	}
	for i, id := range tr.ids[1:] {
		if th := threadOf(t, s, id); th.Access != api.AccessAutoEdits || th.Model != sonnet {
			t.Errorf("thread %d under the narrowed one: %+v at %v", i+1, th.Model, th.Access)
		}
	}
	// Widening the top widens none under it.
	if err := configure(s, tr.ids[0], whole(sonnet, api.AccessFull)); err != nil {
		t.Fatal(err)
	}
	if th := threadOf(t, s, tr.ids[1]); th.Access != api.AccessAutoEdits {
		t.Errorf("a child widened with its parent, to %v", th.Access)
	}
	// The top's model, effort and fast mode are its own: the threads
	// under it aren't decided on.
	under := [2]engine.Thread{threadOf(t, s, tr.ids[1]), threadOf(t, s, tr.ids[2])}
	if err := configure(s, tr.ids[0], api.ThreadConfigure{Model: new("top"), Effort: new("low"), Fast: new(true)}); err != nil {
		t.Fatal(err)
	}
	for i, was := range under {
		if th := threadOf(t, s, was.ID); th.Model != was.Model || th.Access != was.Access || th.Revision != was.Revision {
			t.Errorf("thread %d under a thread given a model: %+v at %v, revision %d; want %+v at %v, revision %d",
				i+1, th.Model, th.Access, th.Revision, was.Model, was.Access, was.Revision)
		}
	}
	// A child takes a configure of its own that leaves it within its
	// parent, with or without an access.
	for _, tc := range []struct {
		name string
		c    api.ThreadConfigure
	}{
		{"its effort", api.ThreadConfigure{Effort: new("high")}},
		{"its parent's access", api.ThreadConfigure{Access: new(api.AccessFull)}},
		{"fast mode at a narrower access", api.ThreadConfigure{Fast: new(true), Access: new(api.AccessAsk)}},
		{"nothing", api.ThreadConfigure{}},
	} {
		if err := configure(s, tr.ids[1], tc.c); err != nil {
			t.Errorf("a child given %s: %v", tc.name, err)
		}
	}
	want := api.ModelRef{Provider: "fake", Model: sonnet.Model, Effort: "high", Fast: true}
	if th := threadOf(t, s, tr.ids[1]); th.Model != want || th.Access != api.AccessAsk {
		t.Errorf("the child is at %+v, %v; want %+v at Ask", th.Model, th.Access, want)
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
	if err := configure(s, thread, whole(sonnet, api.AccessFull)); err != nil {
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
