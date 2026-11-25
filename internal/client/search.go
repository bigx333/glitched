package client

import (
	"cmp"
	"context"
	"slices"
	"sync"
	"time"

	"glitched/internal/api"
)

// SearchMatch pairs a full-text transcript hit with the environment it came from.
type SearchMatch struct {
	Env *Env
	Hit api.SearchHit
}

type searcher interface {
	Search(ctx context.Context, q api.SearchQuery) (api.SearchResult, error)
}

const searchTimeout = 5 * time.Second

// Search queries all online environments' full-text transcript indexes in
// parallel and posts the merged results (newest thread updatedAt first)
// back onto the UI thread via done.
func (m *Manager) Search(query string, limit int, done func([]SearchMatch)) {
	if m.closed {
		return
	}
	type target struct {
		env *Env
		ctx context.Context
		fn  func(context.Context, api.SearchQuery) (api.SearchResult, error)
	}
	var targets []target
	for _, e := range m.envs {
		if e.Status != Status(Online{}) {
			continue
		}
		if e.Kind == Local && m.embedded != nil {
			targets = append(targets, target{env: e, ctx: e.ctx, fn: m.embedded.Search})
			continue
		}
		if e.session != nil {
			if sr, ok := e.session.ep.(searcher); ok {
				targets = append(targets, target{env: e, ctx: e.session.ctx, fn: sr.Search})
			}
		}
	}
	if len(targets) == 0 {
		done(nil)
		return
	}
	q := api.SearchQuery{Query: query, Limit: limit}
	m.wg.Go(func() {
		var mu sync.Mutex
		var wg sync.WaitGroup
		var merged []SearchMatch
		for _, tg := range targets {
			wg.Add(1)
			go func(tg target) {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(tg.ctx, searchTimeout)
				defer cancel()
				res, err := tg.fn(ctx, q)
				if err != nil {
					return
				}
				mu.Lock()
				for _, h := range res.Hits {
					merged = append(merged, SearchMatch{Env: tg.env, Hit: h})
				}
				mu.Unlock()
			}(tg)
		}
		wg.Wait()
		slices.SortFunc(merged, func(a, b SearchMatch) int {
			if c := b.Hit.UpdatedAt.Compare(a.Hit.UpdatedAt); c != 0 {
				return c
			}
			return cmp.Compare(b.Hit.Seq, a.Hit.Seq)
		})
		if limit > 0 && len(merged) > limit {
			merged = merged[:limit]
		}
		m.post(m.ctx, func() { done(merged) })
	})
}
