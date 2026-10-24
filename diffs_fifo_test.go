//go:build unix

package main

import (
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// An edit can name a FIFO, which no writer may ever open: the panel says
// it isn't a file rather than waiting on it.
func TestAFIFODoesNotBlock(t *testing.T) {
	ws := gitWorkspace(t)
	if err := syscall.Mkfifo(filepath.Join(ws, "pipe"), 0o644); err != nil {
		t.Skip("no FIFOs:", err)
	}
	read := make(chan *fileDiff)
	go func() { read <- loaded(ws, "pipe") }()
	select {
	case d := <-read:
		if d.DiffState != nil || !strings.Contains(d.unshown, "regular file") {
			t.Fatalf("shown %v, saying %q; want it not shown, as it isn't a regular file", d.DiffState != nil, d.unshown)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("reading a FIFO blocked")
	}
}
