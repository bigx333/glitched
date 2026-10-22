//go:build unix

package daemon

import (
	"errors"
	"os"
	"syscall"
)

// lock takes the engine's OS lock, so two engines never open one database.
// The lock lasts until the returned file is closed: the caller must hold it,
// or its finalizer closes the fd and releases the lock.
func lock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, err
	}
	return f, nil
}
