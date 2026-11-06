package browsermcp

import (
	"errors"
	"os"
	"syscall"
)

// denied is whether err is the socket refusing this user: Windows says
// so with WSAEACCES, which os.ErrPermission doesn't match.
func denied(err error) bool {
	return errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.WSAEACCES)
}
