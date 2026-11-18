//go:build unix

package transport

import (
	"net"
	"syscall"
)

// released shuts a TCP listener's socket down before closing its fd. A
// child this process forks shares the fd until it execs, so a close alone
// leaves the port listening in that child, and an engine restarted on the
// same address finds it in use.
type released struct{ *net.TCPListener }

func (l released) Close() error {
	if rc, err := l.SyscallConn(); err == nil {
		rc.Control(func(fd uintptr) { syscall.Shutdown(int(fd), syscall.SHUT_RDWR) })
	}
	return l.TCPListener.Close()
}

func release(ln net.Listener) net.Listener {
	if tl, ok := ln.(*net.TCPListener); ok {
		return released{tl}
	}
	return ln
}
