package api

import (
	"errors"
	"fmt"
)

var (
	// ErrConflict: a known command ID arrived with a different body.
	ErrConflict = errors.New("api: command id reused with a different body")
	// ErrRevision: the command's revision is behind the target's.
	ErrRevision = errors.New("api: stale revision")
	// ErrExpired: the command arrived after its Expires.
	ErrExpired = errors.New("api: command expired")
	// ErrNotFound: the target thread is unknown on this host.
	ErrNotFound = errors.New("api: not found")
	// ErrStaleEpoch: an upload or command carries an epoch older than the
	// one already recorded.
	ErrStaleEpoch = errors.New("api: stale epoch")
	// ErrAlreadyAnswered: a request was answered by someone else first.
	ErrAlreadyAnswered = errors.New("api: already answered")
	// ErrOverflow ends a stream whose subscriber fell too far behind. The
	// client reopens it from the last cursor it holds.
	ErrOverflow = errors.New("api: stream overflow")
)

// RejectedError is a command the engine refused by policy or state.
type RejectedError struct{ Reason string }

func (e *RejectedError) Error() string { return "api: rejected: " + e.Reason }

// MovedError answers a command sent to a host that no longer owns the
// thread. The client re-sends the same command ID to To.
type MovedError struct {
	To    ID
	Epoch uint64
}

func (e *MovedError) Error() string {
	return fmt.Sprintf("api: moved to %s at epoch %d", e.To, e.Epoch)
}
