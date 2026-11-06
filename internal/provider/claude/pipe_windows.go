package claude

import (
	"crypto/rand"
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// pipe is a pipe claude reads from, when in, or writes to. An anonymous
// pipe's handles cannot be polled, so its deadlines fail. The adapter's
// end is a named pipe's server, opened for overlapped I/O, whose deadlines
// work; claude's end is synchronous, as a program expects of its standard
// handles.
func pipe(in bool) (child, ours *os.File, err error) {
	// A random name keeps another program from taking it first. One that
	// lists the machine's pipes could still connect before claude's end
	// does, which fails the start.
	name := `\\.\pipe\glitched-claude-` + rand.Text()
	path, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, nil, err
	}
	access, mode := uint32(windows.PIPE_ACCESS_INBOUND), uint32(windows.GENERIC_WRITE)
	if in {
		// Duplex, not outbound: an outbound server's handle has no
		// FILE_READ_ATTRIBUTES, so os.NewFile cannot query it, leaves it
		// out of the poller, and its deadlines fail. Claude's end still
		// cannot write, as its access is read-only.
		access, mode = windows.PIPE_ACCESS_DUPLEX, windows.GENERIC_READ|windows.FILE_WRITE_ATTRIBUTES
	}
	server, err := windows.CreateNamedPipe(path,
		access|windows.FILE_FLAG_OVERLAPPED|windows.FILE_FLAG_FIRST_PIPE_INSTANCE,
		windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT|windows.PIPE_REJECT_REMOTE_CLIENTS,
		1, 64<<10, 64<<10, 0, nil)
	if err != nil {
		return nil, nil, os.NewSyscallError("CreateNamedPipe", err)
	}
	client, err := windows.CreateFile(path, mode|windows.FILE_READ_ATTRIBUTES, 0, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		windows.CloseHandle(server)
		return nil, nil, os.NewSyscallError("CreateFile", err)
	}
	return os.NewFile(uintptr(client), name), os.NewFile(uintptr(server), name), nil
}

// mayBeCut is whether a write that failed with err, reporting no bytes
// written, may still have sent some. A named pipe's write that ran out of
// time reports none, however much claude had read.
func mayBeCut(err error) bool { return errors.Is(err, os.ErrDeadlineExceeded) }
