//go:build unix

package client

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"glitched/internal/api"
)

// MarkRead shows its marks at once, while another GUI holds gui.json's
// lock, and saves them all once the lock is free.
func TestMarkReadDoesntWaitForGUIJSON(t *testing.T) {
	dir := stateDir(t)
	m, g := manager(t, dir)
	lk, err := os.OpenFile(filepath.Join(dir, "gui.json.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lk.Close() })
	if err := syscall.Flock(int(lk.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	read := []*Thread{
		{View: api.Thread{ID: api.NewID(), LastTurn: &api.TurnResult{Ended: time.Now()}}},
		{View: api.Thread{ID: api.NewID(), LastTurn: &api.TurnResult{Ended: time.Now()}}},
	}
	marked := make(chan struct{})
	go func() {
		for _, th := range read {
			m.MarkRead(th)
		}
		close(marked)
	}()
	select {
	case <-marked:
	case <-time.After(5 * time.Second):
		t.Fatal("MarkRead waits for gui.json's lock")
	}
	for _, th := range read {
		if m.Unread(th) {
			t.Fatal("unread right after MarkRead")
		}
	}
	lk.Close()
	g.until(t, "both marks saved", func() bool {
		s, err := savedIn(dir)
		return err == nil && !s.Seen[read[0].View.ID].IsZero() && !s.Seen[read[1].View.ID].IsZero()
	})
}
