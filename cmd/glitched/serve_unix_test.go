//go:build unix

package main

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"maps"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/daemon"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
	"glitched/internal/transport"
)

func TestServeKeepsTheStateDirAndSocketPrivate(t *testing.T) {
	dir, err := os.MkdirTemp("", "gl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	served := make(chan error, 1)
	go func() { served <- serve(ctx, []string{"-dir", dir}) }()
	defer func() { cancel(); <-served }()
	ep := transport.NewEndpointClient(daemon.Socket(dir), api.ID{})
	for {
		if _, err := ep.Info(ctx); err == nil {
			break
		}
		select {
		case err := <-served:
			t.Fatalf("serve exited: %v", err)
		case <-time.After(20 * time.Millisecond):
		}
	}

	for path, want := range map[string]fs.FileMode{dir: 0o700, daemon.Socket(dir).Address: 0o600} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s has mode %o, want %o", path, got, want)
		}
	}
}

// A serve whose -listen port another process holds refuses to start: it
// says where and why in one line, exits non-zero, and leaves its state as
// it found it, the run its last stop cut off and the setting to continue
// it included.
func TestServeRefusesAPortInUseAndChangesNothing(t *testing.T) {
	dir, err := os.MkdirTemp("", "gl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	cutOff(t, dir)
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	addr := held.Addr().String()
	before := files(t, dir)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "serve", "-dir", dir, "-listen", addr)
	cmd.Env = append(os.Environ(), "GLITCHED_TEST_MAIN=1")
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() == 0 {
		t.Errorf("serve on a port in use: %v, want a non-zero exit", err)
	}
	if want := "glitched: can't listen on " + addr + ": address already in use\n"; string(out) != want {
		t.Errorf("serve printed %q, want %q", out, want)
	}
	after := files(t, dir)
	var changed []string
	for path := range maps.Keys(before) {
		if b, ok := after[path]; !ok || !bytes.Equal(b, before[path]) {
			changed = append(changed, path)
		}
	}
	for path := range maps.Keys(after) {
		if _, ok := before[path]; !ok {
			changed = append(changed, path)
		}
	}
	if len(changed) > 0 {
		slices.Sort(changed)
		t.Errorf("the refused serve changed or added %v", changed)
	}
}

// cutOff leaves in dir a Claude Code thread whose run had started when its
// engine stopped, and the setting that has the next engine continue it.
func cutOff(t *testing.T, dir string) {
	t.Helper()
	ad := fake.New(fake.Turn(false))
	ad.HoldStart = true
	cfg := daemon.Config{Dir: dir, Hub: true, Epoch: 1, ContinueAfterRestart: true, Providers: provider.Mux{"claude": ad}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	d, err := daemon.Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	ep := d.Endpoint()
	id := api.NewID()
	for _, body := range []api.Intent{
		api.ThreadCreate{Workspace: t.TempDir(), Title: "cut off", Model: api.ModelRef{Provider: "claude", Model: "claude"}},
		api.MessageDispatch{Text: "work", Mode: api.StartNow},
	} {
		if _, err := ep.Submit(context.Background(), api.Command{ID: api.NewID(), Target: id, Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	for deadline := time.Now().Add(10 * time.Second); ad.Starts() == 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the run never started")
		}
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
}

// files is every file under dir, by path, with its bytes; a socket has
// none.
func files(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	got := map[string][]byte{}
	err := filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		if !e.Type().IsRegular() {
			got[path] = nil
			return nil
		}
		b, err := os.ReadFile(path)
		got[path] = b
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}
