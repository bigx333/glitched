package runtime

import "golang.org/x/sys/unix"

// cloneTree clones the folder from, and all it holds, to to in one call,
// each file with its mode and times.
func cloneTree(from, to string) error { return unix.Clonefile(from, to, unix.CLONE_NOFOLLOW) }
