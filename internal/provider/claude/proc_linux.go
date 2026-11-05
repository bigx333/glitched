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

	"glitched/internal/api"
)

// sysProcAttr puts claude in a process group of its own, out of reach of a
// terminal's Ctrl-C to the engine, and has Linux send it SIGTERM when the
// engine dies, so claude stops its tool shells as it exits.
func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
}

const cgroupRoot = "/sys/fs/cgroup"

// slice is where each claude a run starts gets a systemd scope of its own:
// a cgroup that holds it and all it starts, even a process that drops its
// environment. It covers what claude starts, not work handed to other
// programs, as a tmux server that puts its panes in scopes of its own. The
// scope outlives the engine, and its name, from the run, finds it after a
// restart.
type slice struct {
	dir        string // the slice's cgroup; "" where runs get no scope
	systemdRun string
}

// scopes is the user systemd manager's app.slice, where there is one on a
// cgroup v2 hierarchy. Elsewhere a run's processes are found by their
// marker alone, which one that drops it escapes.
var scopes = sync.OnceValue(func() slice {
	s, err := userSlice()
	if err != nil {
		slog.Warn("claude: no user systemd scope for a run, so a process a run starts that drops its environment outlives the run", "err", err)
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

// contain has cmd start claude in a new scope of run's. Each process gets
// a scope of its own: systemd may not have let go of the last one's name.
// A command that cannot start, as one the guard refused, probes nothing.
func contain(cmd *exec.Cmd, run string) {
	if cmd.Err != nil {
		return
	}
	s := scopes()
	if s.dir == "" {
		return
	}
	unit := scopePrefix(run) + api.NewID().String() + ".scope"
	cmd.Args = slices.Concat([]string{s.systemdRun}, scopeFlags, []string{"--unit=" + unit, "--", cmd.Path}, cmd.Args[1:])
	cmd.Path = s.systemdRun
}

func scopePrefix(run string) string { return "glitched-run-" + run + "-" }

// scopesOf is the cgroups of run's scopes.
func scopesOf(run string) []string {
	dir := scopes().dir
	if dir == "" {
		return nil
	}
	found, _ := filepath.Glob(filepath.Join(dir, scopePrefix(run)+"*.scope"))
	return found
}

// kill has the kernel kill every process in run's scopes.
func kill(run string) error {
	var errs []error
	for _, dir := range scopesOf(run) {
		if err := os.WriteFile(filepath.Join(dir, "cgroup.kill"), []byte("1"), 0); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// stopRun stops every process of run's: claude, and what it started, tool
// shells in sessions of their own included. They get SIGTERM, on which
// claude stops its shells itself, then SIGKILL once killGrace passes or ctx
// ends. It reports whether any process was found, and fails while one is
// left.
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
		if sig == syscall.SIGKILL {
			if err := kill(run); err != nil {
				failed = err
			}
		}
		if sig != 0 {
			if err := signal(marked(run), sig); err != nil {
				failed = err
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// proc is a process that was a run's. Its pidfd names that
// process even once it has exited, so a signal sent through it never
// reaches another process that took its PID since.
type proc struct {
	pid int
	fd  int // -1 when the kernel gave no pidfd
}

// marked is run's processes, each held by a pidfd opened before it is
// found the run's again, so what was read is that process's own. Where the
// kernel has no pidfds, as Linux before 5.3, a process is held by its PID
// alone, and a process that takes the PID before the signal gets it. A
// process the kernel gives no pidfd for any other reason is left to a
// later scan.
func marked(run string) []proc {
	var ps []proc
	for _, pid := range scan(run) {
		fd, err := unix.PidfdOpen(pid, 0)
		switch {
		case errors.Is(err, unix.ENOSYS):
			fd = -1
		case err != nil:
			continue
		}
		if !belongs(pid, run) {
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

// scan is the PIDs of run's processes: those in its scopes, and those whose
// environment names run as theirs. A process that has exited is in no
// cgroup and has no environment, so its zombie does not count.
func scan(run string) []int {
	var pids []int
	for _, dir := range scopesOf(run) {
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
	want := marker(run)
	dirs, _ := os.ReadDir("/proc")
	for _, d := range dirs {
		pid, err := strconv.Atoi(d.Name())
		if err != nil || pid == os.Getpid() || slices.Contains(pids, pid) {
			continue
		}
		if carries(pid, want) {
			pids = append(pids, pid)
		}
	}
	return pids
}

// belongs is whether pid is in one of run's scopes or carries its marker.
func belongs(pid int, run string) bool {
	if dir := scopes().dir; dir != "" {
		cg, _ := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cgroup"))
		rel := strings.TrimPrefix(dir, cgroupRoot) + "/" + scopePrefix(run)
		if strings.Contains(string(cg), "0::"+rel) {
			return true
		}
	}
	return carries(pid, marker(run))
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
