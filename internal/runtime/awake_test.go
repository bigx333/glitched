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
	"glitched/internal/runtime"
	"glitched/internal/store"
)

// heard is what a host told WithAwake, in order.
type heard struct {
	mu   sync.Mutex
	told []bool
}

func (h *heard) tell(working bool) {
	h.mu.Lock()
	h.told = append(h.told, working)
	h.mu.Unlock()
}

func (h *heard) all() []bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.told)
}

func awakeHost(t *testing.T, path string, ad provider.Adapter, h *heard) node {
	t.Helper()
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	host, err := runtime.New(ctx, st, provider.Mux{"fake": ad}, runtime.WithAwake(h.tell))
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	n := node{st, host}
	t.Cleanup(n.kill)
	return n
}

// A host says an agent works while a run starts, runs or stops, and not
// while it waits on the user's answer or once it ends.
func TestAwakeHearsWhetherAnAgentWorks(t *testing.T) {
	h := &heard{}
	n := awakeHost(t, dbPath(t), fake.New(fake.Turn(true)), h)
	thread := newThread(t, n.h)
	submit(t, n.h, thread, dispatch("go", api.StartNow))
	req := until(t, n.h, thread, "approval", waiting).Requests[0]
	if got := h.all(); !slices.Equal(got, []bool{true, false}) {
		t.Fatalf("told %v once the run came to wait, want [true false]", got)
	}
	respond(t, n.h, thread, req)
	until(t, n.h, thread, "turn end", idle)
	if got := h.all(); !slices.Equal(got, []bool{true, false, true, false}) {
		t.Fatalf("told %v once the answered run ended, want [true false true false]", got)
	}
}

// Two threads at work together are one stretch of work: the host says
// none works only once both stop.
func TestAwakeSpansEveryThread(t *testing.T) {
	h := &heard{}
	held := fake.New(fake.Turn(false))
	held.HoldStart = true
	n := awakeHost(t, dbPath(t), held, h)
	one, two := newThread(t, n.h), newThread(t, n.h)
	submit(t, n.h, one, dispatch("go", api.StartNow))
	submit(t, n.h, two, dispatch("go", api.StartNow))
	until(t, n.h, two, "both starting", func(api.Thread) bool { return held.Starts() == 2 })
	submit(t, n.h, one, api.TurnInterrupt{})
	until(t, n.h, one, "the first stopped", idle)
	if got := h.all(); !slices.Equal(got, []bool{true}) {
		t.Fatalf("told %v with the second still starting, want [true]", got)
	}
	submit(t, n.h, two, api.TurnInterrupt{})
	until(t, n.h, two, "the second stopped", idle)
	if got := h.all(); !slices.Equal(got, []bool{true, false}) {
		t.Fatalf("told %v once both stopped, want [true false]", got)
	}
}

// A run a restarted host takes back while it waits on the user works no
// more than before the restart, and works again once answered.
func TestAwakeAfterARestart(t *testing.T) {
	path, ad := dbPath(t), fake.New(fake.Turn(true))
	n := start(t, path, ad)
	thread := newThread(t, n.h)
	submit(t, n.h, thread, dispatch("go", api.StartNow))
	until(t, n.h, thread, "approval", waiting)
	n.kill()

	h := &heard{}
	n = awakeHost(t, path, ad, h)
	req := until(t, n.h, thread, "still waiting", waiting).Requests[0]
	if got := h.all(); len(got) != 0 {
		t.Fatalf("told %v for a run that waits on the user", got)
	}
	respond(t, n.h, thread, req)
	until(t, n.h, thread, "turn end", idle)
	if got := h.all(); !slices.Equal(got, []bool{true, false}) {
		t.Fatalf("told %v once answered, want [true false]", got)
	}
}

// lingering opens the fake's agents to play the first two events of a
// turn and work on, and to close only once release closes; closing closes
// as the first close begins.
type lingering struct {
	*fake.Adapter
	closing, release chan struct{}
	once             sync.Once
}

func (s *lingering) Open(ctx context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	g, err := s.Adapter.Open(ctx, spec)
	return &lingeringAgent{Agent: g, s: s, left: 2}, err
}

type lingeringAgent struct {
	provider.Agent
	s    *lingering
	left int
}

func (g *lingeringAgent) Next(ctx context.Context) (provider.Event, error) {
	if g.left == 0 {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	g.left--
	return g.Agent.Next(ctx)
}

func (g *lingeringAgent) Close(ctx context.Context) error {
	g.s.once.Do(func() { close(g.s.closing) })
	select {
	case <-g.s.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return g.Agent.Close(ctx)
}

// An agent whose thread is deleted as it works counts as working until it
// closes, as it may work on until then.
func TestAwakeUntilADeletedAgentCloses(t *testing.T) {
	h := &heard{}
	ad := &lingering{Adapter: fake.New(fake.Turn(false)), closing: make(chan struct{}), release: make(chan struct{})}
	n := awakeHost(t, dbPath(t), ad, h)
	thread := newThread(t, n.h)
	submit(t, n.h, thread, dispatch("go", api.StartNow))
	until(t, n.h, thread, "running", func(th api.Thread) bool { return th.Run == api.RunRunning })
	submit(t, n.h, thread, api.ThreadDelete{})
	<-ad.closing
	if got := h.all(); !slices.Equal(got, []bool{true}) {
		t.Fatalf("told %v with the deleted thread's agent still closing, want [true]", got)
	}
	close(ad.release)
	for deadline := time.Now().Add(10 * time.Second); !slices.Equal(h.all(), []bool{true, false}); {
		if time.Now().After(deadline) {
			t.Fatalf("told %v once the agent closed, want [true false]", h.all())
		}
		time.Sleep(5 * time.Millisecond)
	}
}
