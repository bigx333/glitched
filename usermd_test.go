package main

import (
	"fmt"
	"glitched/internal/daemon"
	"glitched/internal/provider/fake"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// sent seeds a thread with the user's messages and returns the texts
// its transcript shows.
func sent(t *testing.T, msgs ...string) *harness {
	t.Helper()
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(checks))
	seed(t, d, t.TempDir(), "Messages", msgs...)
	h := start(t, dir)
	h.tt.SetSize(1320, 12000)
	h.a.select_(h.thread("Messages"))
	h.until("its transcript", func() bool { return h.tt.HasText("One test fails.") })
	h.tt.Advance(time.Second)
	return h
}

// A message's code spans and fenced code blocks show as code, without
// their backticks, as a reply's do.
func TestAUserMessageShowsItsCode(t *testing.T) {
	h := sent(t,
		"Why does `break` leave only the inner loop?\n\n```go\nfor {\n\tbreak\n}\n```",
		"~~~\n```\nx\n~~~\nafter",
		"```npm i``` first\nthen b",
	)
	texts := h.tt.Texts()
	if slices.ContainsFunc(texts, func(s string) bool { return strings.Contains(s, "`break`") || strings.Contains(s, "```go") }) {
		t.Errorf("a message shows its code's backticks; texts %q", texts)
	}
	for _, s := range []string{"Why does break leave only the inner loop?", "for {\n\tbreak\n}", "```\nx", "after", "npm i first\nthen b"} {
		if !slices.Contains(texts, s) {
			t.Errorf("no %q; texts %q", s, texts)
		}
	}
}

// Outside its code, a message with code shows exactly as the user typed
// it: its lines, blank lines and indents, and every mark Markdown would
// read.
func TestAUserMessageShowsAsTyped(t *testing.T) {
	typed := []string{
		"Steps:\nrun make\nthen run tests",
		"line one\nline two\n---",
		"Status\n====",
		"Traceback:\n  File \"main.py\", line 3\n    return x",
		"\tindented with a tab",
		`Use ^\d+\.\d+$ on C:\Users\me\_build`,
		"What is <b> vs &nbsp; or &amp;? line one<br>line two",
		"[ERROR]: timeout\nwhat now?",
		`It matches \[a-z\]+ and *a* or __b__ ~~c~~ $x$`,
		"# not a heading\n> not a quote\n- not a list\n1. not ordered\n| not | a table |",
		"a\n\n\n\nb",
		"#",
		"See [the settings](/settings) or https://example.com",
		"a\\\nb",
		"```",
		"text\n~~~~",
		"    ```\n# not a heading\n    ```",
	}
	withCode := make([]string, len(typed))
	for i, s := range typed {
		withCode[i] = s + " `c`"
	}
	h := sent(t, withCode...)
	texts := h.tt.Texts()
	for _, s := range typed {
		if !slices.Contains(texts, s+" c") {
			t.Errorf("no %q as typed; texts %q", s+" c", texts)
		}
	}
}

// A code span ends on its line: backticks left open show as typed, and
// what follows on later lines stays as typed, never Markdown.
func TestAUserMessagesCodeSpanEndsOnItsLine(t *testing.T) {
	h := sent(t,
		"I ran `make` then `go test\n# FAIL\n[open settings](/settings) and see `foo`",
		"run `go test ./... and then\n- it fails on `TestFoo`",
	)
	for _, s := range []string{
		"I ran make then `go test\n# FAIL\n[open settings](/settings) and see foo",
		"run `go test ./... and then\n- it fails on TestFoo",
	} {
		if !slices.Contains(h.tt.Texts(), s) {
			t.Errorf("no %q; texts %q", s, h.tt.Texts())
		}
	}
}

// A fence closes only with spaces or tabs after it, as Markdown reads
// it, so the text after a fence closed by other spaces stays code.
func TestAUserMessagesFenceClosesAsMarkdownReadsIt(t *testing.T) {
	h := sent(t,
		"```\nx\n```\u00a0\nthen *bold*",
		"```\ny\n    ```\n*indented* `c`",
		"```\nz\n```  \n*spaced* `c`",
		"```\nw\n````\n*longer* `c`",
	)
	for _, want := range []string{"x\n```\u00a0\nthen *bold*", "y\n    ```\n*indented* `c`", "z", "*spaced* c", "w", "*longer* c"} {
		if !slices.Contains(h.tt.Texts(), want) {
			t.Errorf("no %q; texts %q", want, h.tt.Texts())
		}
	}
}

// Blank lines between a message's text and its code show as typed, but
// for the one each side the code's spacing shows, and so do blank lines
// that border no code.
func TestAUserMessageKeepsBlankLinesAroundCode(t *testing.T) {
	h := sent(t, "tight\n```\ncode tight\n```\nafter `c`", "loose\n\n\n\n```\ncode loose\n```\n\nafter one `c`", "\n\nleading `c`")
	gap := func(text, code string) float32 {
		a, _ := h.tt.Find(text)
		b, _ := h.tt.Find(code)
		return b.Y - a.Y
	}
	if tight, loose := gap("tight", "code tight"), gap("loose", "code loose"); loose <= tight {
		t.Errorf("the code is %v below three blank lines, %v below none", loose, tight)
	}
	if slices.ContainsFunc(h.tt.Texts(), func(s string) bool { return strings.Contains(s, "\\") }) {
		t.Errorf("a backslash shows that wasn't typed; texts %q", h.tt.Texts())
	}
	for _, s := range []string{"after c", "after one c", "\n\nleading c"} {
		if !slices.Contains(h.tt.Texts(), s) {
			t.Errorf("no %q; texts %q", s, h.tt.Texts())
		}
	}
}

// In a message with code, line endings of other systems end lines as
// line feeds do, and a byte order mark, which shows as nothing, leaves
// the first line's indent.
func TestAUserMessageReadsAnyLineEnding(t *testing.T) {
	h := sent(t, "crlf `one`\r\ncrlf two", "cr `one`\rcr two", "\ufeff    if `a_b` {\n    }", "\ufeff\ufeff    if `b_c` {\n    }")
	for _, s := range []string{"crlf one\ncrlf two", "cr one\ncr two", "    if a_b {\n    }", "    if b_c {\n    }"} {
		if !slices.Contains(h.tt.Texts(), s) {
			t.Errorf("no %q; texts %q", s, h.tt.Texts())
		}
	}
}

// Nothing a message with code holds moves the window to the app's own
// pages.
func TestAUserMessageStaysOffTheAppsPages(t *testing.T) {
	const msg = "See [the settings](/settings) `now`."
	h := sent(t, msg)
	h.click("See [the settings](/settings) now.")
	h.tt.Advance(time.Second)
	if p := h.a.router.Path(); p != "/" {
		t.Errorf("at %q after clicking the message", p)
	}
}

// A link between runs of backticks too long to open a code span, which
// Markdown leaves as text, opens nothing either.
func TestAUserMessagesLongBacktickRunsOpenNothing(t *testing.T) {
	run := strings.Repeat("`", 1001)
	h := sent(t, run+" [click here](https://example.org) "+run+" `c`")
	if r, ok := h.tt.Find("click here"); ok {
		h.tt.ClickAt(r.X+r.W/2, r.Y+r.H/2)
	}
	if o := h.tt.OpenedURLs(); len(o) > 0 || !h.tt.HasText("[click here](https://example.org)") {
		t.Errorf("opened %q; texts %q", o, h.tt.Texts())
	}
}

// A code block whose fence the user left open copies its code.
func TestAUserMessagesOpenFenceCopies(t *testing.T) {
	h := sent(t, "left open:\n```\nx := 1")
	h.click("Copy code")
	if c := h.tt.Clipboard(); c != "x := 1\n" {
		t.Errorf("clipboard %q", c)
	}
}

// An address in a message with code opens nothing.
func TestAUserMessagesAddressOpensNothing(t *testing.T) {
	const msg = "Ping ops@example.com about `it`"
	h := sent(t, msg)
	if r, ok := h.tt.Find("ops@example.com"); ok {
		h.tt.ClickAt(r.X+r.W/2, r.Y+r.H/2)
	}
	h.click("Ping ops@example.com about it")
	if o := h.tt.OpenedURLs(); len(o) > 0 {
		t.Errorf("opened %q", o)
	}
}

// What the user's messages show as is kept for so many of them only.
func TestUserMarkdownIsKeptForSoMany(t *testing.T) {
	a := &app{}
	for i := range 3 * userMDsKept {
		a.userMarkdown(fmt.Sprint(i), "a")
	}
	if n := len(a.userMDs); n > userMDsKept {
		t.Errorf("%d messages kept, want at most %d", n, userMDsKept)
	}
}

// A message without code shows as plain text, as before: its menu copies
// only what was typed, with no Markdown of it.
func TestAUserMessageWithoutCodeIsPlainText(t *testing.T) {
	const msg = "Use ^\\d+ on C:\\Users\\me\n  indented"
	h := sent(t, msg)
	if err := h.tt.RightClick(msg); err != nil {
		t.Fatal(err)
	}
	if m := h.tt.Menu(); slices.Contains(m, "Copy as Markdown") {
		t.Errorf("menu %q offers the message's Markdown", m)
	}
}

// A fence's opening line that holds more than a language's name is text,
// so none of what was typed on it is hidden as the block's label, and so
// is all up to the fence that closes it, which opens nothing after.
func TestAUserMessagesFenceLineShowsAsTyped(t *testing.T) {
	typed := []string{"```npm install left-pad\nnpm run build\n```", "```R&amp;D\\_notes\nx = 1\n```"}
	h := sent(t, append(typed, "```npm install left-pad\nnpm run build\n```\nPlease explain `it`.")...)
	for _, s := range append(typed, "```npm install left-pad\nnpm run build\n```\nPlease explain it.") {
		if !slices.Contains(h.tt.Texts(), s) {
			t.Errorf("no %q as typed; texts %q", s, h.tt.Texts())
		}
	}
}

// The bubble's padding selects its text and opens its menu, as the text
// does, with or without code.
func TestAUserMessagesPaddingSelects(t *testing.T) {
	for _, c := range []struct{ typed, shown string }{{"short", "short"}, {"short `c`", "short c"}} {
		h := sent(t, c.typed)
		r, _ := h.tt.Find(c.shown)
		send, _ := h.tt.Find("Send")
		x, y := send.X+send.W+8-5, r.Y+r.H/2
		h.tt.RightClickAt(x, y)
		menu := h.tt.Menu()
		h.tt.CloseMenu()
		h.tt.Press(x, y)
		h.tt.Move(x-20, y)
		h.tt.Move(r.X-40, y)
		h.tt.Release(r.X-40, y)
		h.tt.Key(ui.Cmd, ui.KeyC)
		if len(menu) == 0 || h.tt.Clipboard() != c.shown {
			t.Errorf("%q: menu %q and %q copied from the padding", c.typed, menu, h.tt.Clipboard())
		}
	}
}
