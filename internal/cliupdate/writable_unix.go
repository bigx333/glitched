//go:build unix

package cliupdate

import (
	"os"

	"golang.org/x/sys/unix"
)

// writable is whether the engine's user may write in dir.
func writable(dir string) bool { return unix.Access(dir, unix.W_OK) == nil }

// executable is whether path is a program the engine's user may run.
func executable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && unix.Access(path, unix.X_OK) == nil
}
