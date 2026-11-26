//go:build windows

package pty

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// UpdateProcThreadAttribute takes the pseudo console's handle itself as the
// attribute's value, which windows.ProcThreadAttributeListContainer.Update
// would have to hold as a pointer.
var procUpdateProcThreadAttribute = windows.NewLazySystemDLL("kernel32.dll").NewProc("UpdateProcThreadAttribute")

type conProc struct {
	in      *os.File // what is typed, to the console
	out     *os.File // the console's output
	process windows.Handle

	code    int
	waitCh  chan struct{} // closed once the shell exited and code is set
	readEOF chan struct{} // closed once Read reached the end of the output

	eofOnce sync.Once

	mu      sync.Mutex
	console windows.Handle // zero once closed
	closed  bool
}

func startProc(dir string, command []string, cols, rows int) (proc, error) {
	bin, args, err := resolveCommand(command)
	if err != nil {
		return nil, err
	}
	// The console reads what is typed from inR and writes its output to
	// outW; this side writes to inW and reads outR.
	var inR, inW, outR, outW windows.Handle
	if err := windows.CreatePipe(&inR, &inW, nil, 0); err != nil {
		return nil, fmt.Errorf("pty: %w", err)
	}
	if err := windows.CreatePipe(&outR, &outW, nil, 0); err != nil {
		windows.CloseHandle(inR)
		windows.CloseHandle(inW)
		return nil, fmt.Errorf("pty: %w", err)
	}
	var console windows.Handle
	err = windows.CreatePseudoConsole(coord(cols, rows), inR, outW, 0, &console)
	// The console has its own copies of its ends.
	windows.CloseHandle(inR)
	windows.CloseHandle(outW)
	if err != nil {
		windows.CloseHandle(inW)
		windows.CloseHandle(outR)
		return nil, fmt.Errorf("pty: CreatePseudoConsole: %w", err)
	}
	p := &conProc{
		in:      os.NewFile(uintptr(inW), "conpty-in"),
		out:     os.NewFile(uintptr(outR), "conpty-out"),
		console: console,
		waitCh:  make(chan struct{}),
		readEOF: make(chan struct{}),
	}
	if p.process, err = spawn(console, spawnDir(dir), bin, args); err != nil {
		p.closeConsole()
		return nil, err
	}
	go p.wait()
	return p, nil
}

func spawn(console windows.Handle, dir, bin string, args []string) (windows.Handle, error) {
	attrs, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return 0, fmt.Errorf("pty: %w", err)
	}
	defer attrs.Delete()
	if r, _, err := procUpdateProcThreadAttribute.Call(uintptr(unsafe.Pointer(attrs.List())), 0, windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE, uintptr(console), unsafe.Sizeof(console), 0, 0); r == 0 {
		return 0, fmt.Errorf("pty: UpdateProcThreadAttribute: %w", err)
	}
	si := windows.StartupInfoEx{ProcThreadAttributeList: attrs.List()}
	si.Cb = uint32(unsafe.Sizeof(si))
	// The console's own standard handles: without invalid ones, a child of
	// a process whose output is redirected would write there instead.
	si.Flags = windows.STARTF_USESTDHANDLES
	si.StdInput, si.StdOutput, si.StdErr = windows.InvalidHandle, windows.InvalidHandle, windows.InvalidHandle

	cmdline, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(append([]string{bin}, args[1:]...)))
	if err != nil {
		return 0, err
	}
	wdir, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, err
	}
	env := environmentBlock(os.Environ())
	var pi windows.ProcessInformation
	flags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_UNICODE_ENVIRONMENT)
	if err := windows.CreateProcess(nil, cmdline, nil, nil, false, flags, &env[0], wdir, &si.StartupInfo, &pi); err != nil {
		return 0, fmt.Errorf("pty: starting %s: %w", bin, err)
	}
	windows.CloseHandle(pi.Thread)
	return pi.Process, nil
}

func (p *conProc) wait() {
	code := uint32(1)
	if _, err := windows.WaitForSingleObject(p.process, windows.INFINITE); err == nil {
		_ = windows.GetExitCodeProcess(p.process, &code)
	}
	p.mu.Lock()
	proc := p.process
	p.process = 0
	p.mu.Unlock()
	if proc != 0 {
		_ = windows.CloseHandle(proc)
	}
	p.code = int(code)
	close(p.waitCh)
	select {
	case <-p.readEOF:
	case <-time.After(drainTime):
	}
	p.closeConsole()
}

// closeConsole closes the console, which ends the processes attached to
// it; Read returns io.EOF once it read what the console wrote.
func (p *conProc) closeConsole() {
	p.mu.Lock()
	console := p.console
	p.console = 0
	p.mu.Unlock()
	if console == 0 {
		return
	}
	p.in.Close()
	// ClosePseudoConsole waits for the output to be read on Windows before
	// 11 24H2, so it can't run on Read's goroutine.
	go func() {
		windows.ClosePseudoConsole(console)
		p.out.Close()
	}()
}

func (p *conProc) Read(b []byte) (int, error) {
	n, err := p.out.Read(b)
	if err != nil && n == 0 {
		p.eofOnce.Do(func() { close(p.readEOF) })
		if errors.Is(err, os.ErrClosed) || errors.Is(err, windows.ERROR_BROKEN_PIPE) {
			err = io.EOF
		}
	}
	return n, err
}

func (p *conProc) Write(b []byte) (int, error) {
	return p.in.Write(b)
}

func (p *conProc) Resize(cols, rows int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.console == 0 {
		return os.ErrClosed
	}
	return windows.ResizePseudoConsole(p.console, coord(cols, rows))
}

func (p *conProc) Wait() (int, error) {
	<-p.waitCh
	return p.code, nil
}

// Close closes the console and kills the shell if it outlasts killTime.
func (p *conProc) Close() error {
	p.mu.Lock()
	again := p.closed
	p.closed = true
	p.mu.Unlock()
	p.closeConsole()
	if !again {
		select {
		case <-p.waitCh:
		case <-time.After(killTime):
			p.mu.Lock()
			proc := p.process
			if proc != 0 {
				_ = windows.TerminateProcess(proc, 1)
			}
			p.mu.Unlock()
		}
	}
	<-p.waitCh
	return nil
}

// coord packs cols×rows as the COORD ConPTY takes.
func coord(cols, rows int) windows.Coord {
	return windows.Coord{X: int16(max(1, min(cols, 0x7fff))), Y: int16(max(1, min(rows, 0x7fff)))}
}

func resolveCommand(cmd []string) (string, []string, error) {
	if len(cmd) > 0 {
		bin, err := exec.LookPath(cmd[0])
		if err != nil {
			return "", nil, err
		}
		return bin, append([]string(nil), cmd...), nil
	}
	for _, name := range []string{"pwsh.exe", "powershell.exe", os.Getenv("COMSPEC"), "cmd.exe"} {
		if name == "" {
			continue
		}
		if bin, err := exec.LookPath(name); err == nil {
			return bin, []string{name}, nil
		}
	}
	return "", nil, errors.New("pty: no shell found")
}

// environmentBlock encodes env as CreateProcess takes it: NUL-terminated
// UTF-16 strings, then another NUL.
func environmentBlock(env []string) []uint16 {
	var b []uint16
	for _, kv := range env {
		b = append(b, utf16.Encode([]rune(kv))...)
		b = append(b, 0)
	}
	if len(b) == 0 {
		b = append(b, 0)
	}
	return append(b, 0)
}
