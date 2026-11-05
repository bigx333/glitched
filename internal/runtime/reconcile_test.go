package runtime_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
)

// stuck stands for the claude adapter when a run's process will not die:
// while fail is set, Reconcile errors.
type stuck struct {
	*fake.Adapter
	fail atomic.Bool
	mu   sync.Mutex
	runs map[api.ID]int
}

func (s *stuck) Reconcile(ctx context.Context, id provider.RuntimeIdentity) (provider.Recovery, error) {
	s.mu.Lock()
	s.runs[id.Run]++
	s.mu.Unlock()
	if s.fail.Load() {
		return nil, errors.New("claude: processes [4242] of the run still run")
	}
	return s.Adapter.Reconcile(ctx, id)
}

func (s *stuck) calls(run api.ID) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runs[run]
}

// crashWithAStuckRun leaves a run waiting on approval when its host dies,
// and serves the database again with an adapter that can't reconcile it.
func crashWithAStuckRun(t *testing.T, path string) (n node, b *stuck, thread, run api.ID) {
	t.Helper()
	b = &stuck{Adapter: fake.New(fake.Turn(true)), runs: map[api.ID]int{}}
	n = serve(t, path, provider.Mux{"b": b})
	thread = threadOn(t, n.h, "b")
	run = submit(t, n.h, thread, dispatch("go", api.StartNow)).Command
	until(t, n.h, thread, "the approval", waiting)
	n.kill()
	b.fail.Store(true)
	n = serve(t, path, provider.Mux{"b": b})
	until(t, n.h, thread, "uncertain", uncertain)
	return n, b, thread, run
}

func retried(t *testing.T, b *stuck, run api.ID, than int) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); b.calls(run) <= than; time.Sleep(2 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the run was reconciled %d times and never again: a failed reconcile must be retried", b.calls(run))
		}
	}
}

func TestAFailedBootReconcileIsRetriedBeforeTheNextTurnStarts(t *testing.T) {
	n, b, thread, run := crashWithAStuckRun(t, dbPath(t))
	submit(t, n.h, thread, api.TurnInterrupt{})
	until(t, n.h, thread, "idle", idle)
	submit(t, n.h, thread, dispatch("next", api.StartNow))
	retried(t, b, run, 2)
	if b.Starts() != 1 {
		t.Fatalf("the next turn started while the old run's processes may still run")
	}
	b.fail.Store(false)
	until(t, n.h, thread, "the next turn's approval", waiting)
	if b.Starts() != 2 {
		t.Errorf("starts = %d, want 2", b.Starts())
	}
}

func TestADeleteWaitsForAFailedBootReconcileAcrossARestart(t *testing.T) {
	path := dbPath(t)
	n, b, thread, run := crashWithAStuckRun(t, path)
	submit(t, n.h, thread, api.TurnInterrupt{})
	until(t, n.h, thread, "idle", idle)
	submit(t, n.h, thread, api.ThreadDelete{})
	retried(t, b, run, 2)
	n.kill()
	state := closeState(t, path)
	if s := state(); s == "done" {
		t.Fatalf("the close is done while the run's reconcile still fails")
	}
	calls := b.calls(run)
	serve(t, path, provider.Mux{"b": b})
	retried(t, b, run, calls)
	b.fail.Store(false)
	for deadline := time.Now().Add(5 * time.Second); state() != "done"; time.Sleep(2 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the close is %s, want done once the reconcile succeeds", state())
		}
	}
}
