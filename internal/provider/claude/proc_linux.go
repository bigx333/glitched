package claude

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
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
// killGrace passes or ctx ends. It reports whether any process was found,
// and fails while one is left.
func stopRun(ctx context.Context, run string) (bool, error) {
	if len(scan(run)) == 0 {
		return false, nil
	}
	err := signal(marked(run), syscall.SIGTERM)
	if settled(ctx, run, 0) == nil {
		return true, nil
	}
	if left := settled(context.Background(), run, syscall.SIGKILL); left != nil {
		return true, errors.Join(left, err)
	}
	return true, nil
}

// settled waits up to killGrace, or until ctx ends, for run's processes to
// exit. Each time it finds some left, it sends them sig, unless sig is 0.
// It fails with the processes left, and why a signal to them failed.
func settled(ctx context.Context, run string, sig syscall.Signal) error {
	deadline := time.Now().Add(killGrace)
	var failed error
	for {
		left := scan(run)
		if len(left) == 0 {
			return nil
		}
		if ctx.Err() != nil || time.Now().After(deadline) {
			return errors.Join(fmt.Errorf("claude: processes %v of the run still run", left), failed)
		}
		if sig != 0 {
			if err := signal(marked(run), sig); err != nil {
				failed = err
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// proc is a process that carried a run's marker. Its pidfd names that
// process even once it has exited, so a signal sent through it never
// reaches another process that took its PID since.
type proc struct {
	pid int
	fd  int // -1 when the kernel gave no pidfd
}

// marked is run's processes, each held by a pidfd opened before its marker
// is read again, so the marker read is that process's own. Where the
// kernel has no pidfds, as Linux before 5.3, a process is held by its PID
// alone, and a process that takes the PID before the signal gets it. A
// process the kernel gives no pidfd for any other reason is left to a
// later scan.
func marked(run string) []proc {
	want := marker(run)
	var ps []proc
	for _, pid := range scan(run) {
		fd, err := unix.PidfdOpen(pid, 0)
		switch {
		case errors.Is(err, unix.ENOSYS):
			fd = -1
		case err != nil:
			continue
		}
		if !carries(pid, want) {
			if fd >= 0 {
				unix.Close(fd)
			}
			continue
		}
		ps = append(ps, proc{pid: pid, fd: fd})
	}
	return ps
}

// signal sends sig to ps, lets go of their pidfds, and fails with what
// kept a process that is still there from getting sig.
func signal(ps []proc, sig syscall.Signal) error {
	var errs []error
	for _, p := range ps {
		var err error
		if p.fd < 0 {
			err = syscall.Kill(p.pid, sig)
		} else {
			err = unix.PidfdSendSignal(p.fd, sig, nil, 0)
			unix.Close(p.fd)
		}
		if err != nil && !errors.Is(err, unix.ESRCH) {
			errs = append(errs, fmt.Errorf("signal process %d: %w", p.pid, err))
		}
	}
	return errors.Join(errs...)
}

func marker(run string) []byte { return []byte(runEnv + "=" + run) }

// scan is the PIDs of the processes whose environment names run as theirs.
// A process that has exited has none, so its zombie does not count.
func scan(run string) []int {
	want := marker(run)
	dirs, _ := os.ReadDir("/proc")
	var pids []int
	for _, d := range dirs {
		pid, err := strconv.Atoi(d.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		if carries(pid, want) {
			pids = append(pids, pid)
		}
	}
	return pids
}

// carries is whether pid's environment holds the entry want.
func carries(pid int, want []byte) bool {
	env, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "environ"))
	if err != nil {
		return false
	}
	for kv := range bytes.SplitSeq(env, []byte{0}) {
		if bytes.Equal(kv, want) {
			return true
		}
	}
	return false
}
