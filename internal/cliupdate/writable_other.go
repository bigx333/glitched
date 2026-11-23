//go:build !unix

package cliupdate

import "os"

// writable is true where Access has no W_OK to ask: the updater says
// itself when it may not write.
func writable(string) bool { return true }

// executable is whether path is a file, which is all a system without
// Access can tell.
func executable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
