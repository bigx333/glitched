package claude

import (
	"testing"
	"time"

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

// The adapter's end of claude's stdin must take deadlines, and claude's
// end must not take writes.
func TestClaudesInputHasDeadlinesAndClaudeCannotWriteToIt(t *testing.T) {
	child, ours, err := pipe(true)
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()
	defer ours.Close()
	if err := ours.SetWriteDeadline(time.Time{}); err != nil {
		t.Errorf("SetWriteDeadline on the adapter's end: %v", err)
	}
	if _, err := child.Write([]byte("x")); err == nil {
		t.Error("claude's end of its input took a write")
	}
}
