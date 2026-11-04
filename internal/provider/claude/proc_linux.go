package claude

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

// sysProcAttr puts claude in a process group of its own, out of reach of a
// terminal's Ctrl-C to the engine, and has Linux send it SIGTERM when the
// engine dies, so claude stops its tool shells as it exits.
func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
}

// stopRun stops every process that carries run's marker: claude, and what
// it started, tool shells in sessions of their own included. They get
// SIGTERM, on which claude stops its shells itself, then SIGKILL once
// killGrace passes or ctx ends. It reports whether any process was found.
func stopRun(ctx context.Context, run string) bool {
	pids := marked(run)
	if len(pids) == 0 {
		return false
	}
	signal(pids, syscall.SIGTERM)
	if !settled(ctx, run, nil) {
		settled(context.Background(), run, func(pids []int) { signal(pids, syscall.SIGKILL) })
	}
	return true
}

// settled waits up to killGrace, or until ctx ends, for run's processes to
// exit, and reports whether they did. Each time it finds some left, it
// calls each with them.
func settled(ctx context.Context, run string, each func([]int)) bool {
	deadline := time.Now().Add(killGrace)
	for {
		pids := marked(run)
		if len(pids) == 0 {
			return true
		}
		if ctx.Err() != nil || time.Now().After(deadline) {
			return false
		}
		if each != nil {
			each(pids)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func signal(pids []int, sig syscall.Signal) {
	for _, pid := range pids {
		syscall.Kill(pid, sig)
	}
}

// marked is the processes whose environment names run as theirs. A process
// that has exited has none, so its zombie does not count.
func marked(run string) []int {
	want := []byte(runEnv + "=" + run)
	dirs, _ := os.ReadDir("/proc")
	var pids []int
	for _, d := range dirs {
		pid, err := strconv.Atoi(d.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		env, err := os.ReadFile(filepath.Join("/proc", d.Name(), "environ"))
		if err != nil {
			continue
		}
		for kv := range bytes.SplitSeq(env, []byte{0}) {
			if bytes.Equal(kv, want) {
				pids = append(pids, pid)
				break
			}
		}
	}
	return pids
}
