//go:build !windows

package browsermcp

import (
	"errors"
	"os"
)

// denied is whether err is the socket refusing this user.
func denied(err error) bool { return errors.Is(err, os.ErrPermission) }
