//go:build !linux && !darwin

package sweep

import (
	"context"
	"syscall"
)

func SysProcAttr() *syscall.SysProcAttr { return nil }

// Stop finds no process: the engine reads no other process's
// environment here, so an agent is stopped by its own process alone.
func (Mark) Stop(context.Context, string, map[int]int64) (bool, error) { return false, nil }

func (Mark) Scan(string, map[int]int64) []int { return nil }

func (Mark) Carries(int, string) bool { return false }

func Group(int) map[int]int64 { return nil }
