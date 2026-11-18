//go:build unix

package daemon

import (
	"errors"
	"os"
	"syscall"
)

// lock takes the engine's OS lock, so two engines never open one database.
// The lock lasts until the returned unlock: the caller must hold it, or the
// file's finalizer closes the fd and releases the lock.
//
// unlock releases the lock before closing the file. A child this process
// forks shares the file until it execs, so a close alone leaves the lock to
// that child, and an engine started at once finds it taken.
func lock(path string) (unlock func() error, err error) {
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
	return func() error {
		return errors.Join(syscall.Flock(int(f.Fd()), syscall.LOCK_UN), f.Close())
	}, nil
}
