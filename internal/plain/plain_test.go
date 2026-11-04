package plain

import "testing"

func TestText(t *testing.T) {
	for _, tc := range []struct {
		in    string
		lines bool
		want  string
	}{
		{"rm -rf ~/work # echo safe", false, "rm -rf ~/work # echo safe"},
		{"a\nb", true, "a\nb"},
		{"a\nb", false, `a\u{A}b`},
		{"a\tb\r\x00\x1b\x7f", true, `a\u{9}b\u{D}\u{0}\u{1B}\u{7F}`},
		{": א; rm ب", false, `: \u{5D0}; rm \u{628}`},
		{"‮⁦‏؜  ", true, `\u{202E}\u{2066}\u{200F}\u{61C}\u{2028}\u{2029}`},
		{"./check️\U000E0100͏\U000E0000", false, `./check\u{FE0F}\u{E0100}\u{34F}\u{E0000}`},
		{" ㅤᅟ⠀\u0085\u009b", true, `\u{A0}\u{3164}\u{115F}\u{2800}\u{85}\u{9B}`},
		{"\u0435xec \u00e9 e\u0301", false, `\u{435}xec \u{E9} e\u{301}`},
		{"bad\xff", false, `bad\u{FFFD}`},
	} {
		got, escaped := Text(tc.in, tc.lines)
		if got != tc.want {
			t.Errorf("Text(%+q, %v) = %q, want %q", tc.in, tc.lines, got, tc.want)
		}
		if want := got != tc.in; escaped != want {
			t.Errorf("Text(%+q, %v) says escaped %v, want %v", tc.in, tc.lines, escaped, want)
		}
	}
}
