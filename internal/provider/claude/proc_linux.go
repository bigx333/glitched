package claude

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
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

const cgroupRoot = "/sys/fs/cgroup"

// slice is where each claude launch gets a systemd scope of its own: a
// cgroup that holds it and all it starts, even a process that drops its
// environment. It covers what claude starts, not work handed to other
// programs, as a tmux server that puts its panes in scopes of its own. The
// scope outlives the engine, and its name, from the launch's mark, finds
// it after a restart.
type slice struct {
	dir        string // the slice's cgroup; "" where claude gets no scope
	systemdRun string
}

// scopes is the user systemd manager's app.slice, where there is one on a
// cgroup v2 hierarchy. Elsewhere claude's processes are found by their
// marker alone, which one that drops it escapes.
var scopes = sync.OnceValue(func() slice {
	s, err := userSlice()
	if err != nil {
		slog.Warn("claude: no user systemd scope for claude, so a process claude starts that drops its environment outlives it", "err", err)
	}
	return s
})

func userSlice() (slice, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(cgroupRoot, &st); err != nil || st.Type != unix.CGROUP2_SUPER_MAGIC {
		return slice{}, fmt.Errorf("no cgroup v2 hierarchy at %s", cgroupRoot)
	}
	run, err := exec.LookPath("systemd-run")
	if err != nil {
		return slice{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", "--user", "show", "--property=ControlGroup", "--value").Output()
	if err != nil {
		return slice{}, fmt.Errorf("no user systemd manager: %w", err)
	}
	manager := filepath.Join(cgroupRoot, strings.TrimSpace(string(out)))
	// cgroup.kill, from Linux 5.14, kills a cgroup's processes as they fork.
	if _, err := os.Stat(filepath.Join(manager, "cgroup.kill")); err != nil {
		return slice{}, fmt.Errorf("no cgroup.kill under the user systemd manager: %w", err)
	}
	// systemd-run can still be refused a scope, as by polkit, and claude
	// then never starts.
	probe := exec.CommandContext(ctx, run, append(scopeFlags, "--", "true")...)
	if out, err := probe.CombinedOutput(); err != nil {
		return slice{}, fmt.Errorf("systemd-run cannot start a scope: %w: %s", err, bytes.TrimSpace(out))
	}
	return slice{dir: filepath.Join(manager, "app.slice"), systemdRun: run}, nil
}

var scopeFlags = []string{"--user", "--scope", "--quiet", "--collect", "--slice=app.slice"}

// contain has cmd start claude in m's scope, which m, a launch's mark,
// names alone. A command that cannot start, as one the guard refused,
// probes nothing.
func contain(cmd *exec.Cmd, m mark) {
	if cmd.Err != nil {
		return
	}
	s := scopes()
	if s.dir == "" {
		return
	}
	unit := scopePrefix(m) + ".scope"
	cmd.Args = slices.Concat([]string{s.systemdRun}, scopeFlags, []string{"--unit=" + unit, "--", cmd.Path}, cmd.Args[1:])
	cmd.Path = s.systemdRun
}

// confine has nothing left to do once claude starts: its scope holds it
// from the start.
func confine(*exec.Cmd, mark) error { return nil }

func scopePrefix(m mark) string {
	if run, ok := strings.CutPrefix(string(m), "run-"); ok {
		return "glitched-run-" + run + "-"
	}
	return "glitched-claude-" + string(m)
}

// scopesOf is the cgroups of m's scopes.
func scopesOf(m mark) []string {
	dir := scopes().dir
	if dir == "" {
		return nil
	}
	found, _ := filepath.Glob(filepath.Join(dir, scopePrefix(m)+"*.scope"))
	return found
}

// kill has the kernel kill every process in s's scopes. A sweep that
// spares claude's group leaves them be: claude is in its scope too.
func (s sweep) kill() error {
	if s.spare != 0 {
		return nil
	}
	var errs []error
	for _, dir := range scopesOf(s.m) {
		if err := os.WriteFile(filepath.Join(dir, "cgroup.kill"), []byte("1"), 0); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// held is a process that was a sweep's. Its pidfd names that
// process even once it has exited, so a signal sent through it never
// reaches another process that took its PID since.
type held struct {
	pid int
	fd  int // -1 when the kernel gave no pidfd
}

// marked is s's processes, each held by a pidfd opened before it is
// found s's again, so what was read is that process's own. Where the
// kernel has no pidfds, as Linux before 5.3, a process is held by its PID
// alone, and a process that takes the PID before the signal gets it. A
// process the kernel gives no pidfd for any other reason is left to a
// later scan.
func (s sweep) marked() []held {
	var ps []held
	for _, pid := range s.scan() {
		fd, err := unix.PidfdOpen(pid, 0)
		switch {
		case errors.Is(err, unix.ENOSYS):
			fd = -1
		case err != nil:
			continue
		}
		if !s.has(pid) {
			if fd >= 0 {
				unix.Close(fd)
			}
			continue
		}
		ps = append(ps, held{pid: pid, fd: fd})
	}
	return ps
}

func (s sweep) signal(sig syscall.Signal) error { return signal(s.marked(), sig) }

// signal sends sig to ps, lets go of their pidfds, and fails with what
// kept a process that is still there from getting sig.
func signal(ps []held, sig syscall.Signal) error {
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

// scan is the PIDs of s's processes: those in m's scopes, and those whose
// environment names m as theirs, less those in the spared group. A process
// that has exited is in no cgroup and has no environment, so its zombie
// does not count.
func (s sweep) scan() []int {
	var pids []int
	for _, dir := range scopesOf(s.m) {
		filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || !d.IsDir() {
				return nil
			}
			procs, _ := os.ReadFile(filepath.Join(path, "cgroup.procs"))
			for f := range strings.FieldsSeq(string(procs)) {
				if pid, err := strconv.Atoi(f); err == nil && !slices.Contains(pids, pid) {
					pids = append(pids, pid)
				}
			}
			return nil
		})
	}
	dirs, _ := os.ReadDir("/proc")
	for _, d := range dirs {
		pid, err := strconv.Atoi(d.Name())
		if err != nil || pid == os.Getpid() || slices.Contains(pids, pid) {
			continue
		}
		if carries(pid, s.m) {
			pids = append(pids, pid)
		}
	}
	return slices.DeleteFunc(pids, s.spares)
}

// has is whether pid is one of s's: in one of m's scopes or carrying its
// marker, and not in the spared group.
func (s sweep) has(pid int) bool {
	if s.spares(pid) {
		return false
	}
	if dir := scopes().dir; dir != "" {
		cg, _ := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cgroup"))
		rel := strings.TrimPrefix(dir, cgroupRoot) + "/" + scopePrefix(s.m)
		if strings.Contains(string(cg), "0::"+rel) {
			return true
		}
	}
	return carries(pid, s.m)
}

// spares is whether s leaves pid be: it is in the spared group, or has
// gone, so its group cannot be read.
func (s sweep) spares(pid int) bool {
	if s.spare == 0 {
		return false
	}
	stat, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return true
	}
	// The command, in parentheses, may hold anything, ")" and spaces too;
	// state, ppid and pgrp follow its last ")".
	i := bytes.LastIndexByte(stat, ')')
	if i < 0 {
		return true
	}
	fields := strings.Fields(string(stat[i+1:]))
	if len(fields) < 3 {
		return true
	}
	pgid, err := strconv.Atoi(fields[2])
	return err != nil || pgid == s.spare
}

// carries is whether pid's environment makes it one of m's.
func carries(pid int, m mark) bool {
	env, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "environ"))
	if err != nil {
		return false
	}
	for kv := range bytes.SplitSeq(env, []byte{0}) {
		if m.tags(kv) {
			return true
		}
	}
	return false
}
