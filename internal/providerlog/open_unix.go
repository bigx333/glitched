//go:build unix

package providerlog

import (
	"io/fs"
	"syscall"
)

// Supported is that the log can keep to its folder here.
const Supported = true

// openFlags open a file without following a link at its path, and
// without waiting, as for a FIFO with no reader.
const openFlags = syscall.O_NOFOLLOW | syscall.O_NONBLOCK

// links is how many names the file fi is of has.
func links(fi fs.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Nlink)
	}
	return 0
}
