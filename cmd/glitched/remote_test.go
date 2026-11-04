package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/daemon"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
	"glitched/internal/transport"
)

func stateDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "gl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// remoteEngine serves an engine that runs the hub on a loopback port, and
// returns that address and a token the engine paired.
func remoteEngine(t *testing.T) (addr, token string) {
	t.Helper()
	dir := stateDir(t)
	token, err := daemon.Pair(dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	d, err := daemon.Start(context.Background(), daemon.Config{Dir: dir, Hub: true, Listen: []string{"127.0.0.1:0"}, Providers: provider.Mux{"fake": fake.New(fake.Turn(true))}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d.Addrs()[0].String(), token
}

func TestRunAndThreadsReachARemoteEngine(t *testing.T) {
	addr, token := remoteEngine(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// No engine serves -dir, so only -server answers.
	nowhere := []string{"-dir", stateDir(t), "-server", addr}
	if err := run(ctx, slices.Concat(nowhere, []string{"-token", token, "-yes", "hello"})); err != nil {
		t.Fatalf("run -server: %v", err)
	}
	if err := threads(ctx, slices.Concat(nowhere, []string{"-token", token})); err != nil {
		t.Fatalf("threads -server: %v", err)
	}
	if err := threads(ctx, slices.Concat(nowhere, []string{"-token", "wrong"})); !errors.Is(err, transport.ErrRefused) {
		t.Fatalf("threads -server with a wrong token: %v, want ErrRefused", err)
	}
	ep := transport.NewEndpointClient(transport.Tailnet{Address: addr, Token: token}, api.ID{})
	defer ep.Close()
	snap, err := ep.Read(ctx, api.Query{})
	if err != nil || len(snap.Threads) != 1 || snap.Threads[0].Title != "hello" || snap.Threads[0].Run != api.RunIdle {
		t.Fatalf("threads on the remote engine after the turn: %+v %v", snap.Threads, err)
	}
}

func TestServeUploadsToARemoteHubWithAToken(t *testing.T) {
	addr, token := remoteEngine(t)
	dir := stateDir(t)
	defer serving(t, dir, "-hub=false", "-hub-server", addr, "-hub-token", token)()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := run(ctx, []string{"-dir", dir, "-yes", "hello"}); err != nil {
		t.Fatal(err)
	}
	ep := transport.NewEndpointClient(daemon.Socket(dir), api.ID{})
	defer ep.Close()
	snap, err := ep.Read(ctx, api.Query{})
	if err != nil || len(snap.Threads) != 1 {
		t.Fatalf("threads on the host: %+v %v", snap.Threads, err)
	}
	thread := snap.Threads[0].ID
	host, err := ep.Read(ctx, api.Query{Thread: thread})
	if err != nil {
		t.Fatal(err)
	}
	hubc := transport.NewHubClient(transport.Tailnet{Address: addr, Token: token}, api.ID{})
	defer hubc.Close()
	replicated(t, hubc, thread, host.Events)
	if err := replica(ctx, []string{"-server", addr, "-token", token, "-thread", thread.String()}); err != nil {
		t.Fatalf("replica -server: %v", err)
	}
}

func TestServeRefusesAListenAddressOffTheTailnet(t *testing.T) {
	err := serve(context.Background(), []string{"-dir", stateDir(t), "-listen", "0.0.0.0:7465"})
	if err == nil || !strings.Contains(err.Error(), "Tailscale") {
		t.Fatalf("serve -listen 0.0.0.0:7465: %v, want the tailnet rule", err)
	}
}

func TestRemoteFlagsThatCantWorkAreUsageErrors(t *testing.T) {
	// Nothing may be dialled or started before the flags are checked.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dir, hubDir := stateDir(t), stateDir(t)
	thread := api.NewID().String()
	cases := []struct {
		name string
		cmd  func(context.Context, []string) error
		args []string
	}{
		{"run -token without -server", run, []string{"-dir", dir, "-token", "x", "-yes", "hi"}},
		{"run -server without -token", run, []string{"-dir", dir, "-server", "127.0.0.1:1", "-yes", "hi"}},
		{"threads -token without -server", threads, []string{"-dir", dir, "-token", "x"}},
		{"threads -server without -token", threads, []string{"-dir", dir, "-server", "127.0.0.1:1"}},
		{"replica -token without -server", replica, []string{"-dir", dir, "-token", "x", "-thread", thread}},
		{"replica -server without -token", replica, []string{"-dir", dir, "-server", "127.0.0.1:1", "-thread", thread}},
		{"serve -hub-token without -hub-server", serve, []string{"-dir", dir, "-hub-token", "x"}},
		{"serve -hub-server without -hub-token", serve, []string{"-dir", dir, "-hub=false", "-hub-server", "127.0.0.1:1"}},
		{"serve -hub-dir and -hub-server", serve, []string{"-dir", dir, "-hub-dir", hubDir, "-hub-server", "127.0.0.1:1", "-hub-token", "x"}},
		{"serve -hub and -hub-dir", serve, []string{"-dir", dir, "-hub", "-hub-dir", hubDir}},
		{"serve -hub and -hub-server", serve, []string{"-dir", dir, "-hub=true", "-hub-server", "127.0.0.1:1", "-hub-token", "x"}},
	}
	for _, c := range cases {
		err := c.cmd(ctx, c.args)
		if !errors.As(err, new(usageError)) {
			t.Errorf("%s: %v, want a usage error", c.name, err)
		}
	}
}

// stdout is what f printed.
func stdout(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	f()
	os.Stdout = old
	w.Close()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestPairPrintsAServeLineForItsDir(t *testing.T) {
	dir := filepath.Join(stateDir(t), "a dir")
	var err error
	out := stdout(t, func() { err = pair(context.Background(), []string{"-dir", dir, "-name", "laptop"}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "serve -dir '"+dir+"' -tailnet") {
		t.Fatalf("pair -dir %s printed no serve line for that dir:\n%s", dir, out)
	}
}
