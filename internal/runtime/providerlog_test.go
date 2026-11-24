package runtime_test

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
	"glitched/internal/providerlog"
	"glitched/internal/runtime"
	"glitched/internal/secrets"
	"glitched/internal/store"
)

// An engine that starts deletes the provider logs of the threads its
// store holds deleted, or holds not at all, as when it stopped before a
// deletion reached the log, and keeps the rest.
func TestAStartingEngineDeletesTheLogsOfGoneThreads(t *testing.T) {
	st, err := store.Open(dbPath(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	providers := provider.Mux{"fake": fake.New(fake.Turn(false))}
	h, err := runtime.New(ctx, st, providers)
	if err != nil {
		t.Fatal(err)
	}
	kept, deleted, unknown := newThread(t, h), newThread(t, h), api.NewID()
	submit(t, h, deleted, api.ThreadDelete{})
	h.Kill()

	state := t.TempDir()
	dir := providerlog.Under(state)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, thread := range []api.ID{kept, deleted, unknown} {
		line := fmt.Sprintf("{\"time\":%q}\n", time.Now().Format(time.RFC3339Nano))
		if err := os.WriteFile(providerlog.File(dir, thread), []byte(line), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if !providerlog.Supported {
		t.Skip("this system keeps no provider log")
	}
	plog, err := providerlog.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(plog.Close)
	h, err = runtime.New(ctx, st, providers, runtime.WithProviderLog(plog, false))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Kill)
	if _, err := os.Stat(providerlog.File(dir, kept)); err != nil {
		t.Errorf("the live thread's log: %v", err)
	}
	for _, thread := range []api.ID{deleted, unknown} {
		if _, err := os.Stat(providerlog.File(dir, thread)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("a gone thread's log: %v, want none", err)
		}
	}
}

// Deleting a thread deletes its provider log and every value it has
// held, which its log no longer needs to check lines against: the log
// keeps none of its lines anymore.
func TestDeletingAThreadForgetsTheValuesItHeld(t *testing.T) {
	st, err := store.Open(dbPath(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	vault := secrets.New(t.TempDir())
	state := t.TempDir()
	dir := providerlog.Under(state)
	if !providerlog.Supported {
		t.Skip("this system keeps no provider log")
	}
	plog, err := providerlog.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(plog.Close)
	h, err := runtime.New(ctx, st, provider.Mux{"fake": fake.New(fake.Turn(false))}, runtime.WithVault(vault), runtime.WithProviderLog(plog, true))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Kill)
	thread := newThread(t, h)
	if err := vault.Put(thread, map[string]string{"GLX_PROBE": "probe-value-0123"}); err != nil {
		t.Fatal(err)
	}
	if err := vault.Put(thread, map[string]string{"GLX_PROBE": "probe-value-4567"}); err != nil {
		t.Fatal(err)
	}
	if got := vault.Held(); len(got.Values) != 2 {
		t.Fatalf("the vault holds %+v of the thread's; want both values", got)
	}
	submit(t, h, thread, api.ThreadDelete{})
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		held := vault.Held()
		if len(held.Values) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the deleted thread's values stay: %+v", held)
		}
	}
	if err := vault.Put(thread, map[string]string{"GLX_PROBE": "late"}); !errors.Is(err, secrets.ErrDeleted) {
		t.Errorf("a Put for the deleted thread: %v, want it refused", err)
	}
	plog.Write(thread, provider.ClaudeStreamJSON, provider.Stderr, []byte("late line"), len("late line"), nil)
	plog.Flush()
	if _, err := os.Stat(providerlog.File(dir, thread)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the deleted thread's log: %v, want none", err)
	}
}
