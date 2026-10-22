//go:build unix

package main

import (
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
)

func TestLockSurvivesGC(t *testing.T) {
	path := filepath.Join(t.TempDir(), "engine.lock")
	lk, err := lock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer lk.Close()
	runtime.GC()
	runtime.GC()
	// A second open file description conflicts with a held flock just as a
	// second engine process would.
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
		t.Fatal("a second locker took the engine lock after a GC")
	}
}
