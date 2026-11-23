// Package sweep finds and stops the processes an agent adapter started,
// by a mark each carries in its environment, and everything they started.
// It is how an adapter stops an agent's leftovers after the agent, or the
// engine that ran it, is gone.
package sweep

import "time"

// Mark is the environment variable that marks an adapter's processes:
// Env=<thread>-<launch>, so a prefix finds a thread's or one launch's.
type Mark struct{ Env string }

// KillGrace is how long a process asked to stop has before it is killed.
const KillGrace = 2 * time.Second
