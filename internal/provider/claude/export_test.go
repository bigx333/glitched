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

// RunAs has the program at path stand for the test binary as the claude
// tests may run, for the length of t: a script that starts it.
func RunAs(t *testing.T, path string) { t.Setenv(testClaude, path) }

// ProbeChunk is how much of a program the probe reads at a time.
const ProbeChunk = probeChunk

// Probe is the hidden flags the program at path holds, in order.
func Probe(path string) []string {
	var seen []string
	for _, f := range []string{flagResumeAt, flagDropsTurn, flagFork} {
		if probe(path)[f] {
			seen = append(seen, f)
		}
	}
	return seen
}

// SessionOf is the Claude session of thread.
func SessionOf(thread api.ID) string { return uuidOf(thread) }
