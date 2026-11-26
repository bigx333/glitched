package pty

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
)

func open(t *testing.T, ctx context.Context, mgr *Manager, req api.TerminalOpen) (api.TerminalStream, api.TerminalItem) {
	t.Helper()
	st, err := mgr.OpenTerminal(ctx, req)
	if err != nil {
		t.Fatalf("OpenTerminal: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	first, err := st.Next(ctx)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	return st, first
}

func input(t *testing.T, ctx context.Context, mgr *Manager, ws, client, data string) {
	t.Helper()
	if err := mgr.TerminalInput(ctx, api.TerminalInput{Workspace: ws, Client: client, Data: []byte(data)}); err != nil {
		t.Fatalf("TerminalInput %q: %v", data, err)
	}
}

func needBash(t *testing.T) []string {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash:", err)
	}
	return []string{"bash", "--norc", "--noprofile"}
}

func TestEveryItemCarriesTheLeaseHolder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	mgr := New(Options{Command: []string{"/bin/sh"}, HighWatermark: 64, LowWatermark: 32})
	defer mgr.Close()
	ws := t.TempDir()

	a, _ := open(t, ctx, mgr, api.TerminalOpen{Workspace: ws, Client: "a"})
	input(t, ctx, mgr, ws, "a", "echo replayed-$((6*7)) and enough text to span several replay chunks\n")
	readUntilContains(t, ctx, a, "replayed-42")

	b, first := open(t, ctx, mgr, api.TerminalOpen{Workspace: ws, Client: "b"})
	items := []api.TerminalItem{first}
	var seen strings.Builder
	seen.Write(first.Data)
	input(t, ctx, mgr, ws, "a", "echo live-$((6*8))\n")
	for !strings.Contains(seen.String(), "live-48") {
		it, err := b.Next(ctx)
		if err != nil {
			t.Fatalf("Next (saw %q): %v", seen.String(), err)
		}
		items = append(items, it)
		seen.Write(it.Data)
	}
	for i, it := range items {
		if it.LeaseClient != "a" {
			t.Fatalf("item %d (%q) says the lease holder is %q, want a", i, it.Data, it.LeaseClient)
		}
	}
}

func TestReopenPastTheEndReplaysTheSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	mgr := New(Options{Command: []string{"/bin/sh"}})
	defer mgr.Close()
	ws := t.TempDir()

	st, first := open(t, ctx, mgr, api.TerminalOpen{Workspace: ws, Client: "a"})
	input(t, ctx, mgr, ws, "a", "echo before-$((5*5))\n")
	readUntilContains(t, ctx, st, "before-25")
	st.Close()

	short, cancelShort := context.WithTimeout(ctx, 3*time.Second)
	defer cancelShort()
	again, err := mgr.OpenTerminal(short, api.TerminalOpen{Workspace: ws, Session: first.Session, After: 1 << 40, Client: "a"})
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	readUntilContains(t, short, again, "before-25")
}

func TestReattachResizesForTheLeaseHolder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	mgr := New(Options{Command: []string{"/bin/sh"}})
	defer mgr.Close()
	ws := t.TempDir()

	st, first := open(t, ctx, mgr, api.TerminalOpen{Workspace: ws, Cols: 80, Rows: 24, Client: "a"})
	st.Close()

	again, item := open(t, ctx, mgr, api.TerminalOpen{Workspace: ws, Session: first.Session, Cols: 100, Rows: 30, Client: "a"})
	if item.Cols != 100 || item.Rows != 30 {
		t.Errorf("first item after reattach is %dx%d, want 100x30", item.Cols, item.Rows)
	}
	input(t, ctx, mgr, ws, "a", "echo size=$(stty size)\n")
	readUntilContains(t, ctx, again, "size=30 100")
}

func TestWorkspaceKeepsSurroundingSpaces(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ws := filepath.Join(t.TempDir(), "project ")
	if err := os.Mkdir(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	mgr := New(Options{Command: []string{"/bin/sh"}})
	defer mgr.Close()

	st, _ := open(t, ctx, mgr, api.TerminalOpen{Workspace: ws, Client: "a"})
	input(t, ctx, mgr, ws, "a", "printf '[%s]\\n' \"$PWD\"\n")
	readUntilContains(t, ctx, st, "["+ws+"]")
}

// closeWithin runs mgr.Close and fails unless it returns within d.
func closeWithin(t *testing.T, mgr *Manager, d time.Duration) {
	t.Helper()
	done := make(chan struct{})
	start := time.Now()
	go func() { mgr.Close(); close(done) }()
	select {
	case <-done:
		t.Logf("Close took %v", time.Since(start).Round(time.Millisecond))
	case <-time.After(d):
		t.Fatalf("Close still running after %v", d)
	}
}

func TestCloseDoesNotWaitForTheShellsJobs(t *testing.T) {
	shell := needBash(t)
	cases := []struct {
		name, typed, ready string
		need               string
	}{
		{name: "background job", typed: "sleep 4 &\necho started-$((1+1))\n", ready: "started-2"},
		{name: "detached child", typed: "setsid sleep 4 &\necho started-$((1+1))\n", ready: "started-2", need: "setsid"},
		{name: "foreground program", typed: "cat -v\n\x01up\n", ready: "^Aup"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.need != "" {
				if _, err := exec.LookPath(c.need); err != nil {
					t.Skip(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			mgr := New(Options{Command: shell})
			ws := t.TempDir()
			st, _ := open(t, ctx, mgr, api.TerminalOpen{Workspace: ws, Client: "a"})
			input(t, ctx, mgr, ws, "a", c.typed)
			readUntilContains(t, ctx, st, c.ready)
			closeWithin(t, mgr, time.Second)
		})
	}
}

func TestShellExitEndsTheSessionDespiteABackgroundJob(t *testing.T) {
	shell := needBash(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	mgr := New(Options{Command: shell})
	defer mgr.Close()
	ws := t.TempDir()

	st, _ := open(t, ctx, mgr, api.TerminalOpen{Workspace: ws, Client: "a"})
	input(t, ctx, mgr, ws, "a", "sleep 5 &\nexit\n")
	exited, cancelExited := context.WithTimeout(ctx, 2*time.Second)
	defer cancelExited()
	for {
		it, err := st.Next(exited)
		if err != nil {
			t.Fatalf("no exit 2s after `exit`: %v", err)
		}
		if it.Exited {
			return
		}
	}
}

// ptmxFiles counts this process's open descriptors of /dev/ptmx.
func ptmxFiles(t *testing.T) int {
	t.Helper()
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range ents {
		if target, err := os.Readlink(filepath.Join("/proc/self/fd", e.Name())); err == nil && target == "/dev/ptmx" {
			n++
		}
	}
	return n
}

func TestExitedShellsCloseTheirPTY(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reads /proc/self/fd")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	before := ptmxFiles(t)
	mgr := New(Options{Command: []string{"/bin/sh", "-c", "exit 3"}})
	ws := t.TempDir()
	for i := range 10 {
		st, err := mgr.OpenTerminal(ctx, api.TerminalOpen{Workspace: ws, Client: "a"})
		if err != nil {
			t.Fatal(err)
		}
		for {
			it, err := st.Next(ctx)
			if err != nil {
				t.Fatalf("shell %d: no exit: %v", i, err)
			}
			if it.Exited {
				break
			}
		}
		st.Close()
	}
	if err := mgr.Close(); err != nil {
		t.Fatal(err)
	}
	if leaked := ptmxFiles(t) - before; leaked != 0 {
		t.Fatalf("%d /dev/ptmx descriptors left open after 10 exited shells and Close", leaked)
	}
}

func TestPausedWatcherSeesLeaseReleaseWhileQuiet(t *testing.T) {
	ws := t.TempDir()
	mgr := New(Options{
		Command:       []string{"/bin/sh", "-c", "sleep 60"},
		ReplayBytes:   64 << 10,
		HighWatermark: 1024,
		LowWatermark:  512,
	})
	defer mgr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	owner, err := mgr.OpenTerminal(ctx, api.TerminalOpen{Workspace: ws, Client: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if _, err := owner.Next(ctx); err != nil {
		t.Fatal(err)
	}

	watcher, err := mgr.OpenTerminal(ctx, api.TerminalOpen{Workspace: ws, Client: "watcher"})
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.Close()
	if _, err := watcher.Next(ctx); err != nil {
		t.Fatal(err)
	}

	// Fill watcher past highWatermark so it pauses.
	sub := watcher.(*subStream)
	sess := sub.sess
	chunk := make([]byte, 1200)
	for i := range chunk {
		chunk[i] = 'x'
	}
	sess.mu.Lock()
	end := sess.ring.write(chunk)
	sub.enqueueBytesLocked(chunk, end-uint64(len(chunk)), false, false)
	sub.signalLocked()
	paused := sub.paused
	sess.mu.Unlock()
	if !paused {
		t.Fatal("expected watcher to pause at high watermark")
	}

	// Release owner while watcher is paused and the shell stays quiet.
	mgr.ReleaseTerminalClient("owner")

	// Drain watcher: it must receive a frame reporting the released lease ("").
	sawRelease := false
	drainCtx, drainCancel := context.WithTimeout(ctx, 2*time.Second)
	defer drainCancel()
	for !sawRelease {
		it, err := watcher.Next(drainCtx)
		if err != nil {
			t.Fatalf("watcher never saw lease release after unpausing: %v", err)
		}
		if it.LeaseClient == "" {
			sawRelease = true
		}
	}
}
