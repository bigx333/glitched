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
// before it starts anything. Once cmd's context ends, m's jobs end with
// claude: with no SIGTERM, claude cannot stop its tools itself.
func contain(cmd *exec.Cmd, m mark) {
	if cmd.Err != nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
	cmd.Cancel = func() error {
		end(m)
		return terminate(cmd.Process)
	}
}

// end kills every process in m's jobs, and waits for none.
func end(m mark) {
	jobs.Lock()
	defer jobs.Unlock()
	for key, held := range jobs.of {
		if m.covers(key) {
			for _, job := range held {
				windows.TerminateJobObject(job, 1)
			}
		}
	}
}

// jobs is each launch's job object, by its mark. A job holds claude and
// every process it starts, and Windows kills them all when its last handle
// closes: when the engine exits, too. So the jobs end with the engine that
// made them.
var jobs = struct {
	sync.Mutex
	of       map[mark][]windows.Handle
	stopping map[mark]*halt // stops under way, by the mark they stop
}{of: map[mark][]windows.Handle{}, stopping: map[mark]*halt{}}

// halt is one stop of a mark's jobs, which a stop of any of them at the
// same time waits for.
type halt struct {
	done  chan struct{}
	found bool
	err   error
}

// confine puts claude, which contain had start suspended, in m's job,
// then lets it run. Where Windows gives no job, claude runs
// without one, and Stop and Delete reach claude alone. An engine that dies
// between the start and the resume leaves claude suspended, in no job:
// Go cannot start a process in a job, as PROC_THREAD_ATTRIBUTE_JOB_LIST
// would.
func confine(cmd *exec.Cmd, m mark) error {
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.CreationFlags&windows.CREATE_SUSPENDED == 0 {
		return nil
	}
	pid := uint32(cmd.Process.Pid)
	job, err := jobFor(pid)
	if err != nil {
		slog.Warn("claude: no job object for claude, so Stop and Delete leave the processes it started running", "err", err)
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
		jobs.of[m] = append(jobs.of[m], job)
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

// stopRun kills every process in m's jobs, claude and all it started,
// and waits up to killGrace, or until ctx ends, for them to exit. Windows
// has no SIGTERM to let claude stop its tools itself. It reports whether
// any process was found, and fails while one is left.
func stopRun(ctx context.Context, m mark) (bool, error) {
	// The jobs leave the map while they are stopped, so a stop of any of
	// them at the same time finds none to close again: it waits for this
	// one, then takes what is left.
	waited := false
	jobs.Lock()
	for {
		var s *halt
		for key, h := range jobs.stopping {
			if m.covers(key) || key.covers(m) {
				s = h
				break
			}
		}
		if s == nil {
			break
		}
		jobs.Unlock()
		select {
		case <-s.done:
		case <-ctx.Done():
			return true, ctx.Err()
		}
		if s.err != nil {
			return true, s.err
		}
		waited = waited || s.found
		jobs.Lock()
	}
	held := map[mark][]windows.Handle{}
	for key, js := range jobs.of {
		if m.covers(key) {
			held[key] = js
			delete(jobs.of, key)
		}
	}
	if len(held) == 0 {
		jobs.Unlock()
		return waited, nil
	}
	s := &halt{done: make(chan struct{})}
	jobs.stopping[m] = s
	jobs.Unlock()
	s.found, s.err = stopJobs(ctx, held)
	jobs.Lock()
	delete(jobs.stopping, m)
	jobs.Unlock()
	close(s.done)
	return waited || s.found, s.err
}

// stopJobs kills every process in held, jobs by their marks, and closes
// them once they are empty. Jobs left with processes go back for a later
// stop.
func stopJobs(ctx context.Context, held map[mark][]windows.Handle) (bool, error) {
	var all []windows.Handle
	for _, js := range held {
		all = append(all, js...)
	}
	found := running(all) > 0
	members := processes(all)
	defer func() {
		for _, p := range members {
			windows.CloseHandle(p)
		}
	}()
	var errs []error
	for _, job := range all {
		if err := windows.TerminateJobObject(job, 1); err != nil {
			errs = append(errs, os.NewSyscallError("TerminateJobObject", err))
		}
	}
	for deadline := time.Now().Add(killGrace); ; time.Sleep(time.Millisecond) {
		left := running(all) + exiting(members)
		if left == 0 {
			break
		}
		if ctx.Err() != nil || time.Now().After(deadline) {
			// A later stop tries again.
			jobs.Lock()
			for key, js := range held {
				jobs.of[key] = append(jobs.of[key], js...)
			}
			jobs.Unlock()
			return found, errors.Join(append(errs, fmt.Errorf("claude: %d of claude's processes still run", left))...)
		}
	}
	for _, job := range all {
		windows.CloseHandle(job)
	}
	return found, errors.Join(errs...)
}

// stopTools has nothing to do: a job holds claude and all it starts, and
// Windows has no process groups to tell the tool shells of a turn from the
// MCP servers claude keeps, so a parked claude's tools end with its job.
func stopTools(context.Context, mark, int) error { return nil }

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

// processes is a handle to each process in held's jobs. A job
// counts a process as gone a while before it is: while it still holds its
// files and working directory, and has not signalled its handle.
func processes(held []windows.Handle) []windows.Handle {
	var handles []windows.Handle
	for _, job := range held {
		var list struct {
			Assigned, Listed uint32
			IDs              [1024]uintptr
		}
		// With more than the list holds, Windows fails but fills it.
		windows.QueryInformationJobObject(job, windows.JobObjectBasicProcessIdList,
			uintptr(unsafe.Pointer(&list)), uint32(unsafe.Sizeof(list)), nil)
		for _, id := range list.IDs[:min(list.Listed, uint32(len(list.IDs)))] {
			if p, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(id)); err == nil {
				handles = append(handles, p)
			}
		}
	}
	return handles
}

// exiting is how many of handles are of processes not yet gone.
func exiting(handles []windows.Handle) int {
	n := 0
	for _, p := range handles {
		if ev, _ := windows.WaitForSingleObject(p, 0); ev != windows.WAIT_OBJECT_0 {
			n++
		}
	}
	return n
}

// terminate kills claude: Windows has no SIGTERM for it, and a signal it
// cannot send would leave claude running for killGrace. stopRun then ends
// what claude started.
func terminate(p *os.Process) error { return p.Kill() }
