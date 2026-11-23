//go:build !unix

package daemon

import "os/exec"

// ownGroup leaves cmd as it is where there are no process groups: a
// cancel kills cmd alone.
func ownGroup(*exec.Cmd) (end func()) { return func() {} }
