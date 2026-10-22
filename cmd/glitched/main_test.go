package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/transport"
)

func TestServeRunAndReplicate(t *testing.T) {
	// A short path: unix socket paths are limited to about 100 bytes.
	dir, err := os.MkdirTemp("", "gl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	served := make(chan error, 1)
	go func() { served <- serve(ctx, []string{"-dir", dir}) }()
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
	if err := serve(ctx, []string{"-dir", dir}); err != errLocked {
		t.Fatalf("second engine on the same dir: %v, want errLocked", err)
	}

	if err := run(ctx, []string{"-dir", dir, "-yes", "hello"}); err != nil {
		t.Fatal(err)
	}
	snap, err := ep.Read(ctx, api.Query{})
	if err != nil || len(snap.Threads) != 1 || snap.Threads[0].Run != api.RunIdle {
		t.Fatalf("threads after the turn: %+v %v", snap.Threads, err)
	}
	thread := snap.Threads[0].ID
	host, err := ep.Read(ctx, api.Query{Thread: thread})
	if err != nil || len(host.Events) == 0 {
		t.Fatalf("host log for the thread: %d events, %v", len(host.Events), err)
	}

	hubc := transport.NewHubClient(socket(dir), api.ID{})
	deadline := time.Now().Add(5 * time.Second)
	for {
		evs, err := hubc.Events(ctx, thread, api.Cursor{}, 1000)
		if err != nil {
			t.Fatal(err)
		}
		if len(evs) == len(host.Events) {
			for i := range evs {
				if evs[i].Cursor != host.Events[i].Cursor || evs[i].Type != host.Events[i].Type {
					t.Fatalf("replica event %d is %+v, host has %+v", i, evs[i], host.Events[i])
				}
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("replica holds %d events, host %d", len(evs), len(host.Events))
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	if err := <-served; err != nil {
		t.Fatalf("serve: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "engine.db")); err != nil {
		t.Fatal(err)
	}
}
