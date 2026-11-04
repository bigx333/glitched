// Package plain shows an agent's text as printable ASCII, so that a person
// approving it reads what an allow approves.
package plain

import (
	"fmt"
	"strings"
)

// Text is s as a person approving it reads it: printable ASCII as it is, a
// line break as one where lines allows it, and every other character as
// an escape such as \u{5D0}; and whether it escaped any. Drawn as they
// are, a right-to-left letter can reverse the line it starts, a bidi
// control can reorder it, and an invisible mark can change the file a
// command names without changing how it looks.
func Text(s string, lines bool) (string, bool) {
	var b strings.Builder
	escaped := false
	for _, r := range s {
		if ' ' <= r && r <= '~' || r == '\n' && lines {
			b.WriteRune(r)
			continue
		}
		fmt.Fprintf(&b, `\u{%X}`, r)
		escaped = true
	}
	return b.String(), escaped
}
