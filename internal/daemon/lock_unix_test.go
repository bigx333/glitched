//go:build unix

package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"syscall"
	"testing"
)

func TestLockSurvivesGC(t *testing.T) {
	path := filepath.Join(t.TempDir(), "engine.lock")
	unlock, err := lock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
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

// An engine closing while the process starts programs leaves the lock free
// for the next engine at once: a forked child shares the lock's file until
// it execs, and must not keep the lock.
func TestALockIsFreeOnceUnlockedWhileTheProcessForks(t *testing.T) {
	var stop atomic.Bool
	t.Cleanup(func() { stop.Store(true) })
	for range 2 {
		go func() {
			for !stop.Load() {
				exec.Command("true").Run()
			}
		}()
	}
	path := filepath.Join(t.TempDir(), "engine.lock")
	for i := range 1000 {
		unlock, err := lock(path)
		if err != nil {
			t.Fatalf("lock %d after an unlock: %v", i, err)
		}
		if err := unlock(); err != nil {
			t.Fatal(err)
		}
	}
}
