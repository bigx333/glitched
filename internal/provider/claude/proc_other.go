//go:build !linux && !windows && !darwin

package claude

import (
	"context"
	"os"
	"os/exec"
	"syscall"
)

func sysProcAttr() *syscall.SysProcAttr { return nil }

func contain(*exec.Cmd, mark) {}

// stopRun finds no process: the engine reads no other process's
// environment here.
func stopRun(context.Context, mark) (bool, error) { return false, nil }

// stopTools finds no process either, as stopRun.
func stopTools(context.Context, mark, int) error { return nil }

func confine(*exec.Cmd, mark) error { return nil }

func terminate(p *os.Process) error { return p.Signal(syscall.SIGTERM) }
