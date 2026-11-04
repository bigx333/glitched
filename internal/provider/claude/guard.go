package claude

import (
	"fmt"
	"os"
	"testing"
)

// guard stops a test from running the user's real, signed-in claude. Under
// go test, without the claudelive tag, the only claude allowed is the test
// binary itself playing one.
func guard(bin string) {
	if live || !testing.Testing() {
		return
	}
	if self, err := os.Executable(); err == nil && sameFile(bin, self) {
		return
	}
	panic(fmt.Sprintf("claude: a test without the claudelive tag tried to run %s; replay a transcript instead", bin))
}

func sameFile(a, b string) bool {
	fa, errA := os.Stat(a)
	fb, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(fa, fb)
}
