//go:build !unix

package guard

import "os/exec"

// ownGroup leaves the default: without process groups, a timeout stops the
// program itself (exec.CommandContext's kill) but not what it started.
func ownGroup(*exec.Cmd) {}

func loginShellPath() string { return "" }
