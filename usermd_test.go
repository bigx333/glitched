package main

import (
	"glitched/internal/daemon"
	"glitched/internal/provider/fake"
	"slices"
	"strings"
	"testing"
	"time"
)

// A message the user sent reads as Markdown, as a reply does: a code span
// and a fenced code block show as code, without their backticks.
func TestAUserMessageShowsItsMarkdown(t *testing.T) {
	const msg = "Why does `break` leave only the inner loop?\n\n```go\nfor {\n\tbreak\n}\n```"
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(checks))
	seed(t, d, t.TempDir(), "Loops", msg)
	h := start(t, dir)
	th := h.thread("Loops")
	h.a.select_(th)
	h.until("its transcript", func() bool { return h.tt.HasText("One test fails.") })
	h.tt.Advance(time.Second)
	texts := h.tt.Texts()
	if slices.ContainsFunc(texts, func(s string) bool { return strings.Contains(s, "`") }) {
		t.Errorf("the message shows its backticks; texts %q", texts)
	}
	for _, s := range []string{"Why does break leave only the inner loop?", "break"} {
		if !h.tt.HasText(s) {
			t.Errorf("no %q; texts %q", s, texts)
		}
	}
}

// A message keeps the lines the user typed, which Markdown would join,
// but its code keeps its own.
func TestAUserMessageKeepsItsLines(t *testing.T) {
	const msg = "Steps:\nrun make\nthen run tests\n\n```\nmake\n```\n\n~~~\n```\nx\n~~~\nlast\nline"
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(checks))
	seed(t, d, t.TempDir(), "Steps", msg)
	h := start(t, dir)
	h.a.select_(h.thread("Steps"))
	h.until("its transcript", func() bool { return h.tt.HasText("One test fails.") })
	h.tt.Advance(time.Second)
	for _, s := range []string{"Steps:\nrun make\nthen run tests", "last\nline"} {
		if !h.tt.HasText(s) {
			t.Errorf("no %q; texts %q", s, h.tt.Texts())
		}
	}
	if got, want := userMarkdown(msg), "Steps:  \nrun make  \nthen run tests\n\n```\nmake\n```\n\n~~~\n```\nx\n~~~\nlast  \nline"; got != want {
		t.Errorf("userMarkdown(%q) = %q, want %q", msg, got, want)
	}
}

// Dollars and backslashed brackets in a message are text, not math.
func TestAUserMessageHasNoMath(t *testing.T) {
	const msg = "It costs $5 or $6 and matches \\[a-z\\]+"
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(checks))
	seed(t, d, t.TempDir(), "Math", msg)
	h := start(t, dir)
	h.a.select_(h.thread("Math"))
	h.until("its transcript", func() bool { return h.tt.HasText("One test fails.") })
	h.tt.Advance(time.Second)
	if want := "It costs $5 or $6 and matches [a-z]+"; !h.tt.HasText(want) {
		t.Errorf("no %q; texts %q", want, h.tt.Texts())
	}
}

// A link in a message to a path, as the app's own pages have, goes
// nowhere: the user's text doesn't move the window.
func TestAUserMessagesLinkStaysOffTheAppsPages(t *testing.T) {
	const msg = "See [the settings](/settings)."
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(checks))
	seed(t, d, t.TempDir(), "Links", msg)
	h := start(t, dir)
	h.a.select_(h.thread("Links"))
	h.until("its transcript", func() bool { return h.tt.HasText("One test fails.") })
	h.tt.Advance(time.Second)
	h.click("the settings")
	h.tt.Advance(time.Second)
	if p := h.a.router.Path(); p != "/" {
		t.Errorf("at %q after clicking the message's link", p)
	}
}
