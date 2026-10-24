package main

import (
	"context"
	"errors"
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

func serving(t *testing.T, dir string, args ...string) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- serve(ctx, append([]string{"-dir", dir}, args...)) }()
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
	return func() {
		cancel()
		if err := <-served; err != nil {
			t.Errorf("serve: %v", err)
		}
	}
}

func TestServeRewindsALogTheHubHoldsMoreOf(t *testing.T) {
	ctx := context.Background()
	var dirs [3]string
	for i := range dirs {
		dir, err := os.MkdirTemp("", "gl")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(dir) })
		dirs[i] = dir
	}
	hubDir, dir, backup := dirs[0], dirs[1], dirs[2]
	defer serving(t, hubDir)()
	hubc := transport.NewHubClient(socket(hubDir), api.ID{})
	ep := transport.NewEndpointClient(socket(dir), api.ID{})
	host := func() func() { return serving(t, dir, "-hub=false", "-hub-dir", hubDir) }
	uploaded := func(log api.ID, want uint64) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for n, _ := hubc.Acked(ctx, log); n != want; n, _ = hubc.Acked(ctx, log) {
			if time.Now().After(deadline) {
				t.Fatalf("hub acked %d of log %s, want %d", n, log, want)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	files := []string{"engine.db", "engine.db-wal", "engine.db-shm", "engine.db.fence"}
	copyFiles := func(from, to string) {
		t.Helper()
		for _, f := range files {
			b, err := os.ReadFile(filepath.Join(from, f))
			if errors.Is(err, os.ErrNotExist) {
				os.Remove(filepath.Join(to, f))
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(to, f), b, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}

	stop := host()
	thread := newThread(t, ep)
	env, err := ep.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	uploaded(env.Log, 1)
	stop()
	copyFiles(dir, backup)

	stop = host()
	settle := api.InboxSet{Disposition: api.Disposition{State: api.Settled}}
	if _, err := ep.Submit(ctx, api.Command{ID: api.NewID(), Target: thread, Body: settle}); err != nil {
		t.Fatal(err)
	}
	uploaded(env.Log, 2)
	stop()

	copyFiles(backup, dir)
	defer host()()
	restored, err := ep.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Log == env.Log {
		t.Fatalf("the whole dir came back from a backup and kept log %s, whose seq 2 the hub holds and the database will hand out again", env.Log)
	}
}
