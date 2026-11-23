package secrets

import (
	"cmp"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"
)

// MinRedacted is the shortest value Redact hides: a shorter one, as a PIN,
// would hide every like string a transcript holds.
const MinRedacted = 8

// Redact replaces each of values in s, MinRedacted bytes or longer, with
// its name in brackets, longest first, so a value inside another goes
// with the longer one.
func Redact(s string, values map[string]string) string {
	s, _ = redact(s, values, false)
	return s
}

// RedactStream is Redact for text that goes on: it returns what of s may
// be shown now, redacted, and keeps back the end of s that could be the
// start of a value, for the next call. final shows all of it.
func RedactStream(s string, values map[string]string, final bool) (shown, kept string) {
	return redact(s, values, !final)
}

type secret struct{ name, value string }

func redact(s string, values map[string]string, hold bool) (string, string) {
	var hidden []secret
	longest := 0
	for _, name := range slices.Sorted(maps.Keys(values)) {
		if v := values[name]; len(v) >= MinRedacted {
			hidden = append(hidden, secret{name, v})
			longest = max(longest, len(v))
		}
	}
	if len(hidden) == 0 {
		return s, ""
	}
	slices.SortStableFunc(hidden, func(a, b secret) int { return cmp.Compare(len(b.value), len(a.value)) })
	var out strings.Builder
	i := 0
	for i < len(s) {
		if hold && len(s)-i < longest && couldStart(s[i:], hidden) {
			break
		}
		matched := false
		for _, h := range hidden {
			if strings.HasPrefix(s[i:], h.value) {
				out.WriteString("[" + h.name + "]")
				i += len(h.value)
				matched = true
				break
			}
		}
		if !matched {
			_, n := utf8.DecodeRuneInString(s[i:])
			out.WriteString(s[i : i+n])
			i += n
		}
	}
	return out.String(), s[i:]
}

// couldStart is whether rest, shorter than some value, is how one of them
// starts, so more text could complete it.
func couldStart(rest string, hidden []secret) bool {
	return slices.ContainsFunc(hidden, func(h secret) bool { return len(rest) < len(h.value) && strings.HasPrefix(h.value, rest) })
}
