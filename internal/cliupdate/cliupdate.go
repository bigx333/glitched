// Package cliupdate learns whether an agent CLI on this machine has a newer
// version than the one installed, and how it updates: the CLI's own
// updater for how it was installed, or a command for the user to run.
package cliupdate

import (
	"context"
	"time"
)

// Status is what the engine knows of a provider's CLI versions on its
// machine. Latest is "" while no lookup has answered. Available is that
// Latest is newer than Installed, both parsed; a version that doesn't
// parse is unknown, never an update. Command is what Update runs, or,
// when Manual says why Glitched won't run it, what the user can.
type Status struct {
	Installed string    `json:"installed,omitempty"`
	Latest    string    `json:"latest,omitempty"`
	Checked   time.Time `json:"checked,omitzero"`
	Available bool      `json:"available,omitempty"`
	Command   []string  `json:"command,omitempty"`
	Manual    string    `json:"manual,omitempty"`
}

// Lookup is the one outbound request this package makes: the latest
// version a source publishes. Tests give a fake; nothing in a test or in
// CI reaches the network.
type Lookup interface {
	Latest(ctx context.Context, s Source) (string, error)
}

// Source is where one install's latest version is published: a URL that
// answers JSON, and the dotted Field that holds the version in it, which
// may carry Trim before it, as Codex's "rust-v".
type Source struct {
	URL   string `json:"url"`
	Field string `json:"field"`
	Trim  string `json:"trim,omitempty"`
}
