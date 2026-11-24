package runtime

import (
	"context"
	"time"

	"glitched/internal/api"
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

// SetCheckpointBudgets sets the base capture's budget, the most files and
// bytes a checkpoint holds, the most the stores hold, and the run
// checkpoints a thread keeps.
func SetCheckpointBudgets(base time.Duration, files int, bytes, limit int64, kept int) func() {
	b, f, n, l, k := baseBudget, maxCheckpointFiles, maxCheckpointBytes, checkpointLimit, keptRuns
	baseBudget, maxCheckpointFiles, maxCheckpointBytes, checkpointLimit, keptRuns = base, files, bytes, limit, kept
	return func() { baseBudget, maxCheckpointFiles, maxCheckpointBytes, checkpointLimit, keptRuns = b, f, n, l, k }
}

// SetSeedBudget sets how long seeding an empty checkpoint store may take.
func SetSeedBudget(d time.Duration) func() {
	old := seedBudget
	seedBudget = d
	return func() { seedBudget = old }
}

// SetCheckpointHook calls f as a checkpoint store's refs change: step is
// "captured" after a capture wrote its ref, "dropped" after a drop deleted
// a store's.
func SetCheckpointHook(f func(ctx context.Context, step string)) func() {
	old := checkpointHook
	checkpointHook = f
	return func() { checkpointHook = old }
}

// Waiting is whether thread's start waits on a revert in its folder.
func Waiting(h *Host, thread api.ID) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.waiting[thread]
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
