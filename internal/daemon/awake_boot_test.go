package daemon

import (
	"context"
	"sync"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
)

// working opens the fake's agents to play the first two events of a turn
// and then work on, and takes them back after a restart still working;
// the second Reconcile waits for gate.
type working struct {
	*fake.Adapter
	gate, stalled chan struct{}
	mu            sync.Mutex
	reconciles    int
}

func (w *working) Open(ctx context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	g, err := w.Adapter.Open(ctx, spec)
	return &workingAgent{Agent: g, left: 2}, err
}

func (w *working) Reconcile(ctx context.Context, id provider.RuntimeIdentity) (provider.Recovery, error) {
	w.mu.Lock()
	w.reconciles++
	second := w.reconciles == 2
	w.mu.Unlock()
	if second {
		close(w.stalled)
		select {
		case <-w.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	rec, err := w.Adapter.Reconcile(ctx, id)
	if r, ok := rec.(provider.Restored); ok {
		rec = provider.Restored{Agent: &workingAgent{Agent: r.Agent}}
	}
	return rec, err
}

type workingAgent struct {
	provider.Agent
	left int
}

func (g *workingAgent) Next(ctx context.Context) (provider.Event, error) {
	if g.left == 0 {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	g.left--
	return g.Agent.Next(ctx)
}

// An agent a restarted engine takes back still working holds the machine
// awake from then on, while the engine still takes back the rest.
func TestARestoredAgentHoldsTheMachineAwakeDuringBoot(t *testing.T) {
	dir := stateDir(t)
	w := &working{Adapter: fake.New(fake.Turn(false)), gate: make(chan struct{}), stalled: make(chan struct{})}
	first := make(inhibitor, 4)
	d, err := Start(context.Background(), Config{Dir: dir, Providers: provider.Mux{"fake": w}, Inhibit: first.hold})
	if err != nil {
		t.Fatal(err)
	}
	ep, ids := d.Endpoint(), []api.ID{api.NewID(), api.NewID()}
	for _, id := range ids {
		for _, b := range []api.Intent{api.ThreadCreate{Workspace: "/src/app", Title: "fix it", Model: api.ModelRef{Provider: "fake", Model: "fake"}}, api.MessageDispatch{Text: "go", Mode: api.StartNow}} {
			if _, err := ep.Submit(context.Background(), api.Command{ID: api.NewID(), Target: id, Body: b}); err != nil {
				t.Fatal(err)
			}
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	for _, id := range ids {
		for {
			snap, err := ep.Read(context.Background(), api.Query{Thread: id})
			if err == nil && snap.Threads[0].Run == api.RunRunning {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("the runs never got to running")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	first.next(t, true)
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	first.next(t, false)

	second := make(inhibitor, 4)
	started := make(chan *Daemon, 1)
	go func() {
		d, err := Start(context.Background(), Config{Dir: dir, Providers: provider.Mux{"fake": w}, Inhibit: second.hold})
		if err != nil {
			t.Error(err)
		}
		started <- d
	}()
	<-w.stalled
	second.next(t, true)
	close(w.gate)
	d = <-started
	if d == nil {
		return
	}
	defer d.Close()
	for _, id := range ids {
		if snap, err := d.Endpoint().Read(context.Background(), api.Query{Thread: id}); err != nil || snap.Threads[0].Run != api.RunRunning {
			t.Fatalf("thread %v after the restart: %+v, %v; want its run taken back running", id, snap.Threads, err)
		}
	}
}
