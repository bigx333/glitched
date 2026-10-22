//go:build unix

package main

import (
	"errors"
	"os"
	"syscall"
)

// lock takes the engine's OS lock, held until the process exits, so two
// engines never open one database.
func lock(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return errLocked
		}
		return err
	}
	return nil
}
