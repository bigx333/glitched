//go:build unix

package runtime

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"glitched/internal/gittest"
)

// A cancel while git runs a hook returns at once, though the hook left a
// process holding git's output open, and that process is killed with git.
func TestACanceledGitKillsWhatItsHookStarted(t *testing.T) {
	dir := t.TempDir()
	hooks := filepath.Join(dir, "hooks")
	pidFile := filepath.Join(dir, "pid")
	env := gittest.Env()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"commit", "-q", "--allow-empty", "-m", "start"},
		{"config", "core.hooksPath", hooks},
	} {
		if out, err := gitWith(context.Background(), env, dir, args...); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	os.Mkdir(hooks, 0o755)
	hook := "#!/bin/sh\nsleep 60 &\necho $! > " + pidFile + "\nwait\n"
	if err := os.WriteFile(filepath.Join(hooks, "post-checkout"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := gitWith(ctx, env, dir, "checkout", "-q", "-b", "other")
		done <- err
	}()
	pid := 0
	for deadline := time.Now().Add(10 * time.Second); pid == 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the hook never started its sleep")
		}
		b, _ := os.ReadFile(pidFile)
		pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
	}
	canceled := time.Now()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("the canceled git succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("git is still running 5s after its cancel: the hook's sleep holds its output")
	}
	t.Logf("returned %v after the cancel", time.Since(canceled))
	for deadline := time.Now().Add(5 * time.Second); !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			exec.Command("kill", "-9", strconv.Itoa(pid)).Run()
			t.Fatalf("the hook's sleep %d outlived the canceled git", pid)
		}
	}
}
