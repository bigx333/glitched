//go:build unix

package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"glitched/internal/api"
)

// A turn waits at most syncFirst for its sync, though the sync is stuck
// reading this machine's own files.
func TestATurnWaitsForItsSyncNoLongerThanSyncFirst(t *testing.T) {
	was := syncFirst
	syncFirst = 25 * time.Millisecond
	t.Cleanup(func() { syncFirst = was })
	dir := t.TempDir()
	filename := filepath.Join(dir, "engine.json")
	if err := syscall.Mkfifo(filename, 0600); err != nil {
		t.Fatal(err)
	}
	hold, err := os.OpenFile(filename, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Close()
	ep := heldSync{sync: func(context.Context) error { return errors.New("should not reach sync") }, submitted: make(chan api.Intent, 1)}
	m, e, id := heldRemote(t, dir, ep)
	m.Send(e, id, "hello", api.AfterActive)
	select {
	case b := <-ep.submitted:
		if _, ok := b.(api.MessageDispatch); !ok {
			t.Fatalf("unexpected %T", b)
		}
	case <-time.After(time.Second):
		t.Fatal("the send waited on its sync's blocked config read past syncFirst")
	}
}
