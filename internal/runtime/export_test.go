package runtime

import "time"

func SetCallTimeout(d time.Duration) func() {
	old := callTimeout
	callTimeout = d
	return func() { callTimeout = old }
}

func SetAskTimeout(d time.Duration) func() {
	old := askTimeout
	askTimeout = d
	return func() { askTimeout = old }
}

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
