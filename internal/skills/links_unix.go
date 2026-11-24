//go:build unix

package skills

import (
	"errors"
	"os"
	"syscall"
)

// links is how many names the open file f, whose Stat is fi, has.
func links(f *os.File, fi os.FileInfo) (uint64, error) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, errors.New("skills: no link count for " + f.Name())
	}
	return uint64(st.Nlink), nil
}
