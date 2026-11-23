//go:build !unix

package guard

import "os/exec"

// OwnGroup leaves the default: without process groups, a timeout stops the
// program itself (exec.CommandContext's kill) but not what it started.
func OwnGroup(*exec.Cmd) {}

func loginShellPath() string { return "" }
