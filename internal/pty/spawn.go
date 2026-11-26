package pty

import (
	"os"
	"time"
)

// A proc is a shell running in a pseudo-terminal. Read returns io.EOF once
// the shell exited and its output is read; Close ends the shell and is
// safe to call again, and after it exited.
type proc interface {
	Read(p []byte) (int, error)
	Write(p []byte) (int, error)
	Resize(cols, rows int) error
	Wait() (int, error)
	Close() error
}

const (
	// drainTime is how long a shell's last output may take to be read once
	// it exited, before the terminal is hung up on whatever else has it,
	// such as a job left in the background.
	drainTime = 100 * time.Millisecond
	// killTime is how long Close lets a shell end before killing it.
	killTime = 300 * time.Millisecond
)

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
	return string(os.PathSeparator)
}
