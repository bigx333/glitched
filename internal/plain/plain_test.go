package plain

import (
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestText(t *testing.T) {
	for _, tc := range []struct {
		name        string
		in          string
		lines       bool
		want        string
		wantEscaped bool
	}{
		{"empty string", "", false, "", false},
		{"printable ASCII bounds space and tilde", " ~", false, " ~", false},
		{"plain shell command", "rm -rf ~/work # echo safe", false, "rm -rf ~/work # echo safe", false},
		{"newline kept when lines enabled", "a\nb", true, "a\nb", false},
		{"newline escaped when single-line", "a\nb", false, `a\u{A}b`, true},
		{"ascii control characters", "a\tb\r\x00\x1b\x7f", true, `a\u{9}b\u{D}\u{0}\u{1B}\u{7F}`, true},
		{"c1 control sequences", "\u0085\u009b2K", true, `\u{85}\u{9B}2K`, true},
		{"right-to-left Hebrew and Arabic letters", ": א; rm ب", false, `: \u{5D0}; rm \u{628}`, true},
		{"bidi overrides isolates and line separators", "\u202e\u2066\u200f\u061c\u2028\u2029\u2069\u202c", true, `\u{202E}\u{2066}\u{200F}\u{61C}\u{2028}\u{2029}\u{2069}\u{202C}`, true},
		{"variation selectors joiner and tag characters", "./check\ufe0f\U000E0100\u034f\U000E0000\U000E0041", false, `./check\u{FE0F}\u{E0100}\u{34F}\u{E0000}\u{E0041}`, true},
		{"combining stroke", "echo safe; r\u0336m", false, `echo safe; r\u{336}m`, true},
		{"invisible fillers and no-break spaces", "\u00a0\u3164\u115f\u2800", true, `\u{A0}\u{3164}\u{115F}\u{2800}`, true},
		{"zero width space and byte order mark", "DR\u200bOP\ufeff", true, `DR\u{200B}OP\u{FEFF}`, true},
		{"cyrillic look-alike and combining accent", "\u0435xec \u00e9 e\u0301", false, `\u{435}xec \u{E9} e\u{301}`, true},
		{"invalid UTF-8 byte becomes replacement rune", "bad\xff", false, `bad\u{FFFD}`, true},
		{"literal escape sequence doubled", `./check\u{FE0F}`, false, `./check\\u{FE0F}`, true},
		{"two backslashes before u brace doubled", `a\\u{41}`, false, `a\\\\u{41}`, true},
		{"backslash before non-ASCII doubled", "a\\\ufe0f", false, `a\\\u{FE0F}`, true},
		{"ordinary backslashes untouched", `sed 's/\./,/g'; printf 'a\\b\n' \u00e9 \u`, false, `sed 's/\./,/g'; printf 'a\\b\n' \u00e9 \u`, false},
		{"trailing backslash untouched", `foo\`, false, `foo\`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, escaped := Text(tc.in, tc.lines)
			if got != tc.want {
				t.Errorf("Text(%+q, %v) = %q, want %q", tc.in, tc.lines, got, tc.want)
			}
			if escaped != tc.wantEscaped {
				t.Errorf("Text(%+q, %v) escaped = %v, want %v", tc.in, tc.lines, escaped, tc.wantEscaped)
			}
		})
	}
}

// A literal \u{FE0F} and a real U+FE0F name different files, so they
// show differently.
func TestTextShowsALiteralEscapeApart(t *testing.T) {
	literal, _ := Text(`./check\u{FE0F}`, false)
	real, _ := Text("./check\ufe0f", false)
	if literal == real {
		t.Errorf("a literal \\u{FE0F} and U+FE0F both show as %q", literal)
	}
}

// Every text shows as one that reads back as it and no other.
func FuzzText(f *testing.F) {
	for _, s := range []string{`./check\u{FE0F}`, "./check\ufe0f", `\\u{41}`, "\\\\\ufe0f", `\u`, `\u{`, "\\\n", `sed 's/\./,/'`, "a\nb\x1b[8m"} {
		f.Add(s, false)
		f.Add(s, true)
	}
	f.Fuzz(func(t *testing.T, s string, lines bool) {
		if !utf8.ValidString(s) {
			t.Skip()
		}
		shown, _ := Text(s, lines)
		back, ok := read(shown)
		if !ok || back != s {
			t.Errorf("Text(%+q, %v) = %q, which reads back as %+q", s, lines, shown, back)
		}
		for _, r := range shown {
			if (r < ' ' || r > '~') && !(r == '\n' && lines) {
				t.Errorf("Text(%+q, %v) = %q, which holds %U", s, lines, shown, r)
			}
		}
	})
}

// read is the text a person reads in shown: a \u{X} is the character X;
// a run of n backslashes just before u{ is n/2 of them, and the escape
// when n is odd; and every other character is itself.
func read(shown string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(shown); {
		if shown[i] != '\\' {
			b.WriteByte(shown[i])
			i++
			continue
		}
		n := 0
		for i+n < len(shown) && shown[i+n] == '\\' {
			n++
		}
		if !strings.HasPrefix(shown[i+n:], "u{") {
			b.WriteString(shown[i : i+n])
			i += n
			continue
		}
		b.WriteString(strings.Repeat(`\`, n/2))
		i += n
		if n%2 == 0 {
			continue
		}
		end := strings.IndexByte(shown[i:], '}')
		if end < 0 {
			return "", false
		}
		r, err := strconv.ParseUint(shown[i+2:i+end], 16, 32)
		if err != nil {
			return "", false
		}
		b.WriteRune(rune(r))
		i += end + 1
	}
	return b.String(), true
}
