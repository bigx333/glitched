package claude

import (
	"testing"

	"golang.org/x/sys/windows"
)

// A program may set its standard input's pipe mode, as libuv does, which
// needs FILE_WRITE_ATTRIBUTES: os.Pipe's read end has it.
func TestClaudeCanSetItsInputsPipeMode(t *testing.T) {
	child, ours, err := pipe(true)
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()
	defer ours.Close()
	mode := uint32(windows.PIPE_READMODE_BYTE | windows.PIPE_NOWAIT)
	if err := windows.SetNamedPipeHandleState(windows.Handle(child.Fd()), &mode, nil, nil); err != nil {
		t.Errorf("SetNamedPipeHandleState on claude's stdin: %v", err)
	}
}
