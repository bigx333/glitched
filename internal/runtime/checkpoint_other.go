//go:build !unix

package runtime

import "os/exec"

func ownGroup(*exec.Cmd) {}

func reap(*exec.Cmd) {}
