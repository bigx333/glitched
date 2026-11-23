//go:build !darwin

package daemon

import "context"

// inhibit is none off a Mac: a Linux engine is mostly a server that doesn't
// sleep, and systemd-inhibit's sleep lock needs a polkit grant a service
// user rarely has.
var inhibit func(context.Context) error
