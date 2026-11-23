package codex

import (
	"fmt"
	"os"
	"testing"
)

// testCodex names, for a test binary and every process it starts, the one
// Codex they may run: the test binary playing app-server. A program a
// test starts is not under go test itself, and learns of the test from
// this.
const testCodex = "GLITCHED_TEST_CODEX"

func init() {
	if live || !testing.Testing() {
		return
	}
	if self, err := os.Executable(); err == nil {
		os.Setenv(testCodex, self)
	}
}

// guard stops a test from running the user's real, signed-in codex.
// Without the codexlive tag, a test and what it starts may run only the
// test binary playing app-server (package codextest). A test binary
// panics; any other program gets the error, so it fails the turn and not
// the whole daemon.
func guard(bin string) error {
	if live {
		return nil
	}
	allowed, underTest := os.LookupEnv(testCodex)
	if !underTest && !testing.Testing() || sameFile(bin, allowed) {
		return nil
	}
	err := fmt.Errorf("codex: a test without the codexlive tag tried to run %s; play app-server with codextest instead", bin)
	if testing.Testing() {
		panic(err.Error())
	}
	return err
}

func sameFile(a, b string) bool {
	fa, errA := os.Stat(a)
	fb, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(fa, fb)
}
