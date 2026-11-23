package client

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
	"glitched/internal/runtime"
	"glitched/internal/store"
)

// gate is an engine that holds each submit until let gives it a turn, and
// counts the submits waiting on it at once.
type gate struct {
	api.Endpoint
	let           chan struct{}
	mu            sync.Mutex
	waiting, most int
}

func (g *gate) Submit(ctx context.Context, c api.Command) (api.Receipt, error) {
	g.mu.Lock()
	g.waiting++
	g.most = max(g.most, g.waiting)
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		g.waiting--
		g.mu.Unlock()
	}()
	select {
	case <-g.let:
	case <-ctx.Done():
		return api.Receipt{}, ctx.Err()
	}
	return g.Endpoint.Submit(ctx, c)
}

func (g *gate) counts() (waiting, most int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.waiting, g.most
}

// Many threads settled at once wait on their engine a few at a time: the
// engine commits one at a time, so each command's deadline covers a few
// commits, not all of them, and none fails for waiting its turn.
func TestManyCommandsWaitOnTheEngineAFewAtATime(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "host.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	host, err := runtime.New(context.Background(), st, provider.Mux{"fake": fake.New(nil)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { host.Close() })
	ids := make([]api.ID, 3*submitsAtOnce)
	for i := range ids {
		ids[i] = api.NewID()
		if _, err := host.Submit(context.Background(), api.Command{ID: api.NewID(), Target: ids[i],
			Body: api.ThreadCreate{Workspace: "/work", Title: "settled", Model: fakeModel}}); err != nil {
			t.Fatal(err)
		}
	}
	dir := stateDir(t)
	gt := &gate{Endpoint: host, let: make(chan struct{})}
	serveOn(t, dir, gt)
	m, g := manager(t, dir)
	e := m.Envs()[0]
	online(t, g, e)
	g.until(t, "the threads", func() bool { return len(e.Threads) == len(ids) })
	for _, id := range ids {
		m.SetDisposition(e, id, api.Disposition{State: api.Settled})
	}
	// One at a time, as the engine commits them, on a goroutine of its own:
	// a commit waits on the GUI reading its shell, which g.until does.
	go func() {
		for range ids {
			gt.let <- struct{}{}
		}
	}()
	g.until(t, "every settle taken", func() bool {
		for _, id := range ids {
			if th := e.Threads[id]; th.View.Disposition.State != api.Settled || len(th.Pending) > 0 {
				return false
			}
		}
		return true
	}, func() string {
		th := e.Threads[ids[0]]
		w, most := gt.counts()
		return fmt.Sprintf("first %v pending %+v, waiting %d most %d", th.View.Disposition.State, th.Pending, w, most)
	})
	if _, most := gt.counts(); most > submitsAtOnce {
		t.Errorf("%d commands waited on the engine at once, want %d at most", most, submitsAtOnce)
	}
}
