//go:build linux || darwin

package acp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// sysProcAttr puts the agent in a process group of its own, out of reach
// of a terminal's Ctrl-C to the engine.
func sysProcAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }

// process is one the system runs, as a sweep reads it.
type process struct {
	pid, ppid, pgid int
	start           int64 // when it started, which tells it from one that takes its PID later
	zombie          bool
}

// stopMarked stops every process whose environment carries a mark that
// starts with prefix, and what they started: the agent, its MCP servers
// and its tools, in sessions of their own included. They get SIGTERM,
// then SIGKILL once killGrace passes. seen holds what an earlier scan
// found, as one taken while the agent still lived to be their ancestor.
// It reports whether any process was found, and fails while one is left
// once ctx ends.
func stopMarked(ctx context.Context, prefix string, seen map[int]int64) (bool, error) {
	if seen == nil {
		seen = map[int]int64{}
	}
	pids := scan(prefix, seen)
	if len(pids) == 0 {
		return false, nil
	}
	err := signal(pids, syscall.SIGTERM)
	deadline := time.Now().Add(killGrace)
	for {
		left := scan(prefix, seen)
		if len(left) == 0 {
			return true, nil
		}
		if ctx.Err() != nil {
			return true, errors.Join(fmt.Errorf("acp: processes %v of %s still run", left, prefix), err)
		}
		if time.Now().After(deadline) {
			err = signal(left, syscall.SIGKILL)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// scan is the PIDs of the processes that carry a mark under prefix, what
// they started, and what an earlier scan found that still runs. macOS
// shows no platform binary's environment, as /bin/sh's, so such a process
// is found by its marked ancestor, or by an earlier scan that found it so.
func scan(prefix string, seen map[int]int64) []int {
	all := processes()
	children := map[int][]int{}
	byPID := map[int]process{}
	var tree []int
	for _, p := range all {
		if p.zombie || p.pid == os.Getpid() {
			continue
		}
		byPID[p.pid] = p
		children[p.ppid] = append(children[p.ppid], p.pid)
		if start, ok := seen[p.pid]; ok && start == p.start || carries(p.pid, prefix) {
			tree = append(tree, p.pid)
		}
	}
	for i := 0; i < len(tree); i++ {
		for _, c := range children[tree[i]] {
			if !slices.Contains(tree, c) {
				tree = append(tree, c)
			}
		}
	}
	for _, pid := range tree {
		seen[pid] = byPID[pid].start
	}
	return tree
}

func signal(pids []int, sig syscall.Signal) error {
	var errs []error
	for _, pid := range slices.Backward(pids) {
		if err := syscall.Kill(pid, sig); err != nil && !errors.Is(err, unix.ESRCH) {
			errs = append(errs, fmt.Errorf("signal process %d: %w", pid, err))
		}
	}
	return errors.Join(errs...)
}

// group is what still runs in the process group the agent led, as it
// is once the agent is reaped: while one of them lives, the system gives
// no new process that ID, so each is the agent's.
func group(leader int) map[int]int64 {
	left := map[int]int64{}
	for _, p := range processes() {
		if p.pgid == leader && p.pid != leader && !p.zombie {
			left[p.pid] = p.start
		}
	}
	return left
}

// marks is whether kv, an entry of a process's environment, carries a
// mark under prefix.
func marks(kv, prefix string) bool {
	v, ok := strings.CutPrefix(kv, procEnv+"=")
	return ok && strings.HasPrefix(v, prefix)
}
