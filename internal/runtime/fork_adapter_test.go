package runtime_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
	"glitched/internal/store"
)

type forkAdapter struct {
	*fake.Adapter
	rewind provider.ConversationFork
	err    error
	native bool

	mu     sync.Mutex
	specs  []provider.ForkSpec
	opened []provider.SessionSpec
	sent   []string
	// failStarts is how many of the next starts fail before they send.
	failStarts int
}

func newForkAdapter(rewind provider.ConversationFork, err error, native bool) *forkAdapter {
	return &forkAdapter{Adapter: fake.New(fake.Turn(false)), rewind: rewind, err: err, native: native}
}

func (r *forkAdapter) ForkConversation(_ context.Context, spec provider.ForkSpec) (provider.ConversationFork, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.specs = append(r.specs, spec)
	return r.rewind, r.err
}

func (r *forkAdapter) Open(ctx context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	ag, err := r.Adapter.Open(ctx, spec)
	r.mu.Lock()
	r.opened = append(r.opened, spec)
	r.mu.Unlock()
	return &forkAgent{Agent: ag, r: r, run: spec.Run}, err
}

func (r *forkAdapter) seen() ([]provider.ForkSpec, []provider.SessionSpec, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.specs), slices.Clone(r.opened), slices.Clone(r.sent)
}

type forkAgent struct {
	provider.Agent
	r   *forkAdapter
	run api.ID
}

func (g *forkAgent) Do(ctx context.Context, a provider.Action) error {
	if s, ok := a.(provider.Start); ok {
		g.r.mu.Lock()
		defer g.r.mu.Unlock()
		if g.r.failStarts > 0 {
			g.r.failStarts--
			return errors.New("the start failed")
		}
		g.r.sent = append(g.r.sent, s.Text)
	}
	return g.Agent.Do(ctx, a)
}

func (g *forkAgent) Next(ctx context.Context) (provider.Event, error) {
	e, err := g.Agent.Next(ctx)
	if end, ok := e.(provider.TurnEnded); ok {
		end.Head = "head-" + g.run.String()
		e = end
	}
	return e, err
}

func (g *forkAgent) Native() bool { return g.r.native }

func sessionOf(t *testing.T, st *store.Store, thread api.ID) string {
	t.Helper()
	th, err := st.Thread(ctx, thread)
	if err != nil {
		t.Fatal(err)
	}
	return th.Session
}

func boundOf(t *testing.T, ep api.Endpoint, thread api.ID) []api.SessionBound {
	t.Helper()
	var out []api.SessionBound
	for _, e := range events(t, ep, thread) {
		if b, _ := e.Decode(); b != nil {
			if s, ok := b.(api.SessionBound); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

// runs is the thread's runs, oldest first, by their turn.ended.
func runsOf(t *testing.T, ep api.Endpoint, thread api.ID) []api.TurnEnded {
	t.Helper()
	var out []api.TurnEnded
	for _, e := range events(t, ep, thread) {
		if b, _ := e.Decode(); b != nil {
			if r, ok := b.(api.TurnEnded); ok {
				out = append(out, r)
			}
		}
	}
	return out
}
