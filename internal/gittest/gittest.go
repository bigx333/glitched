// Package gittest runs git for tests apart from the host's git config, so
// settings such as tag.gpgsign, commit.gpgsign or hooks there never change
// what a test's git does.
package gittest

import (
	"os"
	"os/exec"
)

// Env is the process's environment without the host's global and system
// git config, and with an identity to commit and tag as.
func Env() []string {
	return append(os.Environ(),
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
}

// Command is git with args, run in Env.
func Command(args ...string) *exec.Cmd {
	cmd := exec.Command("git", args...)
	cmd.Env = Env()
	return cmd
}
