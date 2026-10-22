package runtime_test

import (
	"context"
	"fmt"
	"sync"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
)

// gated wraps the fake so a test can hold provider calls: a call its gate
// answers with a channel waits for that channel or its context.
type gated struct {
	*fake.Adapter
	mu   sync.Mutex
	gate func(thread api.ID, a provider.Action) <-chan struct{}
	took map[string]int // calls the fake accepted, by action type
}

func newGated(script []provider.Event) *gated {
	return &gated{Adapter: fake.New(script), took: map[string]int{}}
}

func (g *gated) setGate(fn func(thread api.ID, a provider.Action) <-chan struct{}) {
	g.mu.Lock()
	g.gate = fn
	g.mu.Unlock()
}

// took reports how many calls of a's type reached the fake and succeeded.
func (g *gated) count(a provider.Action) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.took[fmt.Sprintf("%T", a)]
}

func (g *gated) Open(ctx context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	ag, err := g.Adapter.Open(ctx, spec)
	if err != nil {
		return nil, err
	}
	return &gatedAgent{ag, g, spec.Thread}, nil
}

func (g *gated) Reconcile(ctx context.Context, id provider.RuntimeIdentity) (provider.Recovery, error) {
	rec, err := g.Adapter.Reconcile(ctx, id)
	if r, ok := rec.(provider.Restored); ok {
		rec = provider.Restored{Agent: &gatedAgent{r.Agent, g, id.Thread}}
	}
	return rec, err
}

type gatedAgent struct {
	provider.Agent
	g      *gated
	thread api.ID
}

func (a *gatedAgent) Do(ctx context.Context, act provider.Action) error {
	a.g.mu.Lock()
	gate := a.g.gate
	a.g.mu.Unlock()
	if gate != nil {
		if wait := gate(a.thread, act); wait != nil {
			select {
			case <-wait:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	err := a.Agent.Do(ctx, act)
	if err == nil {
		a.g.mu.Lock()
		a.g.took[fmt.Sprintf("%T", act)]++
		a.g.mu.Unlock()
	}
	return err
}
