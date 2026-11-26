package api

import (
	"context"
	"time"
)

// TerminalOpen opens or reattaches to an engine-owned PTY session for a workspace.
type TerminalOpen struct {
	Workspace string `json:"workspace"`
	Session   ID     `json:"session,omitzero"`
	After     uint64 `json:"after,omitempty"`
	Cols      int    `json:"cols,omitempty"`
	Rows      int    `json:"rows,omitempty"`
	Client    string `json:"client,omitempty"`
}

// TerminalItem is a chunk of PTY output or state change streamed from the engine.
type TerminalItem struct {
	Session     ID     `json:"session,omitzero"`
	Offset      uint64 `json:"offset,omitempty"`
	Truncated   bool   `json:"truncated,omitempty"`
	Data        []byte `json:"data,omitempty"`
	Cols        int    `json:"cols,omitempty"`
	Rows        int    `json:"rows,omitempty"`
	LeaseClient string `json:"leaseClient,omitempty"`
	Exited      bool   `json:"exited,omitempty"`
	ExitCode    int    `json:"exitCode,omitempty"`
}

// TerminalInput writes keystrokes or pasted bytes to a workspace's PTY.
type TerminalInput struct {
	Workspace string `json:"workspace"`
	Session   ID     `json:"session,omitzero"`
	Client    string `json:"client,omitempty"`
	Data      []byte `json:"data"`
}

// TerminalResize updates the PTY window dimensions.
type TerminalResize struct {
	Workspace string `json:"workspace"`
	Session   ID     `json:"session,omitzero"`
	Client    string `json:"client,omitempty"`
	Cols      int    `json:"cols"`
	Rows      int    `json:"rows"`
}

// TerminalClaim acquires or steals the expiring terminal input lease.
type TerminalClaim struct {
	Workspace string `json:"workspace"`
	Session   ID     `json:"session,omitzero"`
	Client    string `json:"client"`
	Force     bool   `json:"force,omitempty"`
}

// TerminalLease describes the current owner and expiration of a session's input lease.
type TerminalLease struct {
	Session ID        `json:"session,omitzero"`
	Client  string    `json:"client,omitempty"`
	Until   time.Time `json:"until,omitzero"`
}

// TerminalStream reads TerminalItem frames until the session exits or the stream closes.
type TerminalStream interface {
	Next(ctx context.Context) (TerminalItem, error)
	Close() error
}
