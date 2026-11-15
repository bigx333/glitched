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
	for _, s := range []string{"Why does break leave only the inner loop?", "for {\n\tbreak\n}"} {
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
	if !h.tt.HasText("```\nx") {
		t.Errorf("the tilde fence's code isn't as typed; texts %q", h.tt.Texts())
	}
}

// Only a paragraph's line breaks are made hard: code, wherever it is,
// keeps its lines as typed, as the parser reads it.
func TestUserMarkdown(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"a\nb", "a  \nb"},
		{"a\r\nb", "a  \nb"},
		{"a\n\nb", "a\n\nb"},
		// A code span of three backticks opens no fence.
		{"```npm i``` first\nthen b", "```npm i``` first  \nthen b"},
		// A fence in a list item closes with the item.
		{"- a\n  ```\n  x\nb\nc", "- a\n  ```\n  x\nb  \nc"},
		// Fences in quotes, indented code and closing fences longer than
		// their openers.
		{"> ```\n> x\n> y\n> ```", "> ```\n> x\n> y\n> ```"},
		{"    x\n    y", "    x\n    y"},
		{"````\nx\ny\n`````\nz\nw", "````\nx\ny\n`````\nz  \nw"},
		{"> a\n> b", "> a  \n> b"},
		// Nothing but a link's definition shows as nothing; beside other
		// text, a definition's line shows as typed.
		{"[WARN]: disk", "\\[WARN]: disk"},
		{"Got this:\n\n[ERROR]: timeout\n\nwhat now?", "Got this:\n\n\\[ERROR]: timeout\n\nwhat now?"},
		{"[ERROR]: timeout\nwhat now?", "\\[ERROR]: timeout  \nwhat now?"},
		{"> [1]: https://a", "> \\[1]: https://a"},
		{"- [1]: https://a", "- \\[1]: https://a"},
		{"[a] and [b]: c\nd", "[a] and [b]: c  \nd"},
		// A code span's line break is a space, as typed.
		{"x `a\nb` y\nz", "x `a\nb` y  \nz"},
		// Lone carriage returns end lines as line feeds do.
		{"a\rb", "a  \nb"},
		// Only a block's marks, which show as nothing, show as typed.
		{"#", ""}, {">", ""}, {"-", ""}, {"* * *\n", ""},
	} {
		if got := userMarkdown(c.in); got != c.want {
			t.Errorf("userMarkdown(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// A line of a message that Markdown reads as a link's definition, and
// shows as nothing, shows as typed.
func TestAUserMessageOfALinksDefinitionShows(t *testing.T) {
	const msg = "Got this:\n\n[ERROR]: timeout\n\nwhat now?"
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(checks))
	seed(t, d, t.TempDir(), "Warn", msg)
	h := start(t, dir)
	h.a.select_(h.thread("Warn"))
	h.until("its transcript", func() bool { return h.tt.HasText("One test fails.") })
	h.tt.Advance(time.Second)
	if !h.tt.HasText("[ERROR]: timeout") {
		t.Errorf("no definition's line; texts %q", h.tt.Texts())
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

// Backslashed brackets in a message are text, not math.
func TestAUserMessageHasNoMath(t *testing.T) {
	const msg = "It matches \\[a-z\\]+"
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(checks))
	seed(t, d, t.TempDir(), "Math", msg)
	h := start(t, dir)
	h.a.select_(h.thread("Math"))
	h.until("its transcript", func() bool { return h.tt.HasText("One test fails.") })
	h.tt.Advance(time.Second)
	if want := "It matches [a-z]+"; !h.tt.HasText(want) {
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
