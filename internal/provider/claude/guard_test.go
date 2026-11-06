//go:build !claudelive

package claude_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/claude"
)

// signedIn is a claude on PATH that leaves a mark if it runs.
func signedIn(t *testing.T) (bin, ran string) {
	t.Helper()
	dir := t.TempDir()
	ran = filepath.Join(t.TempDir(), "ran")
	bin = filepath.Join(dir, "claude")
	if runtime.GOOS == "windows" {
		bin += ".exe" // PATH finds no program without its extension
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\ntouch "+ran+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return bin, ran
}

func wantPanic(t *testing.T, how, ran string) {
	t.Helper()
	if recover() == nil {
		t.Errorf("%s in a test without the claudelive tag, want a panic", how)
	}
	if _, err := os.Stat(ran); err == nil {
		t.Errorf("%s ran it", how)
	}
}

func TestATestCannotRunARealClaude(t *testing.T) {
	_, ran := signedIn(t)
	defer wantPanic(t, "Open found the claude on PATH", ran)
	(&claude.Adapter{}).Open(ctx, provider.SessionSpec{Thread: api.NewID(), Run: api.NewID(), Workspace: t.TempDir()})
}

func TestATestCannotRunClaudeThroughCommand(t *testing.T) {
	bin, ran := signedIn(t)
	defer wantPanic(t, "Command was given "+bin, ran)
	claude.Command(ctx, bin, "--version").Run()
}

// A program a test starts is not under go test, so the test tells it. The
// program, a daemon say, fails that turn and keeps running.
func TestAProgramATestRunsCannotRunARealClaude(t *testing.T) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go to build the helper with")
	}
	bin, ran := signedIn(t)
	helper := filepath.Join(t.TempDir(), "guardhelper")
	if runtime.GOOS == "windows" {
		helper += ".exe" // exec finds no program without its extension
	}
	if out, err := exec.Command(gobin, "build", "-o", helper, "./testdata/guardhelper").CombinedOutput(); err != nil {
		t.Fatalf("build the helper: %v\n%s", err, out)
	}
	out, err := exec.Command(helper, bin).CombinedOutput()
	if _, statErr := os.Stat(ran); statErr == nil {
		t.Errorf("a program the test ran ran %s", bin)
	}
	if err != nil {
		t.Errorf("the helper exited %v with %q, want it to carry on", err, out)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for i, step := range []string{"open: ", "command: "} {
		if i >= len(lines) || !strings.HasPrefix(lines[i], step) || !strings.Contains(lines[i], "claudelive") {
			t.Errorf("the helper said %q, want %sto fail with the guard's error", out, step)
		}
	}
}
