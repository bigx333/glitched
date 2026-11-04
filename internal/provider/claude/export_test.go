package claude

import (
	"testing"
	"time"
)

// SetInterruptGrace sets how long an interrupt has to end the turn, for
// the length of t.
func SetInterruptGrace(t *testing.T, d time.Duration) {
	old := interruptGrace
	interruptGrace = d
	t.Cleanup(func() { interruptGrace = old })
}
