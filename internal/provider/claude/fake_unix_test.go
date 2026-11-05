//go:build unix

package claude_test

import (
	"errors"
	"os/exec"
	"syscall"
)

// command is a process the fake claude starts, in a session of its own,
// as Claude Code runs a tool call.
func command(argv []string) *exec.Cmd {
	c := exec.Command(argv[0], argv[1:]...)
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return c
}

func kill(pid int) { syscall.Kill(pid, syscall.SIGKILL) }

// gone is whether pid has exited and been reaped.
func gone(pid int) bool { return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) }
