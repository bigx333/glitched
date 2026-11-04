package runtime_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
	"glitched/internal/store"
)

// closeState is the state of the one agent.close effect in the store at path.
func closeState(t *testing.T, path string) func() string {
	db := sideDB(t, path)
	return func() (state string) {
		t.Helper()
		if err := db.QueryRow("SELECT state FROM effects WHERE kind = 'agent.close'").Scan(&state); err != nil {
			t.Fatal(err)
		}
		return state
	}
}

// offline commits intents to the store at path with no host serving it,
// and leases the next effect when lease is set, as a host that died would.
func offline(t *testing.T, path string, thread api.ID, lease bool, intents ...api.Intent) {
	t.Helper()
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for i, b := range intents {
		if _, err := st.Submit(ctx, api.Command{ID: api.NewID(), Target: thread, Body: b}, time.Now()); err != nil {
			t.Fatal(err)
		}
		if lease && i == 1 {
			if _, ok, err := st.Lease(ctx, func() []api.ID { return nil }); !ok || err != nil {
				t.Fatalf("lease the start: %v %v", ok, err)
			}
		}
	}
}

// reconciles counts the Reconcile calls an adapter gets.
type reconciles struct {
	*fake.Adapter
	mu   sync.Mutex
	runs []api.ID
}

func (r *reconciles) Reconcile(ctx context.Context, id provider.RuntimeIdentity) (provider.Recovery, error) {
	r.mu.Lock()
	r.runs = append(r.runs, id.Run)
	r.mu.Unlock()
	return r.Adapter.Reconcile(ctx, id)
}

func (r *reconciles) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.runs)
}

// A thread deleted before its start ran has no agent to close, so the
// close is done even when no adapter serves its provider.
func TestACloseForARunThatNeverStartedIsDone(t *testing.T) {
	path := dbPath(t)
	thread := api.NewID()
	offline(t, path, thread, false,
		api.ThreadCreate{Workspace: "/src/app", Title: "on nope", Model: api.ModelRef{Provider: "nope", Model: "m"}},
		dispatch("go", api.StartNow),
		api.ThreadDelete{})
	serve(t, path, provider.Mux{"fake": fake.New(fake.Turn(false))})
	state := closeState(t, path)
	for deadline := time.Now().Add(5 * time.Second); state() != "done"; time.Sleep(2 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the close is %s, want done: the run never started, so it has no agent", state())
		}
	}
}

// A start a dead host leased may have started an agent, so the close of
// its deleted thread still asks the adapter for it.
func TestACloseForAStartADeadHostTookFindsItsAgent(t *testing.T) {
	path := dbPath(t)
	thread := api.NewID()
	offline(t, path, thread, true,
		api.ThreadCreate{Workspace: "/src/app", Title: "on b", Model: api.ModelRef{Provider: "b", Model: "m"}},
		dispatch("go", api.StartNow),
		api.ThreadDelete{})
	b := &reconciles{Adapter: fake.New(fake.Turn(false))}
	serve(t, path, provider.Mux{"b": b})
	state := closeState(t, path)
	for deadline := time.Now().Add(5 * time.Second); state() != "done"; time.Sleep(2 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the close is %s, want done", state())
		}
	}
	if b.count() != 1 {
		t.Errorf("the adapter was asked for the run %d times, want once", b.count())
	}
}

type lockedLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedLog) count(s string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Count(l.b.String(), s)
}

// A close that waits on a missing provider retries every second; it says
// so once, not each time.
func TestARetryLogsAnErrorThatDoesNotChangeOnce(t *testing.T) {
	path := dbPath(t)
	b := &unreachable{Adapter: fake.New(fake.Turn(true)), tried: make(chan struct{}, 1)}
	n := serve(t, path, provider.Mux{"b": b})
	thread := threadOn(t, n.h, "b")
	submit(t, n.h, thread, dispatch("go", api.StartNow))
	until(t, n.h, thread, "the approval", waiting)
	b.fail.Store(true)
	submit(t, n.h, thread, api.ThreadDelete{})
	<-b.tried
	n.kill()

	logs := &lockedLog{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
	defer slog.SetDefault(old)
	n = serve(t, path, provider.Mux{})
	state := closeState(t, path)
	for deadline := time.Now().Add(5 * time.Second); state() == "pending"; time.Sleep(2 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the close was never taken up")
		}
	}
	time.Sleep(3 * time.Second)
	n.kill()
	if got := logs.count("find agent"); got != 1 {
		t.Errorf("the close logged its unchanged error %d times in 3s, want once", got)
	}
}
