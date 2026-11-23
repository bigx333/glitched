//go:build unix

package daemon

import (
	"os/exec"
	"syscall"
)

// ownGroup starts cmd in a process group of its own, which a cancel kills
// whole, and returns what kills what's left of it once cmd has exited.
func ownGroup(cmd *exec.Cmd) (end func()) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	return func() {
		if cmd.Process != nil {
			syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	}
}
