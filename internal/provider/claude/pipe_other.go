//go:build !windows

package claude

import "os"

// pipe is a pipe claude reads from, when in, or writes to.
func pipe(in bool) (child, ours *os.File, err error) {
	r, w, err := os.Pipe()
	if in {
		return r, w, err
	}
	return w, r, err
}
