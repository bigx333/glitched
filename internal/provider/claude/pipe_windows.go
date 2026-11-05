package claude

import (
	"os"
	"strconv"
	"sync/atomic"

	"golang.org/x/sys/windows"
)

var pipes atomic.Uint64

// pipe is a pipe claude reads from, when in, or writes to. An anonymous
// pipe's handles cannot be polled, so its deadlines fail. The adapter's
// end is a named pipe's server, opened for overlapped I/O, whose deadlines
// work; claude's end is synchronous, as a program expects of its standard
// handles.
func pipe(in bool) (child, ours *os.File, err error) {
	name := `\\.\pipe\glitched-claude-` + strconv.Itoa(os.Getpid()) + "-" + strconv.FormatUint(pipes.Add(1), 10)
	path, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, nil, err
	}
	access, mode := uint32(windows.PIPE_ACCESS_INBOUND), uint32(windows.GENERIC_WRITE)
	if in {
		access, mode = windows.PIPE_ACCESS_OUTBOUND, windows.GENERIC_READ
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
