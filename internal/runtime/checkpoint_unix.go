//go:build unix

package runtime

import (
	"os/exec"
	"syscall"
	"time"
)

func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}

// reap kills what is left of the group of cmd, which ran, and waits until
// none of it is, for at most cmd's WaitDelay.
func reap(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	for deadline := time.Now().Add(cmd.WaitDelay); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		if syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) != nil {
			return
		}
	}
}
