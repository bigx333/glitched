//go:build !linux && !darwin

package acp

import (
	"context"
	"syscall"
)

func sysProcAttr() *syscall.SysProcAttr { return nil }

// stopMarked finds no process: the engine reads no other process's
// environment here, so an agent is stopped by its own process alone.
func stopMarked(context.Context, string, map[int]int64) (bool, error) { return false, nil }

func scan(string, map[int]int64) []int { return nil }

func group(int) map[int]int64 { return nil }
