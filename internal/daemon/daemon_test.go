package daemon

import (
	"context"
	"errors"
	"os"
	"testing"

	"glitched/internal/provider/fake"
)

// stateDir is short: unix socket paths are limited to about 100 bytes.
func stateDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "gl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func start(t *testing.T, cfg Config) *Daemon {
	t.Helper()
	cfg.Hub = true
	cfg.Adapter = fake.New(fake.Turn(true))
	d, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return d
}

func TestStartTwiceOnOneDirIsLocked(t *testing.T) {
	dir := stateDir(t)
	d, err := Start(context.Background(), Config{Dir: dir, Hub: true, Adapter: fake.New(nil)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Start(context.Background(), Config{Dir: dir, Hub: true, Adapter: fake.New(nil)}); !errors.Is(err, ErrLocked) {
		t.Fatalf("a second engine on the dir: %v, want ErrLocked", err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	start(t, Config{Dir: dir})
}
