package claude_test

import (
	"os"
	"os/exec"

	"golang.org/x/sys/windows"
)

// command is a process the fake claude starts, as Claude Code runs a tool
// call. Windows has no sleep, so the test binary plays it.
func command(argv []string) *exec.Cmd {
	if argv[0] != "sleep" {
		return exec.Command(argv[0], argv[1:]...)
	}
	c := exec.Command(must(os.Executable()))
	c.Env = append(os.Environ(), envSleep+"="+argv[1]+"s")
	return c
}

func kill(pid int) {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return
	}
	defer windows.CloseHandle(h)
	windows.TerminateProcess(h, 1)
}

// gone is whether pid has exited.
func gone(pid int) bool {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return true
	}
	defer windows.CloseHandle(h)
	ev, _ := windows.WaitForSingleObject(h, 0)
	return ev == windows.WAIT_OBJECT_0
}
