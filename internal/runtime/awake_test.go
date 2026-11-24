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

// heard is what a host told WithAwake, in order, and each thread as the
// last commit its store's hooks ran for left it.
type heard struct {
	mu    sync.Mutex
	told  []bool
	views map[api.ID]api.Thread
	moved chan struct{} // closed by the next commit
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

func (h *heard) commit(c store.Commit) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.views == nil {
		h.views = map[api.ID]api.Thread{}
	}
	for _, th := range c.Threads {
		h.views[th.ID] = th
	}
	if h.moved != nil {
		close(h.moved)
		h.moved = nil
	}
}

// until waits for a commit to leave thread ok. The store runs its hooks in
// the order they were added, the host's first, so the host has told awake
// what that commit changed; a read of the store can see the commit before
// it has.
func (h *heard) until(t *testing.T, thread api.ID, what string, ok func(api.Thread) bool) api.Thread {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		h.mu.Lock()
		th, seen := h.views[thread]
		if seen && ok(th) {
			h.mu.Unlock()
			return th
		}
		if h.moved == nil {
			h.moved = make(chan struct{})
		}
		moved := h.moved
		h.mu.Unlock()
		select {
		case <-moved:
		case <-timeout:
			t.Fatalf("timed out waiting for %s: %+v", what, th)
		}
	}
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
	st.OnCommit(h.commit)
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
	req := h.until(t, thread, "approval", waiting).Requests[0]
	if got := h.all(); !slices.Equal(got, []bool{true, false}) {
		t.Fatalf("told %v once the run came to wait, want [true false]", got)
	}
	respond(t, n.h, thread, req)
	h.until(t, thread, "turn end", idle)
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
	h.until(t, one, "the first stopped", idle)
	if got := h.all(); !slices.Equal(got, []bool{true}) {
		t.Fatalf("told %v with the second still starting, want [true]", got)
	}
	submit(t, n.h, two, api.TurnInterrupt{})
	h.until(t, two, "the second stopped", idle)
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
	h.until(t, thread, "turn end", idle)
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
	plays            int           // events of a turn played before it works on; 0 is 2, -1 every one
	started          chan struct{} // when set, a Start takes effect once it closes, whatever its call's context
	opening          chan struct{} // closed as a Start held by started is called
	opened           sync.Once
}

func (s *lingering) Open(ctx context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	g, err := s.Adapter.Open(ctx, spec)
	left := s.plays
	if left == 0 {
		left = 2
	}
	return &lingeringAgent{Agent: g, s: s, left: left}, err
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
	if g.left > 0 {
		g.left--
	}
	return g.Agent.Next(ctx)
}

func (g *lingeringAgent) Do(ctx context.Context, act provider.Action) error {
	if _, start := act.(provider.Start); start && g.s.started != nil {
		g.s.opened.Do(func() { close(g.s.opening) })
		<-g.s.started
		ctx = context.Background()
	}
	return g.Agent.Do(ctx, act)
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
	toldAtLast(t, h, []bool{true, false})
}

// toldAtLast waits for h to have told want.
func toldAtLast(t *testing.T, h *heard, want []bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); !slices.Equal(h.all(), want); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("told %v, want %v", h.all(), want)
		}
	}
}

// An agent whose turn completed works no more, even as its close drags.
func TestAwakeNotForACompletedAgentClosing(t *testing.T) {
	h := &heard{}
	ad := &lingering{Adapter: fake.New(fake.Turn(false)), closing: make(chan struct{}), release: make(chan struct{}), plays: -1}
	n := awakeHost(t, dbPath(t), ad, h)
	t.Cleanup(func() { close(ad.release) })
	thread := newThread(t, n.h)
	submit(t, n.h, thread, dispatch("go", api.StartNow))
	until(t, n.h, thread, "turn end", idle)
	<-ad.closing
	toldAtLast(t, h, []bool{true, false})
}

// An agent whose thread is deleted as the provider opens it counts as
// working until the open gives up and the agent closes.
func TestAwakeUntilADeletedStartCloses(t *testing.T) {
	h := &heard{}
	held := fake.New(fake.Turn(false))
	held.HoldStart = true
	ad := &lingering{Adapter: held, closing: make(chan struct{}), release: make(chan struct{})}
	n := awakeHost(t, dbPath(t), ad, h)
	thread := newThread(t, n.h)
	submit(t, n.h, thread, dispatch("go", api.StartNow))
	until(t, n.h, thread, "the agent starting", func(api.Thread) bool { return held.Starts() == 1 })
	submit(t, n.h, thread, api.ThreadDelete{})
	<-ad.closing
	if got := h.all(); !slices.Equal(got, []bool{true}) {
		t.Fatalf("told %v with the deleted thread's agent still closing, want [true]", got)
	}
	close(ad.release)
	toldAtLast(t, h, []bool{true, false})
}

// An agent whose thread is deleted as the provider opens it, and which
// starts anyway, counts as working until it closes.
func TestAwakeUntilADeletedStartThatTookCloses(t *testing.T) {
	h := &heard{}
	ad := &lingering{Adapter: fake.New(fake.Turn(false)), closing: make(chan struct{}), release: make(chan struct{}), started: make(chan struct{}), opening: make(chan struct{})}
	n := awakeHost(t, dbPath(t), ad, h)
	thread := newThread(t, n.h)
	submit(t, n.h, thread, dispatch("go", api.StartNow))
	<-ad.opening
	submit(t, n.h, thread, api.ThreadDelete{})
	close(ad.started)
	<-ad.closing
	if got := h.all(); !slices.Equal(got, []bool{true}) {
		t.Fatalf("told %v with the deleted thread's started agent still closing, want [true]", got)
	}
	close(ad.release)
	toldAtLast(t, h, []bool{true, false})
}
