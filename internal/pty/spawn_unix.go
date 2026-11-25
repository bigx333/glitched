//go:build unix

package pty

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	creackpty "github.com/creack/pty"
	"golang.org/x/sys/unix"
)

type unixProc struct {
	ptmx *os.File
	cmd  *exec.Cmd

	code    int
	waitCh  chan struct{} // closed once the shell exited and code is set
	readEOF chan struct{} // closed once Read reached the end of the output

	eofOnce   sync.Once
	closeOnce sync.Once // closes ptmx

	mu     sync.Mutex
	closed bool
}

func startProc(dir string, command []string, cols, rows int) (proc, error) {
	bin, args, err := resolveCommand(command)
	if err != nil {
		return nil, err
	}
	master, tty, err := creackpty.Open()
	if err != nil {
		return nil, err
	}
	defer tty.Close() // the shell has its own copies
	ptmx, err := pollable(master)
	if err != nil {
		return nil, err
	}
	if err := setSize(ptmx, cols, rows); err != nil {
		ptmx.Close()
		return nil, err
	}
	cmd := exec.Command(bin, args[1:]...)
	cmd.Args = args
	cmd.Dir = spawnDir(dir)
	cmd.Env = shellEnv()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err := cmd.Start(); err != nil {
		ptmx.Close()
		return nil, err
	}
	p := &unixProc{
		ptmx:    ptmx,
		cmd:     cmd,
		waitCh:  make(chan struct{}),
		readEOF: make(chan struct{}),
	}
	go p.wait()
	return p, nil
}

// pollable returns a non-blocking copy of f that the runtime's poller
// waits on, and closes f. creack/pty calls f.Fd, which leaves f blocking
// for good: a Read then holds a thread until output comes, and Close
// waits for that Read.
func pollable(f *os.File) (*os.File, error) {
	defer f.Close()
	syscall.ForkLock.RLock()
	fd, err := syscall.Dup(int(f.Fd()))
	if err == nil {
		syscall.CloseOnExec(fd)
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		return nil, err
	}
	if err := syscall.SetNonblock(fd, true); err != nil {
		syscall.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), f.Name()), nil
}

func (p *unixProc) wait() {
	err := p.cmd.Wait()
	if ee, ok := errors.AsType[*exec.ExitError](err); ok {
		p.code = ee.ExitCode()
	} else if err != nil {
		p.code = -1
	}
	close(p.waitCh)
	select {
	case <-p.readEOF:
	case <-time.After(drainTime):
	}
	p.closeMaster()
}

func (p *unixProc) closeMaster() {
	p.closeOnce.Do(func() { p.ptmx.Close() })
}

func (p *unixProc) Read(b []byte) (int, error) {
	for {
		n, err := p.ptmx.Read(b)
		if n > 0 || err == nil {
			return n, nil
		}
		// A master the poller could not take says it has no output yet.
		if errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EINTR) {
			if err = p.waitReadable(); err == nil {
				continue
			}
		}
		p.eofOnce.Do(func() { close(p.readEOF) })
		// EIO: no process has the terminal open any more.
		if errors.Is(err, syscall.EIO) || errors.Is(err, os.ErrClosed) || errors.Is(err, io.EOF) {
			return 0, io.EOF
		}
		return 0, err
	}
}

// waitReadable waits up to 100ms for output on a master the poller could
// not take.
func (p *unixProc) waitReadable() error {
	return control(p.ptmx, func(fd int) error {
		_, err := unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}, 100)
		if errors.Is(err, unix.EINTR) {
			return nil
		}
		return err
	})
}

func (p *unixProc) Write(b []byte) (int, error) {
	return p.ptmx.Write(b)
}

func (p *unixProc) Resize(cols, rows int) error {
	return setSize(p.ptmx, cols, rows)
}

func (p *unixProc) Wait() (int, error) {
	<-p.waitCh
	return p.code, nil
}

// Close hangs up the terminal and ends the shell's process group and the
// one in its foreground, killing them if they outlast killTime.
func (p *unixProc) Close() error {
	p.mu.Lock()
	again := p.closed
	p.closed = true
	p.mu.Unlock()
	select {
	case <-p.waitCh:
		p.closeMaster()
		return nil
	default:
	}
	if again {
		<-p.waitCh
		return nil
	}
	pid := p.cmd.Process.Pid
	fg := 0
	_ = control(p.ptmx, func(fd int) (err error) {
		fg, err = unix.IoctlGetInt(fd, unix.TIOCGPGRP)
		return err
	})
	p.closeMaster()
	if fg > 0 && fg != pid {
		_ = syscall.Kill(-fg, syscall.SIGTERM)
	}
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	select {
	case <-p.waitCh:
	case <-time.After(killTime):
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		<-p.waitCh
	}
	return nil
}

// control runs fn on f's descriptor without f.Fd, which would make f
// blocking.
func control(f *os.File, fn func(fd int) error) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var ferr error
	if err := rc.Control(func(fd uintptr) { ferr = fn(int(fd)) }); err != nil {
		return err
	}
	return ferr
}

func setSize(f *os.File, cols, rows int) error {
	return control(f, func(fd int) error {
		return unix.IoctlSetWinsize(fd, unix.TIOCSWINSZ, &unix.Winsize{
			Col: uint16(max(cols, 1)),
			Row: uint16(max(rows, 1)),
		})
	})
}

func resolveCommand(cmd []string) (string, []string, error) {
	if len(cmd) > 0 {
		bin, err := exec.LookPath(cmd[0])
		if err != nil {
			return "", nil, err
		}
		args := make([]string, len(cmd))
		copy(args, cmd)
		return bin, args, nil
	}
	sh := os.Getenv("SHELL")
	if sh == "" {
		sh = "/bin/sh"
		if runtime.GOOS == "darwin" {
			sh = "/bin/zsh"
		}
	}
	return sh, []string{"-" + filepath.Base(sh)}, nil
}

func shellEnv() []string {
	env := os.Environ()
	set := func(kv string) {
		k, _, _ := strings.Cut(kv, "=")
		for i, e := range env {
			if strings.HasPrefix(e, k+"=") {
				env[i] = kv
				return
			}
		}
		env = append(env, kv)
	}
	set("TERM=xterm-256color")
	set("COLORTERM=truecolor")
	if os.Getenv("LANG") == "" && os.Getenv("LC_ALL") == "" && os.Getenv("LC_CTYPE") == "" {
		set("LANG=en_US.UTF-8")
	}
	return env
}
