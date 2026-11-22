package claude_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/claude"
)

// hidden hides this process's environment from other processes, writes
// its PID to pidfile, and sleeps.
func hidden(pidfile string) int {
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return 2
	}
	os.WriteFile(pidfile, []byte(strconv.Itoa(os.Getpid())), 0o644)
	time.Sleep(time.Hour)
	return 0
}

// stopped is whether pid, which the engine has killed, exits soon. It
// may not be reaped: a process whose parent is gone is init's to reap,
// and init takes its time or never does.
func stopped(pid int) bool {
	for deadline := time.Now().Add(3 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		_, state, _ := strings.Cut(string(stat), ") ")
		if err != nil || strings.HasPrefix(state, "Z") {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
	}
}

// userScopes skips t where the engine has no user systemd manager on a
// cgroup v2 hierarchy to put a run's processes in a scope of their own.
func userScopes(t *testing.T) {
	t.Helper()
	var fs unix.Statfs_t
	if err := unix.Statfs("/sys/fs/cgroup", &fs); err != nil || fs.Type != unix.CGROUP2_SUPER_MAGIC {
		t.Skip("no cgroup v2 hierarchy at /sys/fs/cgroup, so a run's processes are found by their marker alone")
	}
	out, err := exec.Command("systemctl", "--user", "show", "--property=ControlGroup", "--value").Output()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		t.Skipf("no user systemd manager (%v), so a run's processes are found by their marker alone", err)
	}
}

// escapes is each way a tool can leave the run's marker behind: each
// writes to $PIDFILE the PID of a process that sleeps without it.
var escapes = map[string]func(pidfile string) []string{
	"unset": func(f string) []string {
		return []string{"env", "-u", "GLITCHED_CLAUDE_PROCESS", "sh", "-c", "echo $$ > '" + f + "'; exec sleep 60"}
	},
	"changed": func(f string) []string {
		return []string{"env", "GLITCHED_CLAUDE_PROCESS=another", "sh", "-c", "echo $$ > '" + f + "'; exec sleep 60"}
	},
	"cleared, in a new session, forked twice": func(f string) []string {
		return []string{"sh", "-c", "env -i setsid sh -c 'echo $$ > \"" + f + "\"; exec sleep 60' &"}
	},
	"undumpable": func(f string) []string {
		return []string{"env", envHide + "=" + f, must(os.Executable())}
	},
}

// escaped starts a run whose tool leaves the marker behind as escape says,
// and returns the agent, the thread, its fake claude, and the escaped
// process.
func escaped(t *testing.T, escape func(string) []string) (ag provider.Agent, thread api.ID, claudePID, pid int) {
	t.Helper()
	ws := t.TempDir()
	pidfile := filepath.Join(ws, "pid")
	tool, _ := json.Marshal(map[string]any{"orphan": escape(pidfile)})
	tape := filepath.Join(ws, "escape.jsonl")
	head := must(os.ReadFile(filepath.Join("testdata", "orphan-tool.jsonl")))
	lines := strings.Split(strings.TrimSpace(string(head)), "\n")
	data := strings.Join(lines[:4], "\n") + "\n" + string(tool) + "\n{\"hang\":true}\n"
	if err := os.WriteFile(tape, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envFake, tape)
	fake := processes(t)
	thread = api.NewID()
	ag, err := (&claude.Adapter{Path: must(os.Executable())}).Open(ctx, provider.SessionSpec{Thread: thread, Run: api.NewID(), Workspace: ws})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ag.Close(context.Background()) })
	if err := ag.Do(ctx, provider.Start{Text: "Run sleep 60."}); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(turnWait); pid == 0; time.Sleep(5 * time.Millisecond) {
		b, _ := os.ReadFile(pidfile)
		pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		if time.Now().After(deadline) {
			t.Fatal("the tool never started its process")
		}
	}
	t.Cleanup(func() { syscall.Kill(pid, syscall.SIGKILL) })
	return ag, thread, fake("claude"), pid
}

// A tool's process that drops or changes the run's marker, or hides it, is
// still the run's: Close stops it.
func TestCloseStopsAProcessThatLeftTheMarkerBehind(t *testing.T) {
	userScopes(t)
	for name, escape := range escapes {
		t.Run(name, func(t *testing.T) {
			ag, _, _, pid := escaped(t, escape)
			if err := ag.Close(ctx); err != nil {
				t.Errorf("Close: %v", err)
			}
			if !stopped(pid) {
				t.Error("the tool's process still runs after Close")
			}
		})
	}
}

// The same process outlives a claude that died with its engine. The next
// engine's Reconcile stops it, knowing nothing but the thread.
func TestReconcileStopsAProcessThatLeftTheMarkerBehind(t *testing.T) {
	userScopes(t)
	for name, escape := range escapes {
		t.Run(name, func(t *testing.T) {
			_, thread, claudePID, pid := escaped(t, escape)
			syscall.Kill(claudePID, syscall.SIGKILL)
			if !exits(claudePID, 3*time.Second) || exits(pid, 0) {
				t.Fatal("want claude gone and the tool's process still running")
			}
			if _, err := (&claude.Adapter{}).Reconcile(ctx, provider.RuntimeIdentity{Thread: thread}); err != nil {
				t.Errorf("Reconcile: %v", err)
			}
			if !stopped(pid) {
				t.Error("the tool's process still runs after Reconcile")
			}
		})
	}
}

// Without a user systemd manager, claude starts in no scope, and the
// marker still finds a tool that keeps it, after the engine is gone.
func TestWithoutScopesReconcileStopsAMarkedTool(t *testing.T) {
	claude.WithoutScopes(t)
	unscoped(t)
}

// Where systemd-run is refused a scope, as by polkit, claude starts
// without one, as where there is no user systemd manager.
func TestARefusedScopeLeavesTheRunToItsMarker(t *testing.T) {
	userScopes(t)
	bin := t.TempDir()
	refuse := "#!/bin/sh\necho 'Failed to start transient scope unit: Access denied' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "systemd-run"), []byte(refuse), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	claude.RedetectScopes(t)
	unscoped(t)
}

// unscoped starts a run whose tool keeps the marker, checks claude is in
// no scope, and has Reconcile stop the tool once claude is gone.
func unscoped(t *testing.T) {
	t.Helper()
	ws := t.TempDir()
	replay(t, "orphan-tool", "")
	fake := processes(t)
	thread := api.NewID()
	ag, err := (&claude.Adapter{Path: must(os.Executable())}).Open(ctx, provider.SessionSpec{Thread: thread, Run: api.NewID(), Workspace: ws})
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close(ctx)
	if err := ag.Do(ctx, provider.Start{Text: "Run sleep 60."}); err != nil {
		t.Fatal(err)
	}
	tool := waitFor(t, fake, "tool")
	cg, _ := os.ReadFile("/proc/" + strconv.Itoa(fake("claude")) + "/cgroup")
	if strings.Contains(string(cg), "glitched-claude-") {
		t.Errorf("claude started in a scope: %s", cg)
	}
	syscall.Kill(fake("claude"), syscall.SIGKILL)
	if !exits(fake("claude"), 3*time.Second) || exits(tool, 0) {
		t.Fatal("want claude gone and its tool still running")
	}
	if _, err := (&claude.Adapter{}).Reconcile(ctx, provider.RuntimeIdentity{Thread: thread}); err != nil {
		t.Errorf("Reconcile: %v", err)
	}
	if !stopped(tool) {
		t.Error("the tool still runs after Reconcile")
	}
}
