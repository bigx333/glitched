package client

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"glitched/internal/daemon"
)

func readConnUntil(t *testing.T, conn *TerminalConn, want string) string {
	t.Helper()
	type res struct {
		out string
		err error
	}
	ch := make(chan res, 1)
	go func() {
		var sb strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				sb.Write(buf[:n])
				if strings.Contains(sb.String(), want) {
					ch <- res{out: sb.String()}
					return
				}
			}
			if err != nil {
				ch <- res{out: sb.String(), err: err}
				return
			}
		}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("TerminalConn read error before %q (saw %q): %v", want, r.out, r.err)
		}
		return r.out
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out reading %q from TerminalConn", want)
		return ""
	}
}

func TestLocalEmbeddedTerminalPersistsAcrossDetach(t *testing.T) {
	dir := stateDir(t)
	g := &gui{wake: make(chan struct{}, 1)}
	m := New(Config{
		Dir:  dir,
		Post: g.post,
		Engine: func(c *daemon.Config) {
			c.Shell = []string{"/bin/sh"}
		},
	})
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })

	local := m.Envs()[0]
	online(t, g, local)
	ws := t.TempDir()

	conn1, err := m.OpenTerminal(local, ws, 80, 24, nil)
	if err != nil {
		t.Fatalf("OpenTerminal 1: %v", err)
	}
	if _, err := conn1.Write([]byte("echo client-pty-alpha\n")); err != nil {
		t.Fatalf("Write 1: %v", err)
	}
	readConnUntil(t, conn1, "client-pty-alpha")
	if err := conn1.Close(); err != nil {
		t.Fatalf("Close conn1: %v", err)
	}

	// Reopening detaches/reattaches to the same surviving engine PTY with replay.
	conn2, err := m.OpenTerminal(local, ws, 80, 24, nil)
	if err != nil {
		t.Fatalf("OpenTerminal 2: %v", err)
	}
	defer conn2.Close()

	replayed := readConnUntil(t, conn2, "client-pty-alpha")
	if !strings.Contains(replayed, "client-pty-alpha") {
		t.Fatalf("replay missing output: %q", replayed)
	}
	if _, err := conn2.Write([]byte("echo client-pty-beta\n")); err != nil {
		t.Fatalf("Write 2: %v", err)
	}
	readConnUntil(t, conn2, "client-pty-beta")
}

func TestRemoteEngineTerminal(t *testing.T) {
	localDir := stateDir(t)
	m, g := manager(t, localDir)
	online(t, g, m.Envs()[0])

	remoteDir := stateDir(t)
	tok, err := daemon.Pair(remoteDir, "gui")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := daemon.Defaults(remoteDir)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Listen = []string{"127.0.0.1:0"}
	cfg.Shell = []string{"/bin/sh"}
	d, err := daemon.Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })

	if err := add(t, g, m, Server{Label: "box", Addr: d.Addrs()[0].String(), Token: tok}); err != nil {
		t.Fatalf("AddServer: %v", err)
	}
	remoteEnv := m.Envs()[1]
	online(t, g, remoteEnv)

	ws := t.TempDir()
	conn, err := m.OpenTerminal(remoteEnv, ws, 90, 28, nil)
	if err != nil {
		t.Fatalf("OpenTerminal remote: %v", err)
	}
	defer conn.Close()

	if err := conn.Resize(100, 30); err != nil {
		t.Fatalf("Resize remote: %v", err)
	}
	if _, err := conn.Write([]byte("echo remote-shell-xyz\n")); err != nil {
		t.Fatalf("Write remote: %v", err)
	}
	out := readConnUntil(t, conn, "remote-shell-xyz")
	if !strings.Contains(out, "remote-shell-xyz") {
		t.Fatalf("remote output = %q", out)
	}
	// Closing detaches without error.
	if err := conn.Close(); err != nil && err != io.EOF {
		t.Fatalf("Close remote: %v", err)
	}
}
