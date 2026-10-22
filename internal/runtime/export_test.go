package runtime

import "time"

// SetCallTimeout shortens the deadline on provider calls and returns what
// puts it back.
func SetCallTimeout(d time.Duration) func() {
	old := callTimeout
	callTimeout = d
	return func() { callTimeout = old }
}

// SetCloseGrace shortens how long Close waits for commits and returns what
// puts it back.
func SetCloseGrace(d time.Duration) func() {
	old := closeGrace
	closeGrace = d
	return func() { closeGrace = old }
}

// Kill stops h as a killed process stops: from the call on, nothing it took
// from an agent or the outbox reaches the store, and no agent is closed.
func (h *Host) Kill() {
	h.stopDrain()
	h.Close()
}
