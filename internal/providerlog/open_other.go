//go:build !unix

package providerlog

import "io/fs"

// Supported is that the log can keep to its folder here: not where the
// system has no flags to refuse a link or a FIFO at a file's path.
const Supported = false

const openFlags = 0

func links(fs.FileInfo) uint64 { return 0 }
