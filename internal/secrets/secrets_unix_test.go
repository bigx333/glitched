//go:build unix

package secrets

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"glitched/internal/api"
)

// A thread's environment script that is no regular file, as a FIFO whose
// reading would wait for a writer, is refused, by Prepare and by Held,
// and keeps neither waiting.
func TestAScriptNoRegularFileIsRefused(t *testing.T) {
	dir := t.TempDir()
	th := api.NewID()
	v := New(dir)
	if err := v.Put(th, map[string]string{"KEY": "value-1"}); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(v.Dir(th), EnvScript)
	if err := syscall.Unlink(fifo); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	cold := New(dir)
	prepared := make(chan error, 1)
	go func() {
		_, err := cold.Prepare(th)
		prepared <- err
	}()
	select {
	case err := <-prepared:
		if err == nil {
			t.Error("Prepare took a FIFO for the environment script")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Prepare waits on a FIFO")
	}
	if h := scanned(t, cold); h.Complete {
		t.Errorf("Held is %+v with a FIFO for a script, want incomplete", h)
	}
}
