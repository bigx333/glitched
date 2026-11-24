package runtime_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
	"glitched/internal/runtime"
	"glitched/internal/store"
)

type refusingReconcile struct {
	*fake.Adapter
	refuse bool
}

type gatedOpen struct {
	*fake.Adapter
	opening chan struct{}
	release chan struct{}
}

func (a gatedOpen) Open(ctx context.Context, s provider.SessionSpec) (provider.Agent, error) {
	if s.Generation == 2 {
		close(a.opening)
		select {
		case <-a.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return a.Adapter.Open(ctx, s)
}

// A thread's agent keeps its skills while another opens in its place,
// whatever another thread's run sweeps meanwhile.
func TestAnAgentKeepsItsSkillsTillItsReplacementOpens(t *testing.T) {
	state, wsA, wsB := t.TempDir(), t.TempDir(), t.TempDir()
	skillAt(t, filepath.Join(wsA, ".glitched", "skills"), "first")
	skillAt(t, filepath.Join(wsB, ".glitched", "skills"), "other")
	s := newSkilled()
	a := gatedOpen{s.Adapter, make(chan struct{}), make(chan struct{})}
	st, err := store.Open(dbPath(t))
	if err != nil {
		t.Fatal(err)
	}
	h, err := runtime.New(ctx, st, provider.Mux{"fake": a}, runtime.WithSkills(state))
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close(); st.Close() })
	defer close(a.release)
	newThread := func(ws string) api.ID {
		id := api.NewID()
		submit(t, h, id, api.ThreadCreate{Workspace: ws, Title: "t", Model: api.ModelRef{Provider: "fake", Model: "fake"}})
		return id
	}
	first, second := newThread(wsA), newThread(wsB)
	submit(t, h, first, dispatch("go", api.StartNow))
	until(t, h, first, "first turn", idle)
	previous := s.last().List[0].Path
	skillAt(t, filepath.Join(wsA, ".glitched", "skills"), "added")
	submit(t, h, first, dispatch("continue", api.StartNow))
	select {
	case <-a.opening:
	case <-time.After(5 * time.Second):
		t.Fatal("replacement did not reach Open")
	}
	if _, err := os.Stat(previous); err != nil {
		t.Fatalf("old skill missing before the other open: %v", err)
	}
	submit(t, h, second, dispatch("go", api.StartNow))
	until(t, h, second, "other turn", idle)
	if _, err := os.Stat(previous); err != nil {
		t.Fatalf("another thread's open removed the old SKILL.md before replacement finished: %v", err)
	}
}

func (a refusingReconcile) Reconcile(ctx context.Context, id provider.RuntimeIdentity) (provider.Recovery, error) {
	if a.refuse {
		return nil, errors.New("agent is still running")
	}
	return a.Adapter.Reconcile(ctx, id)
}

// An agent the last engine started keeps its skills through a restart,
// restored or uncertain.
func TestAnAgentThatOutlivesItsEngineKeepsItsSkills(t *testing.T) {
	for _, tc := range []string{"restored", "failed-reconcile"} {
		t.Run(tc, func(t *testing.T) {
			state := t.TempDir()
			skillAt(t, filepath.Join(state, "skills"), "mine")
			a := fake.New(fake.Turn(true))
			specs := make(chan provider.SessionSpec, 1)
			a.Confirms = func(spec provider.SessionSpec) []provider.Event {
				specs <- spec
				return nil
			}
			path := dbPath(t)
			startHost := func(ad provider.Adapter) node {
				t.Helper()
				st, err := store.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				h, err := runtime.New(ctx, st, provider.Mux{"fake": ad}, runtime.WithSkills(state))
				if err != nil {
					st.Close()
					t.Fatal(err)
				}
				n := node{st, h}
				t.Cleanup(n.kill)
				return n
			}
			before := startHost(a)
			id := newThread(t, before.h)
			submit(t, before.h, id, dispatch("go", api.StartNow))
			until(t, before.h, id, "approval", waiting)
			spec := <-specs
			if len(spec.Skills.List) != 1 {
				t.Fatalf("live agent has %d skills", len(spec.Skills.List))
			}
			file := spec.Skills.List[0].Path
			if _, err := os.Stat(file); err != nil {
				t.Fatalf("before restart: %v", err)
			}
			before.kill()
			after := startHost(refusingReconcile{a, tc == "failed-reconcile"})
			if tc == "restored" {
				until(t, after.h, id, "restored approval", waiting)
			} else {
				until(t, after.h, id, "uncertain run", uncertain)
			}
			r, err := a.Reconcile(ctx, provider.RuntimeIdentity{Thread: id, Run: spec.Run})
			if _, ok := r.(provider.Restored); !ok || err != nil {
				t.Fatalf("fixture's agent is no longer live: %T, %v", r, err)
			}
			if _, err := os.Stat(file); err != nil {
				t.Fatalf("live agent lost its staged SKILL.md after restart: %v", err)
			}
		})
	}
}
