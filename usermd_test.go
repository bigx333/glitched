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
