package api

import (
	"context"
	"errors"
)

// A Link is the scope of a token this engine minted for another machine's
// engine, so that agents there can start and follow children here: the
// one standing grant between two engines (docs/daemon/placement-and-handoff.md).
// ID is random and never reused, unlike the label a removal frees, so what a
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

// Origin is where a link's thread came from: Thread, titled Title, on the
// engine Engine of the machine that holds the link, which starts it as a
// child of its own, Depth below a thread of its person's, at most at
// Access, the access that machine says its parent has. Link is the link,
// which this engine stamps from the token that asked, never from the
// body.
type Origin struct {
	Engine ID     `json:"engine"`
	Thread ID     `json:"thread"`
	Title  string `json:"title"`
	Depth  int    `json:"depth"`
	Access Access `json:"access"`
	Link   ID     `json:"link,omitzero"`
}

// Repository names a repository by its root commit, as placement does
// (docs/daemon/placement-and-handoff.md), and the branch of it a link's
// thread starts from.
type Repository struct {
	Root   string `json:"root"`
	Branch string `json:"branch"`
}

// ChildReplyQuery asks for what Run, a run of Thread's, said last.
type ChildReplyQuery struct {
	Thread ID `json:"thread"`
	Run    ID `json:"run"`
}

// ChildReply is what a run's agent said last, as a local child's report
// carries it.
type ChildReply struct {
	Text string `json:"text"`
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
