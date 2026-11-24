package runtime

import (
	"context"
	"glitched/internal/api"
	"io"
	"testing"
	"time"

	"glitched/internal/transport"
)

// A refused handshake loses a child only the second time in a row: a
// network failure between two starts the count over.
func TestARefusalBetweenNetworkFailuresStartsTheCountOver(t *testing.T) {
	h := Host{backoff: func(int) time.Duration { return 0 }}
	errs := []error{transport.ErrRefused, io.EOF, transport.ErrRefused, nil}
	n := 0
	lost, _, ok := h.persist(context.Background(), func(context.Context) (string, bool, error) { err := errs[n]; n++; return "b", false, err })
	if lost != "" || !ok || n != 4 {
		t.Fatalf("refused, EOF, refused, success: lost %q after %d tries; want none lost", lost, n)
	}
}

// A catch-up between two refusals starts their count over: the dial
// before it was taken.
func TestARefusalBetweenCatchUpsStartsTheCountOver(t *testing.T) {
	h := Host{backoff: func(int) time.Duration { return 0 }}
	errs := []error{transport.ErrRefused, api.ErrOverflow, transport.ErrRefused, nil}
	n := 0
	lost, _, _ := h.persist(context.Background(), func(context.Context) (string, bool, error) { err := errs[n]; n++; return "B", false, err })
	if lost != "" {
		t.Fatalf("refused, authenticated subscription overflow, refused, success: lost after %d attempts: %s", n, lost)
	}
}
