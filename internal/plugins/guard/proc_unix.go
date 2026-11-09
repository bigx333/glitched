//go:build unix

package guard

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// ownGroup puts the program in a process group of its own, so stopping it
// at its timeout also stops whatever it started (a git's ssh, a gh's git)
// instead of leaving those running as the user.
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}

const pathMark = "\x00glitched-path\x00"

// loginShellPath asks the user's login shell for its PATH, once. A login
// profile may print, so the PATH is what follows a mark; a slow or broken
// profile costs at most two seconds and falls back to the usual folders.
func loginShellPath() string {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, shell, "-l", "-c", `printf '\000glitched-path\000%s' "$PATH"`)
	ownGroup(cmd)
	cmd.WaitDelay = 100 * time.Millisecond
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	if i := bytes.LastIndex(out, []byte(pathMark)); i >= 0 {
		return string(out[i+len(pathMark):])
	}
	return ""
}
