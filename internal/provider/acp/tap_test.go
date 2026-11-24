package acp

import (
	"errors"
	"io"
	"slices"
	"testing"

	"glitched/internal/provider"
)

type failing struct{}

func (failing) Write([]byte) (int, error) { return 0, errors.New("closed") }

// The agent's stdin is tapped only for a write that went through, by its
// length without its line ending.
func TestStdinIsTappedOnceWritten(t *testing.T) {
	var seen []int
	tap := provider.Tap(func(_ provider.Protocol, _ provider.Pipe, _ []byte, size int) { seen = append(seen, size) })
	line := []byte("{\"id\":1}\n")
	tapped{w: failing{}, wire: io.Discard, tap: tap}.Write(line)
	tapped{w: io.Discard, wire: io.Discard, tap: tap}.Write(line)
	if !slices.Equal(seen, []int{8}) {
		t.Errorf("the tap saw lines of %v bytes, want one of 8", seen)
	}
}
