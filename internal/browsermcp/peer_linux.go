//go:build linux

package browsermcp

import (
	"errors"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

// verifyPeer ensures a caller on the Unix domain socket belongs to the same OS user.
func verifyPeer(conn net.Conn) error {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return nil
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return err
	}
	var uid uint32
	var sockErr error
	if err := raw.Control(func(fd uintptr) {
		cred, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if err != nil {
			sockErr = err
			return
		}
		uid = cred.Uid
	}); err != nil {
		return err
	}
	if sockErr != nil {
		return sockErr
	}
	if int(uid) != os.Getuid() {
		return errors.New("browser: unauthorized peer")
	}
	return nil
}
