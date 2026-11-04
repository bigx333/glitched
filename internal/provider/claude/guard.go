package claude

import (
	"fmt"
	"os"
	"testing"
)

// testClaude names, for a test binary and every process it starts, the
// one claude they may run: the test binary playing one. A program a test
// starts is not under go test itself, and learns of the test from this.
const testClaude = "GLITCHED_TEST_CLAUDE"

func init() {
	if live || !testing.Testing() {
		return
	}
	if self, err := os.Executable(); err == nil {
		os.Setenv(testClaude, self)
	}
}

// guard stops a test from running the user's real, signed-in claude.
// Without the claudelive tag, a test and what it starts may run only the
// test binary playing claude.
func guard(bin string) {
	if live {
		return
	}
	allowed, underTest := os.LookupEnv(testClaude)
	if !underTest && !testing.Testing() {
		return
	}
	if sameFile(bin, allowed) {
		return
	}
	panic(fmt.Sprintf("claude: a test without the claudelive tag tried to run %s; replay a transcript instead", bin))
}

func sameFile(a, b string) bool {
	fa, errA := os.Stat(a)
	fb, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(fa, fb)
}
