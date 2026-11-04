package claude

import (
	"testing"
	"time"

	"glitched/internal/api"
)

// SetInterruptGrace sets how long an interrupt has to end the turn, for
// the length of t.
func SetInterruptGrace(t *testing.T, d time.Duration) {
	old := interruptGrace
	interruptGrace = d
	t.Cleanup(func() { interruptGrace = old })
}

// SessionOf is the Claude session of thread.
func SessionOf(thread api.ID) string { return uuidOf(thread) }
