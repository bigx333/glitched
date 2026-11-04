package claude

import (
	"bytes"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"
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

// A process found with the run's marker can exit before it is signalled,
// and its PID go to another process. That one is not signalled.
func TestAReusedPIDIsNotSignalled(t *testing.T) {
	if !inPIDNamespace(t) {
		return
	}
	sleep := func(env ...string) *exec.Cmd {
		c := exec.Command("/bin/sleep", "60")
		c.Env = env
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		return c
	}
	old := sleep(runEnv + "=run")
	// Until it execs, the child has this process's environment.
	environ := "/proc/" + strconv.Itoa(old.Process.Pid) + "/environ"
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		if env, _ := os.ReadFile(environ); bytes.Contains(env, []byte(runEnv)) {
			break
		}
	}
	found := marked("run")
	if len(found) != 1 {
		t.Fatalf("found %d marked processes, want the one", len(found))
	}
	old.Process.Kill()
	old.Wait()
	if err := os.WriteFile("/proc/sys/kernel/ns_last_pid", []byte(strconv.Itoa(old.Process.Pid-1)), 0o600); err != nil {
		t.Fatal(err)
	}
	other := sleep()
	defer other.Process.Kill()
	if other.Process.Pid != old.Process.Pid {
		t.Fatalf("the new process got PID %d, want %d again", other.Process.Pid, old.Process.Pid)
	}
	signal(found, syscall.SIGTERM)
	exited := make(chan struct{})
	go func() { other.Wait(); close(exited) }()
	select {
	case <-exited:
		t.Error("a process that took a marked process's PID was signalled")
	case <-time.After(100 * time.Millisecond):
	}
}
