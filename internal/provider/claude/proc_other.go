//go:build !linux

package claude

import (
	"context"
	"os/exec"
	"syscall"
)

func sysProcAttr() *syscall.SysProcAttr { return nil }

func contain(*exec.Cmd, string) {}

// stopRun finds no process: only Linux lets the engine read another
// process's environment without help.
func stopRun(context.Context, string) (bool, error) { return false, nil }
