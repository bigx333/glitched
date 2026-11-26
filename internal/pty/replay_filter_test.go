package pty

import "testing"

func TestStripReplayQueries(t *testing.T) {
	dropped := map[string]string{
		"DA1":                  "\x1b[c",
		"DA1 with 0":           "\x1b[0c",
		"DA2":                  "\x1b[>c",
		"DA3":                  "\x1b[=c",
		"DSR status":           "\x1b[5n",
		"CPR":                  "\x1b[6n",
		"DECXCPR":              "\x1b[?6n",
		"color scheme":         "\x1b[?996n",
		"DECRQM":               "\x1b[?2026$p",
		"ANSI DECRQM":          "\x1b[4$p",
		"XTVERSION":            "\x1b[>q",
		"XTVERSION 0":          "\x1b[>0q",
		"kitty keyboard":       "\x1b[?u",
		"text area pixels":     "\x1b[14t",
		"cell pixels":          "\x1b[16t",
		"text area cells":      "\x1b[18t",
		"screen cells":         "\x1b[19t",
		"icon label":           "\x1b[20t",
		"window title":         "\x1b[21t",
		"DECREQTPARM":          "\x1b[x",
		"DECID":                "\x1bZ",
		"palette color":        "\x1b]4;1;?\x07",
		"palette colors":       "\x1b]4;1;?;2;?\x1b\\",
		"foreground":           "\x1b]10;?\x07",
		"background":           "\x1b]11;?\x1b\\",
		"cursor color":         "\x1b]12;?\x07",
		"highlight background": "\x1b]17;?\x07",
		"highlight foreground": "\x1b]19;?\x07",
		"clipboard read":       "\x1b]52;c;?\x07",
		"kitty color query":    "\x1b]21;foreground=?;background=?\x1b\\",
		"kitty clipboard read": "\x1b]5522;type=read;Lg==\x1b\\",
		"DECRQSS":              "\x1bP$qm\x1b\\",
		"XTGETTCAP":            "\x1bP+q544e\x1b\\",
		"kitty graphics query": "\x1b_Gi=31,s=1,v=1,a=q,t=d,f=24;AAAA\x1b\\",
	}
	for name, q := range dropped {
		t.Run(name, func(t *testing.T) {
			in := "before" + q + "after"
			if got := string(stripReplayQueries([]byte(in))); got != "beforeafter" {
				t.Fatalf("stripReplayQueries(%q) = %q, want %q", in, got, "beforeafter")
			}
		})
	}

	kept := map[string]string{
		"text":                 "hello, wörld 👋\r\n",
		"SGR":                  "\x1b[1;38;5;196mred\x1b[0m",
		"cursor moves":         "\x1b[2;5H\x1b[3A\x1b[K",
		"alt screen":           "\x1b[?1049h\x1b[?1049l",
		"title":                "\x1b]0;my title\x07\x1b]2;?\x1b\\",
		"cwd":                  "\x1b]7;file://host/tmp\x1b\\",
		"hyperlink":            "\x1b]8;;https://example.com/?q=1\x1b\\link\x1b]8;;\x1b\\",
		"set color":            "\x1b]11;#000000\x07\x1b]4;1;rgb:ff/00/00\x07",
		"clipboard write":      "\x1b]52;c;aGk=\x07",
		"cursor style":         "\x1b[2 q",
		"soft reset":           "\x1b[!p",
		"kitty keyboard push":  "\x1b[>1u\x1b[<u",
		"title stack":          "\x1b[22;0t\x1b[23;0t",
		"window resize":        "\x1b[8;24;80t",
		"DECSACE":              "\x1b[2*x",
		"DCS other":            "\x1bPq#0;2;0;0;0\x1b\\",
		"kitty graphics quiet": "\x1b_Ga=T,i=1,q=2;AAAA\x1b\\",
		"DSR answer echoed":    "^[[?62;22c",
		"unterminated CSI":     "abc\x1b[",
		"unterminated OSC":     "abc\x1b]11;?",
		"lone ESC at end":      "abc\x1b",
		"aborted by CAN":       "\x1b[\x18c",
		"save cursor":          "\x1b7\x1b8",
	}
	silenced := map[string][2]string{
		"kitty graphics transmit": {"\x1b_Ga=T,i=5,f=24;AAAA\x1b\\", "\x1b_Ga=T,i=5,f=24,q=2;AAAA\x1b\\"},
		"kitty graphics errors":   {"\x1b_Ga=p,i=5,q=1\x1b\\", "\x1b_Ga=p,i=5,q=2\x1b\\"},
		"kitty graphics no keys":  {"\x1b_G;AAAA\x1b\\", "\x1b_Gq=2;AAAA\x1b\\"},
	}
	for name, c := range silenced {
		t.Run("silences "+name, func(t *testing.T) {
			if got := string(stripReplayQueries([]byte(c[0]))); got != c[1] {
				t.Fatalf("stripReplayQueries(%q) = %q, want %q", c[0], got, c[1])
			}
		})
	}
	for name, s := range kept {
		t.Run("keeps "+name, func(t *testing.T) {
			if got := string(stripReplayQueries([]byte(s))); got != s {
				t.Fatalf("stripReplayQueries(%q) = %q, want it unchanged", s, got)
			}
		})
	}
}

func TestReplayEditsWithSplitPrefix(t *testing.T) {
	edits := replayEditsWithPrefix([]byte("abc\x1b["), []byte("cdef"))
	got, _ := splice([]byte("cdef"), 0, len("cdef"), edits)
	if string(got) != "\x18def" {
		t.Fatalf("split DA1 = %q, want %q", string(got), "\x18def")
	}

	edits = replayEditsWithPrefix([]byte("abc\x1b]10;"), []byte("?\x07tail"))
	got, _ = splice([]byte("?\x07tail"), 0, len("?\x07tail"), edits)
	if string(got) != "\x18tail" {
		t.Fatalf("split OSC 10 = %q, want %q", string(got), "\x18tail")
	}
}
