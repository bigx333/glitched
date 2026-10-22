package main

import (
	"context"
	"os"
	"strings"
	"testing"
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

func TestServeRefusesAListenAddressOffTheTailnet(t *testing.T) {
	err := serve(context.Background(), []string{"-dir", stateDir(t), "-listen", "0.0.0.0:7465"})
	if err == nil || !strings.Contains(err.Error(), "Tailscale") {
		t.Fatalf("serve -listen 0.0.0.0:7465: %v, want the tailnet rule", err)
	}
}
