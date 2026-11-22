package runtime_test

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
)

// binding plays a provider that names the thread's session itself: each
// agent it opens is bound to id, and it keeps the session each Open was
// given.
type binding struct {
	*fake.Adapter
	id   string
	mu   sync.Mutex
	seen []string
}

type bound struct {
	provider.Agent
	id string
}

func (b bound) Session() string { return b.id }

func (b *binding) Open(ctx context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	b.mu.Lock()
	b.seen = append(b.seen, spec.Session)
	b.mu.Unlock()
	ag, err := b.Adapter.Open(ctx, spec)
	if err != nil {
		return nil, err
	}
	return bound{ag, b.id}, nil
}

func (b *binding) sessions() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.seen)
}

// The session a run's agent binds is stored before its prompt goes out:
// an engine that dies while the prompt is being written still has it,
// and the thread's next run, on the next engine, is given it.
func TestASessionBoundBeforeACrashIsResumed(t *testing.T) {
	path := dbPath(t)
	held := fake.New(fake.Turn(false))
	held.HoldStart = true
	first := &binding{Adapter: held, id: "sess-1"}
	n := start(t, path, first)
	thread := newThread(t, n.h)
	submit(t, n.h, thread, dispatch("go", api.StartNow))
	deadline := time.Now().Add(5 * time.Second)
	for held.Starts() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the run's start never began")
		}
		time.Sleep(time.Millisecond)
	}
	n.kill()

	second := &binding{Adapter: fake.New(fake.Turn(false)), id: "sess-1"}
	n = start(t, path, second)
	until(t, n.h, thread, "uncertain", func(th api.Thread) bool { return th.Run == api.RunUncertain })
	submit(t, n.h, thread, api.TurnInterrupt{})
	until(t, n.h, thread, "idle", idle)
	submit(t, n.h, thread, dispatch("again", api.StartNow))
	until(t, n.h, thread, "the next run", func(th api.Thread) bool { return idle(th) && len(second.sessions()) == 1 })
	if got := first.sessions(); !slices.Equal(got, []string{""}) {
		t.Errorf("the thread's first run was given sessions %q, want none", got)
	}
	if got := second.sessions(); !slices.Equal(got, []string{"sess-1"}) {
		t.Errorf("the run after the crash was given %q, want sess-1", got)
	}
	if c := count(events(t, n.h, thread), "session.bound"); c != 1 {
		t.Errorf("%d session.bound events, want 1: binding the same session again logs nothing", c)
	}
	assertReplay(t, n.st)
}
