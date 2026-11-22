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

// SetKeep sets how long a parked claude waits and how many wait, for the
// adapters first used in t.
func SetKeep(t *testing.T, idle time.Duration, max int) {
	oldIdle, oldMax := keepIdle, keepMax
	keepIdle, keepMax = idle, max
	t.Cleanup(func() { keepIdle, keepMax = oldIdle, oldMax })
}

// SessionOf is the Claude session of thread.
func SessionOf(thread api.ID) string { return uuidOf(thread) }
