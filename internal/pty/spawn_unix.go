//go:build unix

package pty

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	creackpty "github.com/creack/pty"
)

type proc interface {
	Read(p []byte) (int, error)
	Write(p []byte) (int, error)
	Resize(cols, rows int) error
	Wait() (int, error)
	Close() error
}

type unixProc struct {
	ptmx *os.File
	cmd  *exec.Cmd

	mu     sync.Mutex
	closed bool
	waited bool
	code   int
	waitCh chan struct{}
}

func startProc(dir string, command []string, cols, rows int) (proc, error) {
	bin, args, err := resolveCommand(command)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(bin, args[1:]...)
	cmd.Args = args
	cmd.Dir = spawnDir(dir)
	cmd.Env = shellEnv()
	ptmx, err := creackpty.StartWithSize(cmd, &creackpty.Winsize{
		Cols: uint16(max(cols, 1)),
		Rows: uint16(max(rows, 1)),
	})
	if err != nil {
		return nil, err
	}
	up := &unixProc{
		ptmx:   ptmx,
		cmd:    cmd,
		waitCh: make(chan struct{}),
	}
	go func() {
		err := cmd.Wait()
		code := 0
		if err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				code = ee.ExitCode()
			} else {
				code = -1
			}
		}
		up.mu.Lock()
		up.waited = true
		up.code = code
		up.mu.Unlock()
		close(up.waitCh)
	}()
	return up, nil
}

func (p *unixProc) Read(b []byte) (int, error) {
	return p.ptmx.Read(b)
}

func (p *unixProc) Write(b []byte) (int, error) {
	return p.ptmx.Write(b)
}

func (p *unixProc) Resize(cols, rows int) error {
	return creackpty.Setsize(p.ptmx, &creackpty.Winsize{
		Cols: uint16(max(cols, 1)),
		Rows: uint16(max(rows, 1)),
	})
}

func (p *unixProc) Wait() (int, error) {
	<-p.waitCh
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.code, nil
}

func (p *unixProc) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		<-p.waitCh
		return nil
	}
	p.closed = true
	pid := 0
	if p.cmd.Process != nil {
		pid = p.cmd.Process.Pid
	}
	p.mu.Unlock()

	err := p.ptmx.Close()
	if pid > 0 {
		_ = syscall.Kill(-pid, syscall.SIGTERM)
		select {
		case <-p.waitCh:
		case <-time.After(300 * time.Millisecond):
			_ = syscall.Kill(-pid, syscall.SIGKILL)
			<-p.waitCh
		}
	}
	return err
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

func spawnDir(dir string) string {
	dir = normalizeWorkspace(dir)
	if dir != "" {
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			return dir
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return home
	}
	return "/"
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
