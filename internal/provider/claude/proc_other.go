//go:build !linux

package claude

import (
	"context"
	"syscall"
)

func sysProcAttr() *syscall.SysProcAttr { return nil }

// stopRun finds no process: only Linux lets the engine read another
// process's environment without help.
func stopRun(context.Context, string) bool { return false }
