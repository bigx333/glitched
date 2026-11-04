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
//
// The backslashes just before an escape, or before a u{ of s's own, show
// doubled, so that a literal \u{FE0F} shows apart from the U+FE0F it
// spells: each text shows as one that reads back as it and no other. Every
// other backslash shows as it is, so a command reads as it runs.
func Text(s string, lines bool) (string, bool) {
	var b strings.Builder
	escaped := false
	slashes := 0 // backslashes not yet written, just before the next character
	for i, r := range s {
		if r == '\\' {
			slashes++
			continue
		}
		show := ' ' <= r && r <= '~' || r == '\n' && lines
		if slashes > 0 && (!show || strings.HasPrefix(s[i:], "u{")) {
			slashes *= 2
			escaped = true
		}
		b.WriteString(strings.Repeat(`\`, slashes))
		slashes = 0
		if show {
			b.WriteRune(r)
			continue
		}
		fmt.Fprintf(&b, `\u{%X}`, r)
		escaped = true
	}
	b.WriteString(strings.Repeat(`\`, slashes))
	return b.String(), escaped
}
