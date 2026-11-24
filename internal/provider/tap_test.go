package provider

import (
	"strings"
	"testing"
)

// A tap sees a line's length without its line ending, a long line's as a
// short one's, and no more of it than TapCap bytes.
func TestATapSeesALinesLengthWithoutItsEnding(t *testing.T) {
	long := strings.Repeat("x", TapCap+10)
	for _, c := range []struct {
		line       string
		seen, size int
	}{
		{"short\n", 5, 5},
		{"short\r\n", 5, 5},
		{"short", 5, 5},
		{long + "\n", TapCap, TapCap + 10},
		{long + "\r\n", TapCap, TapCap + 10},
	} {
		var seen, size int
		Tap(func(_ Protocol, _ Pipe, line []byte, n int) { seen, size = len(line), n }).Line(ACP, Stdout, []byte(c.line))
		if seen != c.seen || size != c.size {
			t.Errorf("a line of %d bytes is seen as %d of %d, want %d of %d", len(c.line), seen, size, c.seen, c.size)
		}
	}
}
