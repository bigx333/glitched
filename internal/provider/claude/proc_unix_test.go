//go:build linux || darwin

package claude

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"glitched/internal/api"
)

// child is a started process. Only its own goroutine waits for it, and
// closes exited once it is reaped.
type child struct {
	*exec.Cmd
	exited chan struct{}
}

// sleeper starts a process that sleeps with attr and with env as its
// environment, and waits until it has exec'd: until then, it has this
// process's environment.
func sleeper(t *testing.T, attr *syscall.SysProcAttr, env ...string) *child {
	t.Helper()
	c := exec.Command("/bin/sleep", "60")
	c.Env = env
	c.SysProcAttr = attr
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	p := &child{Cmd: c, exited: make(chan struct{})}
	go func() { c.Wait(); close(p.exited) }()
	t.Cleanup(func() {
		select {
		case <-p.exited:
		default:
			// By PID: a test may deny this thread pidfd signals, which Kill uses.
			syscall.Kill(c.Process.Pid, syscall.SIGKILL)
			<-p.exited
		}
	})
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		if execd(c.Process.Pid) {
			return p
		}
	}
	t.Fatal("the process never exec'd")
	return nil
}

// helperEnv names the file a test binary started as claude writes the
// PIDs of what it started to, and stubbornEnv has its tool ignore SIGTERM.
const (
	helperEnv   = "GLITCHED_PROC_HELPER"
	stubbornEnv = "GLITCHED_PROC_STUBBORN"
)

// TestHelperClaude plays claude for launch: it starts an MCP server in its
// process group and a tool shell in a session of its own, both sleep, which
// on macOS shows no other process its environment, then sleeps.
func TestHelperClaude(t *testing.T) {
	pidfile := os.Getenv(helperEnv)
	if pidfile == "" {
		t.Skip("run by launch alone")
	}
	var pids []string
	for i, attr := range []*syscall.SysProcAttr{nil, {Setsid: true}} {
		c := exec.Command("/bin/sleep", "60")
		if i == 1 && os.Getenv(stubbornEnv) != "" {
			c = exec.Command("/bin/sh", "-c", "trap '' TERM; exec /bin/sleep 60")
		}
		c.SysProcAttr = attr
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		go c.Wait()
		for !execd(c.Process.Pid) {
			time.Sleep(time.Millisecond)
		}
		pids = append(pids, strconv.Itoa(c.Process.Pid))
	}
	os.WriteFile(pidfile, []byte(strings.Join(pids, " ")), 0o600)
	time.Sleep(time.Minute)
}

// launched is a claude TestHelperClaude plays, and the server and tool it
// started.
type launched struct {
	claude       *child
	server, tool int
}

// launch starts TestHelperClaude as m's claude, the leader of its process
// group, as start runs claude.
func startHelper(t *testing.T, m mark, env ...string) launched {
	t.Helper()
	pidfile := filepath.Join(t.TempDir(), "pids")
	c := exec.Command(os.Args[0], "-test.run=^TestHelperClaude$", "-test.count=1")
	c.Env = append([]string{procEnv + "=" + string(m), helperEnv + "=" + pidfile}, env...)
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	p := &child{Cmd: c, exited: make(chan struct{})}
	go func() { c.Wait(); close(p.exited) }()
	var l launched
	t.Cleanup(func() {
		for _, pid := range []int{l.server, l.tool, c.Process.Pid} {
			if pid > 0 {
				syscall.Kill(pid, syscall.SIGKILL)
			}
		}
		<-p.exited
	})
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		b, _ := os.ReadFile(pidfile)
		if n, _ := fmt.Sscan(string(b), &l.server, &l.tool); n == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the helper claude never started its server and tool")
		}
	}
	l.claude = p
	return l
}

// alive is whether pid still runs, within 2 s for want false.
func alive(pid int, want bool) bool {
	for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		up := syscall.Kill(pid, 0) == nil
		if up == want || time.Now().After(deadline) {
			return up
		}
	}
}

// gone is whether the launch, claude and all it started, ends within 2 s.
func (l launched) gone() bool {
	select {
	case <-l.claude.exited:
	case <-time.After(2 * time.Second):
		return false
	}
	return !alive(l.server, false) && !alive(l.tool, false)
}

// running is whether claude and all it started still run.
func (l launched) running() bool {
	select {
	case <-l.claude.exited:
		return false
	default:
	}
	return alive(l.server, true) && alive(l.tool, true)
}

// A thread's mark finds every launch of its thread, what each started
// included, and no other thread's.
func TestAThreadsMarkStopsEveryLaunchOfItsThreadAlone(t *testing.T) {
	thread, other := api.NewID(), api.NewID()
	first := startHelper(t, launchMark(thread, api.NewID()))
	second := startHelper(t, launchMark(thread, api.NewID()))
	theirs := startHelper(t, launchMark(other, api.NewID()))
	found, err := stopRun(context.Background(), threadMark(thread))
	if !found || err != nil {
		t.Fatalf("stopRun = %v, %v, want the thread's processes found and stopped", found, err)
	}
	if !first.gone() || !second.gone() {
		t.Error("a process of the thread still runs")
	}
	if !theirs.running() {
		t.Error("a process of another thread was stopped")
	}
}

// A launch's mark finds that launch alone, not another of its thread.
func TestALaunchsMarkStopsThatLaunchAlone(t *testing.T) {
	thread := api.NewID()
	m := launchMark(thread, api.NewID())
	mine := startHelper(t, m)
	sibling := startHelper(t, launchMark(thread, api.NewID()))
	if found, err := stopRun(context.Background(), m); !found || err != nil {
		t.Fatalf("stopRun = %v, %v, want the launch found and stopped", found, err)
	}
	if !mine.gone() {
		t.Error("a process of the launch still runs")
	}
	if !sibling.running() {
		t.Error("another launch of the thread was stopped")
	}
}

// stopTools ends the tool shell, in a session of its own, and leaves
// claude, its group's leader, and the MCP server in its group running.
func TestStopToolsSparesClaudesGroup(t *testing.T) {
	m := launchMark(api.NewID(), api.NewID())
	l := startHelper(t, m)
	if err := stopTools(context.Background(), m, l.claude.Process.Pid); err != nil {
		t.Fatalf("stopTools: %v", err)
	}
	if alive(l.tool, false) {
		t.Error("the tool shell still runs")
	}
	select {
	case <-l.claude.exited:
		t.Error("stopTools stopped claude")
	default:
	}
	if !alive(l.server, true) {
		t.Error("stopTools stopped the MCP server in claude's group")
	}
}

// A tool that ignores SIGTERM outlives claude, which exits on it, and is
// still killed once killGrace passes.
func TestStopRunKillsAToolThatOutlivesClaude(t *testing.T) {
	m := launchMark(api.NewID(), api.NewID())
	l := startHelper(t, m, stubbornEnv+"=1")
	if found, err := stopRun(context.Background(), m); !found || err != nil {
		t.Fatalf("stopRun = %v, %v, want the launch found and stopped", found, err)
	}
	if !l.gone() {
		t.Error("a process of the launch still runs")
	}
}
