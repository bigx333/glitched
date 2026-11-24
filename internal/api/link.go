package api

import (
	"context"
	"errors"
)

// A Link is the scope of a token this engine minted for another machine's
// engine, so that agents there can start and follow children here: the
// one standing grant between two engines (docs/daemon/placement-and-handoff.md).
// ID is random and never reused, unlike the label a revoke frees, so what a
// link made is stamped, counted and found by it alone.
type Link struct {
	ID    ID     `json:"id"`
	Label string `json:"label"`
	// Access is the most a child the link starts may run at, under this
	// engine's own setting for children from other machines.
	Access Access `json:"access"`
	// Working and All cap the link's children that work at once and that
	// exist at all; zero takes the engine's child limits.
	Working int `json:"working,omitempty"`
	All     int `json:"all,omitempty"`
}

// ErrNotAllowed is a call's error when the caller's link may not make it.
var ErrNotAllowed = errors.New("api: a link from another machine may not make this call")

type linkKey struct{}

// WithLink is ctx for a call a link's token made.
func WithLink(ctx context.Context, l Link) context.Context {
	return context.WithValue(ctx, linkKey{}, l)
}

// LinkOf is the link whose token made ctx's call, if one did.
func LinkOf(ctx context.Context) (Link, bool) {
	l, ok := ctx.Value(linkKey{}).(Link)
	return l, ok
}
