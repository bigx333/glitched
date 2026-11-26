package client

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/store"
)

type searchTestEngine struct {
	api.Endpoint
	s *store.Store
}

func (e searchTestEngine) Search(ctx context.Context, q api.SearchQuery) (api.SearchResult, error) {
	return e.s.Search(ctx, q)
}

// Across two engines, tied BM25 scores break ties consistently by thread
// UpdatedAt so a smaller limit still returns the top-ranked thread.
func TestSearchMergeConsistentAcrossLimits(t *testing.T) {
	m := New(Config{Post: func(f func()) { f() }})
	t.Cleanup(m.cancel)
	stores := make([]*store.Store, 2)
	for i := range stores {
		s, err := store.Open(filepath.Join(t.TempDir(), "engine.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		stores[i] = s
		m.envs = append(m.envs, &Env{Status: Online{}, Kind: Remote, session: &session{ctx: m.ctx, ep: searchTestEngine{s: s}}})
	}
	submit := func(s *store.Store, id api.ID, secs int64, body api.Intent) {
		t.Helper()
		if _, err := s.Submit(m.ctx, api.Command{ID: api.NewID(), Target: id, Body: body}, time.Unix(secs, 0)); err != nil {
			t.Fatal(err)
		}
	}
	add := func(engine int, title string, secs int64) api.ID {
		id := api.NewID()
		submit(stores[engine], id, secs, api.ThreadCreate{Workspace: "w", Title: title, Model: api.ModelRef{Provider: "fake"}})
		submit(stores[engine], id, secs+1, api.MessageDispatch{Text: "needle", Mode: api.Defer})
		return id
	}
	a := add(0, "A", 10)
	add(0, "B", 20)
	add(1, "C", 30)
	submit(stores[0], a, 50, api.ThreadRename{Title: "A updated"})

	fetch := func(limit int) []SearchMatch {
		ch := make(chan []SearchMatch, 1)
		m.Search("needle", limit, func(ms []SearchMatch) { ch <- ms })
		return <-ch
	}
	full, one := fetch(3), fetch(1)
	if len(full) != 3 || len(one) != 1 {
		t.Fatalf("unexpected counts: %d, %d", len(full), len(one))
	}
	if one[0].Hit.Thread != full[0].Hit.Thread {
		t.Fatalf("two engines: limit 1 returns %q; limit 3 ranks %q first", one[0].Hit.Title, full[0].Hit.Title)
	}
}
