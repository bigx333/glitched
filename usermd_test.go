package main

import (
	"fmt"
	"glitched/internal/daemon"
	"glitched/internal/provider/fake"
	"slices"
	"strings"
	"testing"
	"time"
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

// Outside its code, a message shows exactly as the user typed it: its
// lines, blank lines and indents, and every mark Markdown would read.
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
	}
	h := sent(t, typed...)
	texts := h.tt.Texts()
	for _, s := range typed {
		if !slices.Contains(texts, s) {
			t.Errorf("no %q as typed; texts %q", s, texts)
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
	h := sent(t, "```\nx\n```\u00a0\nthen *bold*")
	if want := "x\n```\u00a0\nthen *bold*"; !slices.Contains(h.tt.Texts(), want) {
		t.Errorf("no %q; texts %q", want, h.tt.Texts())
	}
}

// Blank lines between a message's text and its code show as typed.
func TestAUserMessageKeepsBlankLinesAroundCode(t *testing.T) {
	h := sent(t, "tight\n```\ncode tight\n```", "loose\n\n\n\n```\ncode loose\n```")
	gap := func(text, code string) float32 {
		a, _ := h.tt.Find(text)
		b, _ := h.tt.Find(code)
		return b.Y - a.Y
	}
	if tight, loose := gap("tight", "code tight"), gap("loose", "code loose"); loose <= tight {
		t.Errorf("the code is %v below three blank lines, %v below none", loose, tight)
	}
}

// Line endings of other systems end lines as line feeds do.
func TestAUserMessageReadsAnyLineEnding(t *testing.T) {
	h := sent(t, "crlf one\r\ncrlf two", "cr one\rcr two")
	for _, s := range []string{"crlf one\ncrlf two", "cr one\ncr two"} {
		if !h.tt.HasText(s) {
			t.Errorf("no %q; texts %q", s, h.tt.Texts())
		}
	}
}

// Nothing a message holds moves the window to the app's own pages.
func TestAUserMessageStaysOffTheAppsPages(t *testing.T) {
	const msg = "See [the settings](/settings)."
	h := sent(t, msg)
	h.click(msg)
	h.tt.Advance(time.Second)
	if p := h.a.router.Path(); p != "/" {
		t.Errorf("at %q after clicking the message", p)
	}
}

// An address in a message opens nothing.
func TestAUserMessagesAddressOpensNothing(t *testing.T) {
	const msg = "Ping ops@example.com about it"
	h := sent(t, msg)
	if r, ok := h.tt.Find("ops@example.com"); ok {
		h.tt.ClickAt(r.X+r.W/2, r.Y+r.H/2)
	}
	h.click(msg)
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
