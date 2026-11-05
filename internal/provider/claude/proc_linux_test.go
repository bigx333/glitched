package claude

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"glitched/internal/api"
	"glitched/internal/provider"

	"golang.org/x/sys/unix"
)

// inPIDNamespace runs the calling test again as PID 1 of a PID namespace
// of its own, where it may pick the next PID, and reports whether this is
// that run. It skips the test where the kernel gives no unprivileged user
// a namespace.
func inPIDNamespace(t *testing.T) bool {
	t.Helper()
	if os.Getpid() == 1 {
		return true
	}
	out, err := exec.Command("unshare", "--user", "--map-root-user", "--pid", "--fork", "--mount-proc",
		os.Args[0], "-test.run=^"+t.Name()+"$", "-test.count=1").CombinedOutput()
	switch {
	case bytes.Contains(out, []byte("--- FAIL")):
		t.Errorf("in a PID namespace of its own:\n%s", out)
	case err != nil:
		t.Skipf("no PID namespace: %v\n%s", err, out)
	}
	return false
}

// child is a started process. Only its own goroutine waits for it, and
// closes exited once it is reaped.
type child struct {
	*exec.Cmd
	exited chan struct{}
}

// sleeper starts a process that sleeps with env as its environment, and
// waits until it has exec'd: until then, it has this process's environment.
func sleeper(t *testing.T, env ...string) *child {
	t.Helper()
	c := exec.Command("/bin/sleep", "60")
	c.Env = env
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
	cmdline := "/proc/" + strconv.Itoa(c.Process.Pid) + "/cmdline"
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		if cmd, _ := os.ReadFile(cmdline); bytes.Equal(cmd, []byte("/bin/sleep\x0060\x00")) {
			return p
		}
	}
	t.Fatal("the process never exec'd")
	return nil
}

// reuse ends old and starts in its PID a process that sleeps with env.
func reuse(t *testing.T, old *child, env ...string) *child {
	t.Helper()
	syscall.Kill(old.Process.Pid, syscall.SIGKILL)
	<-old.exited
	if err := os.WriteFile("/proc/sys/kernel/ns_last_pid", []byte(strconv.Itoa(old.Process.Pid-1)), 0o600); err != nil {
		t.Fatal(err)
	}
	other := sleeper(t, env...)
	if other.Process.Pid != old.Process.Pid {
		t.Fatalf("the new process got PID %d, want %d again", other.Process.Pid, old.Process.Pid)
	}
	return other
}

// signalled is whether p ends within 100 ms.
func signalled(p *child) bool {
	select {
	case <-p.exited:
		return true
	case <-time.After(100 * time.Millisecond):
		return false
	}
}

// deny has the syscall nr fail with errno on this test's OS thread, which
// it never gives back: the thread ends with the test.
func deny(t *testing.T, nr uintptr, errno syscall.Errno) {
	t.Helper()
	runtime.LockOSThread()
	filter := []unix.SockFilter{
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 0},
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: uint32(nr), Jf: 1},
		{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ERRNO | uint32(errno)},
		{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ALLOW},
	}
	prog := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := unix.Prctl(unix.PR_SET_SECCOMP, unix.SECCOMP_MODE_FILTER, uintptr(unsafe.Pointer(&prog)), 0, 0); err != nil {
		t.Fatal(err)
	}
}

// A process found with the run's marker can exit before it is signalled,
// and its PID go to another process. That one is not signalled.
func TestAReusedPIDIsNotSignalled(t *testing.T) {
	if !inPIDNamespace(t) {
		return
	}
	old := sleeper(t, runEnv+"=run")
	found := marked("run")
	if len(found) != 1 {
		t.Fatalf("found %d marked processes, want the one", len(found))
	}
	other := reuse(t, old)
	signal(found, syscall.SIGTERM)
	if signalled(other) {
		t.Error("a process that took a marked process's PID was signalled")
	}
}

// A kernel that has pidfds but will not give one now leaves the process
// to a later scan, rather than to a signal by PID, which can reach another
// process that took the PID since.
func TestAProcessWithoutAPidfdIsNotSignalledByPID(t *testing.T) {
	if !inPIDNamespace(t) {
		return
	}
	old := sleeper(t, runEnv+"=run")
	deny(t, unix.SYS_PIDFD_OPEN, unix.EPERM)
	found := marked("run")
	other := reuse(t, old)
	signal(found, syscall.SIGTERM)
	if signalled(other) {
		t.Error("a process that took a marked process's PID was signalled")
	}
}

// Reconcile fails while a process of the run is left, so it is retried.
func TestReconcileFailsWhileAProcessOfTheRunIsLeft(t *testing.T) {
	if !inPIDNamespace(t) {
		return
	}
	run := api.NewID()
	tool := sleeper(t, runEnv+"="+run.String())
	deny(t, unix.SYS_PIDFD_SEND_SIGNAL, unix.EPERM)
	_, err := (&Adapter{}).Reconcile(context.Background(), provider.RuntimeIdentity{Run: run})
	if left := carries(tool.Process.Pid, marker(run.String())); left && err == nil {
		t.Error("Reconcile succeeded with a process of the run still running")
	}
}
