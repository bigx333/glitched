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
	// ErrExpired: the command arrived after its Expires, or after the
	// engine pruned the receipt its ID may have taken.
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
	// ErrProtocol refuses a peer that speaks another Protocol.
	ErrProtocol = errors.New("api: the peer speaks another protocol")
)

// ProtocolError refuses a peer that speaks protocol Theirs, not this
// one's. It is ErrProtocol.
type ProtocolError struct{ Theirs int }

func (e *ProtocolError) Error() string {
	return fmt.Sprintf("api: the peer speaks protocol %d and this one %d; update the older one", e.Theirs, Protocol)
}

func (e *ProtocolError) Is(err error) bool { return err == ErrProtocol }

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
