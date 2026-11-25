package runtime

import (
	"time"
)

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

// SetAwaitingStart calls f as an ask begins to wait for its run's start.
func SetAwaitingStart(f func()) func() {
	old := awaitingStart
	awaitingStart = f
	return func() { awaitingStart = old }
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

// LostContinued is closed once an auto-continuing host has decided each
// run its boot lost.
func (h *Host) LostContinued() <-chan struct{} { return h.lostDone }

// ContinuesFull is poked as an auto-continuing host waits for one of the
// runs it continued to end before it continues another.
func (h *Host) ContinuesFull() <-chan struct{} { return h.full }

// Closing is closed once h begins to close.
func (h *Host) Closing() <-chan struct{} { return h.ctx.Done() }
