package main

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/daemon"
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
	d, err := daemon.Start(context.Background(), daemon.Config{Dir: dir, Hub: true, Listen: []string{"127.0.0.1:0"}, Adapter: fake.New(fake.Turn(true))})
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
