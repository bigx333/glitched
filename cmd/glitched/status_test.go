package main

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"glitched/internal/daemon"
)

func TestStatusSaysWhetherTheEngineServes(t *testing.T) {
	dir := stateDir(t)
	read := func() string {
		t.Helper()
		var err error
		out := stdout(t, func() { err = status(context.Background(), []string{"-dir", dir}) })
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if out := read(); !strings.Contains(out, "engine     not running") || !strings.Contains(out, "tokens     none") {
		t.Errorf("status before serve:\n%s", out)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	if _, err := daemon.Pair(dir, "laptop"); err != nil {
		t.Fatal(err)
	}
	stop := serving(t, dir, "-listen", addr)
	// serve saves engine.json once its socket answers, a moment after
	// serving returns.
	out := read()
	for deadline := time.Now().Add(5 * time.Second); !strings.Contains(out, addr) && time.Now().Before(deadline); out = read() {
		time.Sleep(10 * time.Millisecond)
	}
	for _, want := range []string{"engine     serving as ", "hub        this engine", "listen     " + addr + "  accepting", "token      laptop  "} {
		if !strings.Contains(out, want) {
			t.Errorf("status while serving lacks %q:\n%s", want, out)
		}
	}
	stop()
	if out := read(); !strings.Contains(out, "listen     "+addr+"  not accepting") || !strings.Contains(out, "engine     not running") {
		t.Errorf("status after serve stopped:\n%s", out)
	}
}
