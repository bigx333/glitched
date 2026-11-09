//go:build unix

package client

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// holdEngineLock takes the lock an engine with state in dir holds while it
// runs, so a GUI that finds the engine out of reach waits for it rather
// than embedding its own.
func holdEngineLock(t *testing.T, dir string) {
	t.Helper()
	lk, err := os.OpenFile(filepath.Join(dir, "engine.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lk.Close() })
	if err := syscall.Flock(int(lk.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
}

// A GUI that retries while the engine under its dir is out of reach, the
// engine still holding its lock, waits for that engine and comes back to
// its threads; it embeds no engine of its own over the socket.
func TestAnEngineOutOfReachIsWaitedForNotReplaced(t *testing.T) {
	b, _, m, g, e, top := treeFixture(t)
	close(b.hold)
	b.drop()
	g.until(t, "offline", func() bool { _, off := e.Status.(Offline); return off })
	engineUntil(t, b, "a retry while out of reach", func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.refused > 0
	})
	b.back()
	online(t, g, e)
	if m.Machine().Embedded || e.Threads[top] == nil {
		t.Fatalf("back online: embedded %v, the top shown %v; want the engine that held the lock, with its thread", m.Machine().Embedded, e.Threads[top] != nil)
	}
}
