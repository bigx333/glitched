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
	}
	h := sent(t, typed...)
	texts := h.tt.Texts()
	for _, s := range typed {
		if !slices.Contains(texts, s) {
			t.Errorf("no %q as typed; texts %q", s, texts)
		}
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
