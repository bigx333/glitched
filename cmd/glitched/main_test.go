package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/daemon"
	eng "glitched/internal/engine"
	"glitched/internal/hub"
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
	ep := transport.NewEndpointClient(daemon.Socket(dir), api.ID{})
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
	if err := serve(ctx, []string{"-dir", dir}); err != daemon.ErrLocked {
		t.Fatalf("second engine on the same dir: %v, want daemon.ErrLocked", err)
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

	replicated(t, transport.NewHubClient(daemon.Socket(dir), api.ID{}), thread, host.Events)

	cancel()
	if err := <-served; err != nil {
		t.Fatalf("serve: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "engine.db")); err != nil {
		t.Fatal(err)
	}
}

// replicated waits until the hub holds the host's events for thread.
func replicated(t *testing.T, hubc *transport.HubClient, thread api.ID, host []api.Event) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		evs, err := hubc.Events(context.Background(), thread, api.Cursor{}, 1000)
		if err != nil {
			t.Fatal(err)
		}
		if len(evs) == len(host) {
			for i := range evs {
				if evs[i].Cursor != host[i].Cursor || evs[i].Type != host[i].Type {
					t.Fatalf("replica event %d is %+v, host has %+v", i, evs[i], host[i])
				}
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("replica holds %d events, host %d", len(evs), len(host))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func serving(t *testing.T, dir string, args ...string) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	served := started(t, ctx, dir, args...)
	return func() {
		cancel()
		if err := <-served; err != nil {
			t.Errorf("serve: %v", err)
		}
	}
}

// started runs serve on dir until ctx ends and returns once its socket
// answers, with the channel serve's result arrives on.
func started(t *testing.T, ctx context.Context, dir string, args ...string) <-chan error {
	t.Helper()
	served := make(chan error, 1)
	go func() { served <- serve(ctx, append([]string{"-dir", dir}, args...)) }()
	ep := transport.NewEndpointClient(daemon.Socket(dir), api.ID{})
	defer ep.Close()
	for {
		if _, err := ep.Info(ctx); err == nil {
			return served
		}
		select {
		case err := <-served:
			t.Fatalf("serve exited: %v", err)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// restoreBehindTheHub serves the host under dir, uploading to the hub under
// hubDir, until the hub holds two events of its log, then brings back a
// backup of dir taken after the first. It returns that log.
func restoreBehindTheHub(t *testing.T, hubDir, dir string) api.ID {
	t.Helper()
	ctx := context.Background()
	backup := stateDir(t)
	hubc := transport.NewHubClient(daemon.Socket(hubDir), api.ID{})
	defer hubc.Close()
	ep := transport.NewEndpointClient(daemon.Socket(dir), api.ID{})
	defer ep.Close()
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
	return env.Log
}

func TestServeRewindsALogTheHubHoldsMoreOf(t *testing.T) {
	hubDir, dir := stateDir(t), stateDir(t)
	defer serving(t, hubDir)()
	log := restoreBehindTheHub(t, hubDir, dir)
	defer serving(t, dir, "-hub=false", "-hub-dir", hubDir)()
	ep := transport.NewEndpointClient(daemon.Socket(dir), api.ID{})
	defer ep.Close()
	restored, err := ep.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if restored.Log == log {
		t.Fatalf("the whole dir came back from a backup and kept log %s, whose seq 2 the hub holds and the database will hand out again", log)
	}
}

func TestServeStopsOnARestoreTheHubProvesAfterStart(t *testing.T) {
	hubDir, dir := stateDir(t), stateDir(t)
	stopHub := serving(t, hubDir)
	log := restoreBehindTheHub(t, hubDir, dir)
	// The hub is down as the host starts, so the startup check can't see the
	// restore.
	stopHub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := started(t, ctx, dir, "-hub=false", "-hub-dir", hubDir)
	ep := transport.NewEndpointClient(daemon.Socket(dir), api.ID{})
	defer ep.Close()
	if env, err := ep.Info(ctx); err != nil || env.Log != log {
		t.Fatalf("the host started on log %s (%v), want the restored %s", env.Log, err, log)
	}

	defer serving(t, hubDir)()
	select {
	case err := <-served:
		if !errors.Is(err, hub.ErrRewound) || !strings.Contains(err.Error(), "restart") {
			t.Fatalf("serve: %v, want ErrRewound and to be told to restart", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the host kept serving a log the hub holds more of")
	}

	// A service manager restarts it, and the startup check rewinds.
	defer serving(t, dir, "-hub=false", "-hub-dir", hubDir)()
	if env, err := ep.Info(ctx); err != nil || env.Log == log {
		t.Fatalf("after a restart the host serves log %s (%v), want a new one", env.Log, err)
	}
}

// serve keeps how it ran, so the next serve on the dir, and the engine the
// GUI embeds there, run the same way unless a flag says otherwise.
func TestServeKeepsItsFlagsForTheNextStart(t *testing.T) {
	dir := stateDir(t)
	keeps := func(want daemon.Config) {
		t.Helper()
		cfg, err := daemon.Defaults(dir)
		if err != nil {
			t.Fatal(err)
		}
		cfg.Dir, cfg.Providers = "", nil
		if !reflect.DeepEqual(cfg, want) {
			t.Fatalf("the next start runs as %+v, want %+v", cfg, want)
		}
	}
	serving(t, dir, "-hub-server", "127.0.0.1:1", "-hub-token", "secret", "-epoch", "2", "-listen", "127.0.0.1:0")()
	keeps(daemon.Config{HubServer: "127.0.0.1:1", HubToken: "secret", Epoch: 2, Listen: []string{"127.0.0.1:0"}})
	if fi, err := os.Stat(filepath.Join(dir, "engine.json")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("engine.json: %v, want mode 0600: it holds the hub's token", err)
	}

	serving(t, dir, "-epoch", "3")()
	keeps(daemon.Config{HubServer: "127.0.0.1:1", HubToken: "secret", Epoch: 3, Listen: []string{"127.0.0.1:0"}})

	serving(t, dir, "-hub", "-listen", "")()
	keeps(daemon.Config{Hub: true, Epoch: 3})
}

// serve keeps the child limits its flags set, and a later serve without
// them keeps those; a limit out of range is a usage error.
func TestServeKeepsTheChildLimits(t *testing.T) {
	dir := stateDir(t)
	limits := func() eng.Limits {
		t.Helper()
		cfg, err := daemon.Defaults(dir)
		if err != nil {
			t.Fatal(err)
		}
		return cfg.Children
	}
	serving(t, dir, "-max-working", "2", "-max-children", "7", "-max-depth", "3")()
	if got, want := limits(), (eng.Limits{Working: 2, All: 7, Depth: 3}); got != want {
		t.Fatalf("after serve with the flags: %+v, want %+v", got, want)
	}
	serving(t, dir, "-max-depth", "1")()
	if got, want := limits(), (eng.Limits{Working: 2, All: 7, Depth: 1}); got != want {
		t.Fatalf("after serve with -max-depth alone: %+v, want %+v", got, want)
	}
	for _, args := range [][]string{{"-max-depth", "5"}, {"-max-depth", "0"}, {"-max-working", "0"}, {"-max-children", "-1"}} {
		if err := serve(context.Background(), append([]string{"-dir", dir}, args...)); !errors.As(err, new(usageError)) {
			t.Errorf("serve %v: %v, want a usage error", args, err)
		}
	}
}

// A relative -hub-dir is kept as the directory it named where serve ran,
// for the next start from anywhere else, as the GUI's from /.
func TestServeKeepsHubDirFromWhereItRan(t *testing.T) {
	dir, cwd := stateDir(t), t.TempDir()
	t.Chdir(cwd)
	serving(t, dir, "-hub-dir", "hub")()
	cfg, err := daemon.Defaults(dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(cwd, "hub"); cfg.HubDir != want {
		t.Errorf("the next start uploads to the hub under %q, want %q", cfg.HubDir, want)
	}
}

// A claude thread on a standalone engine runs Claude Code, found where
// engine.json's claude_path says. Here nothing is there to run.
func TestServeRunsClaudeThreadsOnClaudeCode(t *testing.T) {
	dir, err := os.MkdirTemp("", "gl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if err := os.WriteFile(filepath.Join(dir, "engine.json"), []byte(`{"hub":true,"epoch":1,"claude_path":"/nonexistent/claude"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	served := make(chan error, 1)
	go func() { served <- serve(ctx, []string{"-dir", dir}) }()
	ep := transport.NewEndpointClient(daemon.Socket(dir), api.ID{})
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

	if err := run(ctx, []string{"-dir", dir, "-provider", "claude", "say hi"}); err != nil {
		t.Fatal(err)
	}
	snap, err := ep.Read(ctx, api.Query{})
	if err != nil || len(snap.Threads) != 1 {
		t.Fatalf("threads after the turn: %+v %v", snap.Threads, err)
	}
	host, err := ep.Read(ctx, api.Query{Thread: snap.Threads[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	var ended *api.TurnEnded
	for _, e := range host.Events {
		if b, err := e.Decode(); err == nil {
			if te, ok := b.(api.TurnEnded); ok {
				ended = &te
			}
		}
	}
	if want := "Claude Code is not installed.\nInstall it, or set claude_path in engine.json."; ended == nil || ended.Outcome != api.OutcomeFailed || ended.Error != want {
		t.Errorf("the claude turn ended %+v, want it failed with\n%s", ended, want)
	}
	cancel()
	<-served
}
