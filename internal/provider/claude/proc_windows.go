package claude

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func sysProcAttr() *syscall.SysProcAttr { return nil }

// contain has cmd start claude suspended, so confine can put it in a job
// before it starts anything. Once cmd's context ends, the run's jobs end
// with claude: with no SIGTERM, claude cannot stop its tools itself.
func contain(cmd *exec.Cmd, run string) {
	if cmd.Err != nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
	cmd.Cancel = func() error {
		end(run)
		return terminate(cmd.Process)
	}
}

// end kills every process in run's jobs, and waits for none.
func end(run string) {
	jobs.Lock()
	defer jobs.Unlock()
	for _, job := range jobs.of[run] {
		windows.TerminateJobObject(job, 1)
	}
}

// jobs is each run's job objects, one for each claude it started. A job
// holds claude and every process it starts, and Windows kills them all
// when its last handle closes: when the engine exits, too. So a run's
// jobs end with the engine that made them.
var jobs = struct {
	sync.Mutex
	of       map[string][]windows.Handle
	stopping map[string]*stop // a stop of the run under way
}{of: map[string][]windows.Handle{}, stopping: map[string]*stop{}}

// stop is one stop of a run, which a stop of it at the same time waits for
// and reports.
type stop struct {
	done  chan struct{}
	found bool
	err   error
}

// confine puts claude, which contain had start suspended, in a job of
// run's, then lets it run. Where Windows gives no job, claude runs
// without one, and Stop and Delete reach claude alone. An engine that dies
// between the start and the resume leaves claude suspended, in no job:
// Go cannot start a process in a job, as PROC_THREAD_ATTRIBUTE_JOB_LIST
// would.
func confine(cmd *exec.Cmd, run string) error {
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.CreationFlags&windows.CREATE_SUSPENDED == 0 {
		return nil
	}
	pid := uint32(cmd.Process.Pid)
	job, err := jobFor(pid)
	if err != nil {
		slog.Warn("claude: no job object for a run, so Stop and Delete leave the processes claude started running", "err", err)
	}
	if err := resume(pid); err != nil {
		if job != 0 {
			windows.TerminateJobObject(job, 1)
			windows.CloseHandle(job)
		}
		return fmt.Errorf("resume claude: %w", err)
	}
	if job != 0 {
		jobs.Lock()
		jobs.of[run] = append(jobs.of[run], job)
		jobs.Unlock()
	}
	return nil
}

// jobFor is a new job that kills its processes once closed, with pid in
// it.
func jobFor(pid uint32) (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, os.NewSyscallError("CreateJobObject", err)
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	// No breakaway: a process claude starts cannot leave the job, as one
	// cannot leave its scope on Linux, and one that asks to fails.
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		windows.CloseHandle(job)
		return 0, os.NewSyscallError("SetInformationJobObject", err)
	}
	p, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		windows.CloseHandle(job)
		return 0, os.NewSyscallError("OpenProcess", err)
	}
	defer windows.CloseHandle(p)
	if err := windows.AssignProcessToJobObject(job, p); err != nil {
		windows.CloseHandle(job)
		return 0, os.NewSyscallError("AssignProcessToJobObject", err)
	}
	return job, nil
}

// resume starts pid's threads, which CREATE_SUSPENDED left stopped.
func resume(pid uint32) error {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return os.NewSyscallError("CreateToolhelp32Snapshot", err)
	}
	defer windows.CloseHandle(snap)
	te := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	resumed := 0
	var errs []error
	for err = windows.Thread32First(snap, &te); err == nil; err = windows.Thread32Next(snap, &te) {
		if te.OwnerProcessID != pid {
			continue
		}
		t, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, te.ThreadID)
		if err != nil {
			errs = append(errs, os.NewSyscallError("OpenThread", err))
			continue
		}
		if _, err := windows.ResumeThread(t); err != nil {
			errs = append(errs, os.NewSyscallError("ResumeThread", err))
		} else {
			resumed++
		}
		windows.CloseHandle(t)
	}
	if resumed == 0 {
		return errors.Join(append(errs, fmt.Errorf("no thread of process %d to resume", pid))...)
	}
	return nil
}

// stopRun kills every process in run's jobs, claude and all it started,
// and waits up to killGrace, or until ctx ends, for them to exit. Windows
// has no SIGTERM to let claude stop its tools itself. It reports whether
// any process was found, and fails while one is left.
func stopRun(ctx context.Context, run string) (bool, error) {
	// The jobs leave the map while they are stopped, so a stop of the run
	// at the same time finds none to close again, and waits for this one.
	jobs.Lock()
	if s := jobs.stopping[run]; s != nil {
		jobs.Unlock()
		select {
		case <-s.done:
			return s.found, s.err
		case <-ctx.Done():
			return true, ctx.Err()
		}
	}
	held := jobs.of[run]
	delete(jobs.of, run)
	if len(held) == 0 {
		jobs.Unlock()
		return false, nil
	}
	s := &stop{done: make(chan struct{})}
	jobs.stopping[run] = s
	jobs.Unlock()
	s.found, s.err = stopJobs(ctx, run, held)
	jobs.Lock()
	delete(jobs.stopping, run)
	jobs.Unlock()
	close(s.done)
	return s.found, s.err
}

// stopJobs kills every process in held, run's jobs, and closes them once
// they are empty. Jobs left with processes go back for a later stop.
func stopJobs(ctx context.Context, run string, held []windows.Handle) (bool, error) {
	found := running(held) > 0
	var errs []error
	for _, job := range held {
		if err := windows.TerminateJobObject(job, 1); err != nil {
			errs = append(errs, os.NewSyscallError("TerminateJobObject", err))
		}
	}
	for deadline := time.Now().Add(killGrace); ; time.Sleep(20 * time.Millisecond) {
		left := running(held)
		if left == 0 {
			break
		}
		if ctx.Err() != nil || time.Now().After(deadline) {
			// A later stop tries again.
			jobs.Lock()
			jobs.of[run] = append(jobs.of[run], held...)
			jobs.Unlock()
			return found, errors.Join(append(errs, fmt.Errorf("claude: %d processes of the run still run", left))...)
		}
	}
	for _, job := range held {
		windows.CloseHandle(job)
	}
	return found, errors.Join(errs...)
}

// accounting is JOBOBJECT_BASIC_ACCOUNTING_INFORMATION.
type accounting struct {
	TotalUserTime, TotalKernelTime, ThisPeriodTotalUserTime, ThisPeriodTotalKernelTime int64
	TotalPageFaultCount, TotalProcesses, ActiveProcesses, TotalTerminatedProcesses     uint32
}

// running is how many processes in jobs still run. A job Windows cannot
// read counts as one still running.
func running(held []windows.Handle) int {
	n := 0
	for _, job := range held {
		var info accounting
		if err := windows.QueryInformationJobObject(job, windows.JobObjectBasicAccountingInformation,
			uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil); err != nil {
			n++
			continue
		}
		n += int(info.ActiveProcesses)
	}
	return n
}

// terminate kills claude: Windows has no SIGTERM for it, and a signal it
// cannot send would leave claude running for killGrace. stopRun then ends
// what claude started.
func terminate(p *os.Process) error { return p.Kill() }
