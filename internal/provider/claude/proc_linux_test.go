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

// execd is whether pid runs sleep 60.
func execd(pid int) bool {
	cmd, _ := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	return bytes.Equal(cmd, []byte("/bin/sleep\x0060\x00"))
}

// needPIDPick skips the test where the kernel lets no PID namespace pick
// its next PID, as reuse does.
func needPIDPick(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/proc/sys/kernel/ns_last_pid"); err != nil {
		t.Skipf("no ns_last_pid to pick the next PID with: %v", err)
	}
}

// reuse ends old and starts in its PID a process that sleeps with env.
func reuse(t *testing.T, old *child, env ...string) *child {
	t.Helper()
	syscall.Kill(old.Process.Pid, syscall.SIGKILL)
	<-old.exited
	if err := os.WriteFile("/proc/sys/kernel/ns_last_pid", []byte(strconv.Itoa(old.Process.Pid-1)), 0o600); err != nil {
		t.Fatal(err)
	}
	other := sleeper(t, nil, env...)
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

// A process found with a launch's marker can exit before it is signalled,
// and its PID go to another process. That one is not signalled.
func TestAReusedPIDIsNotSignalled(t *testing.T) {
	needPIDPick(t)
	if !inPIDNamespace(t) {
		return
	}
	m := launchMark(api.NewID(), api.NewID())
	old := sleeper(t, nil, procEnv+"="+string(m))
	found := sweep{m: m}.marked()
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
	needPIDPick(t)
	if !inPIDNamespace(t) {
		return
	}
	m := launchMark(api.NewID(), api.NewID())
	old := sleeper(t, nil, procEnv+"="+string(m))
	deny(t, unix.SYS_PIDFD_OPEN, unix.EPERM)
	found := sweep{m: m}.marked()
	other := reuse(t, old)
	signal(found, syscall.SIGTERM)
	if signalled(other) {
		t.Error("a process that took a marked process's PID was signalled")
	}
}

// Reconcile fails while a process of the thread is left, so it is retried.
func TestReconcileFailsWhileAProcessOfTheThreadIsLeft(t *testing.T) {
	if !inPIDNamespace(t) {
		return
	}
	thread := api.NewID()
	tool := sleeper(t, nil, procEnv+"="+string(launchMark(thread, api.NewID())))
	deny(t, unix.SYS_PIDFD_SEND_SIGNAL, unix.EPERM)
	_, err := (&Adapter{}).Reconcile(context.Background(), provider.RuntimeIdentity{Thread: thread, Run: api.NewID()})
	if left := carries(tool.Process.Pid, threadMark(thread)); left && err == nil {
		t.Error("Reconcile succeeded with a process of the thread still running")
	}
}
