//go:build unix

package main

import (
	"context"
	"io/fs"
	"os"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/transport"
)

func TestServeKeepsTheStateDirAndSocketPrivate(t *testing.T) {
	dir, err := os.MkdirTemp("", "gl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	served := make(chan error, 1)
	go func() { served <- serve(ctx, []string{"-dir", dir}) }()
	defer func() { cancel(); <-served }()
	ep := transport.NewEndpointClient(socket(dir), api.ID{})
	for {
		if _, err := ep.Info(ctx); err == nil {
			break
		}
		select {
		case err := <-served:
			t.Fatalf("serve exited: %v", err)
		case <-time.After(20 * time.Millisecond):
		}
	}

	for path, want := range map[string]fs.FileMode{dir: 0o700, socket(dir).Address: 0o600} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s has mode %o, want %o", path, got, want)
		}
	}
}
