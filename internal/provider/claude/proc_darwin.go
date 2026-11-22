package claude

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"syscall"

	"golang.org/x/sys/unix"
)

// sysProcAttr puts claude in a process group of its own, out of reach of a
// terminal's Ctrl-C to the engine. macOS has no parent-death signal: a
// claude whose engine died sees its stdin close, and the next engine's
// Reconcile stops what is left.
func sysProcAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }

// contain has nothing to do: macOS has no cgroups, so claude's processes
// are found by their marker and their ancestry alone, which one that drops
// the marker and leaves the tree, as a daemon does, escapes.
func contain(*exec.Cmd, mark) {}

func confine(*exec.Cmd, mark) error { return nil }

// szomb is SZOMB, a zombie's p_stat in <sys/proc.h>.
const szomb = 5

// kill has nothing to do: macOS has no cgroup to kill.
func (sweep) kill() error { return nil }

// signal sends sig to s's processes, what a process started before the
// process, so each is still found under its marked ancestor when its turn
// comes. It fails with what kept a process that is still there from
// getting sig. macOS has no pidfds, so each PID is found s's again right
// before its signal; a process that takes the PID between that check and
// the signal gets it, the race left.
func (s sweep) signal(sig syscall.Signal) error {
	var errs []error
	pids := s.scan()
	slices.Reverse(pids)
	for _, pid := range pids {
		if !s.has(pid) {
			continue
		}
		if err := syscall.Kill(pid, sig); err != nil && !errors.Is(err, unix.ESRCH) {
			errs = append(errs, fmt.Errorf("signal process %d: %w", pid, err))
		}
	}
	return errors.Join(errs...)
}

// scan is the PIDs of s's processes, ancestors before what they started:
// the user's processes whose environment names m as theirs, what they
// started, and what an earlier scan of s found that still runs, less
// zombies and those in the spared group. macOS shows another process no
// platform binary's environment, as /bin/zsh's, the shell Claude Code runs
// a tool call in, so such a process is found under a marked ancestor: one
// whose marked ancestors were all gone before s first scanned is found no
// more.
func (s sweep) scan() []int {
	procs, _ := unix.SysctlKinfoProcSlice("kern.proc.uid", os.Getuid())
	byPID := map[int]*unix.KinfoProc{}
	children := map[int][]int{}
	var tree []int
	for i := range procs {
		p := &procs[i]
		pid := int(p.Proc.P_pid)
		if pid == os.Getpid() || p.Proc.P_stat == szomb {
			continue
		}
		byPID[pid] = p
		children[int(p.Eproc.Ppid)] = append(children[int(p.Eproc.Ppid)], pid)
		if s.seen[pid] == started(p) || carries(pid, s.m) {
			tree = append(tree, pid)
		}
	}
	for i := 0; i < len(tree); i++ {
		for _, c := range children[tree[i]] {
			if !slices.Contains(tree, c) {
				tree = append(tree, c)
			}
		}
	}
	tree = slices.DeleteFunc(tree, func(pid int) bool { return !s.wants(byPID[pid]) })
	if s.seen != nil {
		for _, pid := range tree {
			s.seen[pid] = started(byPID[pid])
		}
	}
	return tree
}

// has is whether pid, read anew, is one of s's: a process an earlier scan
// found, by its start time, or one that carries m, or one of whose
// ancestors does.
func (s sweep) has(pid int) bool {
	if pid == os.Getpid() {
		return false
	}
	p, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || int(p.Proc.P_pid) != pid || !s.wants(p) {
		return false
	}
	if s.seen[pid] == started(p) {
		return true
	}
	for {
		if carries(pid, s.m) {
			return true
		}
		if pid = int(p.Eproc.Ppid); pid <= 1 {
			return false
		}
		if p, err = unix.SysctlKinfoProc("kern.proc.pid", pid); err != nil {
			return false
		}
	}
}

// started is when p started, in microseconds, which tells it from a
// process that takes its PID later.
func started(p *unix.KinfoProc) int64 {
	return p.Proc.P_starttime.Sec*1e6 + int64(p.Proc.P_starttime.Usec)
}

// wants is whether s would end p, were it marked: it is not a zombie and
// not in the spared group.
func (s sweep) wants(p *unix.KinfoProc) bool {
	return p.Proc.P_stat != szomb && (s.spare == 0 || int(p.Eproc.Pgid) != s.spare)
}

// carries is whether pid's environment makes it one of m's. The kernel
// lays it out as argc, the executable's path, NUL padding, argc
// arguments, then the environment up to an empty string.
func carries(pid int, m mark) bool {
	args, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil || len(args) < 4 {
		return false
	}
	argc := int(binary.NativeEndian.Uint32(args))
	rest := args[4:]
	i := bytes.IndexByte(rest, 0)
	if i < 0 {
		return false
	}
	rest = bytes.TrimLeft(rest[i:], "\x00")
	for range argc {
		i := bytes.IndexByte(rest, 0)
		if i < 0 {
			return false
		}
		rest = rest[i+1:]
	}
	for kv := range bytes.SplitSeq(rest, []byte{0}) {
		if len(kv) == 0 {
			return false
		}
		if m.tags(kv) {
			return true
		}
	}
	return false
}
