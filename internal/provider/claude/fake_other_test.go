//go:build !unix

package claude_test

import "os/exec"

func newSession(*exec.Cmd) {}
