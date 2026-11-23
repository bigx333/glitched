package runtime_test

import (
	"slices"
	"sync"
	"testing"

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
