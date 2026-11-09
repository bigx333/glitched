// Package guard holds a plugin to one set of rules wherever it reaches
// past its sandbox: the programs it runs (Run) and the hosts it reaches
// (NetPolicy). It mirrors Zeron's plugin-guard crate, so a plugin written
// for one host meets the same limits in the other.
//
// A program a plugin names still runs as the user. Most programs can run
// others or write any file, so these rules are not a sandbox. They stop the
// one-liners a person granting "runs git" would not expect (an alias that
// runs a shell, a repository's fsmonitor, gh printing its token), and Risk
// says plainly what the grant gives away.
package guard

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ErrRefused is what every refusal matches with errors.Is, so a caller can
// tell "the guard said no" (show it, offer the grant) from a program or
// network failure.
var ErrRefused = errors.New("refused by the plugin guard")

type refusal string

func (r refusal) Error() string        { return string(r) }
func (r refusal) Is(target error) bool { return target == ErrRefused }

func refuse(format string, a ...any) error { return refusal(fmt.Sprintf(format, a...)) }

// The two answers Risk gives, worded for the consent sheet.
const (
	RiskHarmless = "harmless"
	RiskAnything = "can do anything you can"
)

// harmless are programs known to neither run others nor write files.
// Anything missing here, including every shell, interpreter, git and gh,
// is RiskAnything: an unknown program is assumed to be able to.
var harmless = []string{
	"basename", "cat", "cksum", "cut", "date", "df", "dirname", "du", "echo", "false", "file", "grep", "head",
	"hostname", "id", "jq", "ls", "md5", "md5sum", "nproc", "printenv", "printf", "ps", "pwd", "readlink",
	"realpath", "sha1sum", "sha256sum", "shasum", "sleep", "stat", "sw_vers", "tail", "tr", "true", "uname",
	"uptime", "wc", "which", "whoami",
}

// Risk classifies a program for the consent sheet. The sheet asks every
// time for RiskAnything and never grants it silently.
func Risk(program string) string {
	if slices.Contains(harmless, normalized(program)) {
		return RiskHarmless
	}
	return RiskAnything
}

// normalized is a program's name as the system finds it: macOS and Windows
// find "Git" as git, and Windows adds an extension.
func normalized(program string) string {
	lower := strings.ToLower(program)
	for _, ext := range []string{".exe", ".cmd", ".bat", ".com"} {
		if stem, ok := strings.CutSuffix(lower, ext); ok {
			return stem
		}
	}
	return lower
}
