//go:build unix

package claude_test

import (
	"os/exec"
	"syscall"
)

func newSession(c *exec.Cmd) { c.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
