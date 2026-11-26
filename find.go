package main

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"golang.org/x/text/unicode/norm"

	"glitched/internal/api"
	"glitched/internal/client"
)

// openFind opens the find bar on the thread shown, at its first match
// when the thread is another than the bar was last on. It finds the query
// whole.
func (a *app) openFind() {
	a.findOpen, a.findFocus = true, true
	a.findWords, a.findTarget = false, 0
	if id := a.current().id(); a.findThread != id {
		a.findThread, a.findIndex = id, 0
		a.findJump = strings.TrimSpace(a.findQuery) != ""
	}
}

// openFindAt opens the find bar on the thread shown at a search hit: the
// row the log has at seq, among the rows that hold each word of query.
func (a *app) openFindAt(query string, seq uint64) {
	a.findOpen, a.findFocus, a.findJump = true, true, true
	a.findThread, a.findQuery, a.findIndex = a.current().id(), query, 0
	a.findWords, a.findTarget = true, seq
}

func (a *app) closeFind() {
	a.findOpen = false
}

// findKey is what the find bar's matches are of.
type findKey struct {
	thread    api.ID
	query     string
	words     bool
	change    uint64
	cursorSeq uint64
	first     *entry
	n         int
}

// findHit is one match in the current thread: row >= 0 indexes rs.es;
// row == -1 is a waiting or held message in the queue strip.
type findHit struct {
	row int
	seq uint64
}

// findMatches are the rows (and waiting queued messages) of t the find bar
// matches, found once for the frame's timeline and bar. A hit's row, once
// the transcript catches up to its seq, is the one chosen.
func (a *app) findMatches(t thread, rs rows) []findHit {
	if !a.findOpen {
		return nil
	}
	if t.id() != a.findThread {
		if t.id() != a.current().id() {
			return nil
		}
		a.findThread, a.findIndex = t.id(), 0
		a.findWords, a.findTarget = false, 0
		a.findJump = strings.TrimSpace(a.findQuery) != ""
	}
	var cursorSeq uint64
	if t.Transcript != nil {
		cursorSeq = t.Transcript.Cursor.Seq
	}
	k := findKey{thread: t.id(), query: a.findQuery, words: a.findWords, change: rs.change, cursorSeq: cursorSeq, n: len(rs.es)}
	if len(rs.es) > 0 {
		k.first = &rs.es[0]
	}
	if a.findKey != k {
		var hits []findHit
		for _, idx := range findMatches(rs.es, a.findQuery, a.findWords) {
			hits = append(hits, findHit{row: idx, seq: rs.es[idx].seq})
		}
		if t.Transcript != nil {
			if want := findTerms(a.findQuery, a.findWords); len(want) > 0 {
				for _, u := range t.Transcript.Queue {
					if waits(u) && textMatchesTerms(u.Text, want) {
						hits = append(hits, findHit{row: -1, seq: u.Seq})
					}
				}
			}
		}
		a.findKey, a.findRows = k, hits
	}
	ms := a.findRows
	if a.findTarget != 0 && t.Transcript != nil && t.Transcript.Cursor.Seq >= a.findTarget {
		for i, h := range ms {
			if h.seq == a.findTarget {
				a.findIndex, a.findJump = i, true
				break
			}
		}
		a.findTarget = 0
	}
	return ms
}

func findTerms(query string, words bool) []string {
	q := foldForFind(strings.TrimSpace(query))
	if q == "" {
		return nil
	}
	if !words {
		return []string{q}
	}
	toks := strings.FieldsFunc(q, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	if len(toks) == 0 {
		return strings.Fields(q)
	}
	return toks
}

func textMatchesTerms(s string, want []string) bool {
	if len(want) == 0 {
		return false
	}
	text := foldForFind(s)
	for _, w := range want {
		if !strings.Contains(text, w) {
			return false
		}
	}
	return true
}

// findMatches returns the indices of the rows of es that show query, past
// case and accents: query whole, or with words, each FTS5 token of it.
func findMatches(es []entry, query string, words bool) []int {
	want := findTerms(query, words)
	if len(want) == 0 {
		return nil
	}
	var out []int
	for i, e := range es {
		if textMatchesTerms(findText(e), want) {
			out = append(out, i)
		}
	}
	return out
}

// findText is the words a row shows, a line each.
func findText(e entry) string {
	parts := []string{e.text, e.note}
	step := func(s client.WorkStep) {
		parts = append(parts, s.Title, s.Path, s.Output)
	}
	for _, w := range e.work {
		step(w)
		if w.Subagent != nil {
			for _, p := range w.Subagent.Parts {
				if p.Step != nil {
					step(*p.Step)
				} else {
					parts = append(parts, p.Text)
				}
			}
		}
	}
	for _, s := range e.plan {
		parts = append(parts, s.Text)
	}
	if e.request != nil {
		parts = append(parts, e.request.Kind, string(e.request.Body))
	}
	for _, r := range e.reports {
		parts = append(parts, r.Title, r.Outcome)
	}
	if e.fork != nil {
		parts = append(parts, e.fork.Title)
	}
	return strings.Join(parts, "\n")
}

// foldForFind lowers s and drops its accents, as search's unicode61
// tokenizer does: "Café" finds as "cafe".
func foldForFind(s string) string {
	ascii := true
	for i := 0; i < len(s) && ascii; i++ {
		ascii = s[i] < utf8.RuneSelf
	}
	if ascii {
		return strings.ToLower(s)
	}
	var b strings.Builder
	for _, r := range norm.NFD.String(s) {
		if !unicode.Is(unicode.Mn, r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

func (a *app) findBar(c *ui.Context, t thread, rs rows) {
	if !a.findOpen {
		return
	}
	if c.Shortcut(0, ui.KeyEscape) {
		a.closeFind()
		return
	}
	matches := a.findMatches(t, rs)
	a.findIndex = max(0, min(a.findIndex, len(matches)-1))
	v := a.viewFor(t.id(), rs)
	show := func() {
		if len(matches) > 0 {
			if h := matches[a.findIndex]; h.row >= 0 {
				a.listFor(t.id()).ScrollIntoView(v.pos(h.row))
			} else if v.len() > 0 {
				a.listFor(t.id()).ScrollIntoView(v.len() - 1)
			}
		}
	}
	step := func(delta int) {
		if len(matches) > 0 {
			a.findIndex = (a.findIndex + delta + len(matches)) % len(matches)
			show()
		}
	}
	if c.Shortcut(ui.Cmd, ui.KeyG) {
		step(1)
	}
	if c.Shortcut(ui.Cmd|ui.Shift, ui.KeyG) {
		step(-1)
	}

	bar := a.cover(coverKey{kind: "find"}, a.frosted(c, ui.Row(c).Key("find-bar").Absolute().Top(12).Right(20).
		Padding(6, 8, 6, 12).Gap(6).Radius(12).AlignItems(ui.Center)).
		Shadow(0, 8, 24, 0, ui.RGBA(0, 0, 0, 0.22)))
	bar.Children(func() {
		ui.Icon(c, icSearch).FontSize(13).TextColor(palette.muted)
		in := ui.TextInput(c.Key("find-input"), &a.findQuery).Width(200).FontSize(12.5).
			Background(ui.Transparent).Border(0, ui.Transparent).FocusRing(false).Padding(2, 0).
			Placeholder("Find in thread…").Label("Find in thread")
		if a.findFocus {
			in.Focus()
			a.findFocus = false
		}
		switch {
		case in.Changed():
			a.findWords, a.findTarget, a.findIndex = false, 0, 0
			matches = a.findMatches(t, rs)
			show()
		case a.findJump && len(matches) > 0:
			a.findJump = false
			show()
		}
		// The field takes Enter, ⇧ and all: the keys held say which.
		if in.Submitted() {
			if c.Modifiers()&ui.Shift != 0 {
				step(-1)
			} else {
				step(1)
			}
		}

		label := ""
		switch {
		case strings.TrimSpace(a.findQuery) == "":
		case len(matches) == 0:
			label = "0 matches"
		default:
			label = fmt.Sprintf("%d of %d", a.findIndex+1, len(matches))
		}
		if label != "" {
			ui.Text(c, label).FontSize(11.5).TextColor(palette.muted).Padding(0, 4)
		}
		if a.iconButton(c, "find-prev", icChevronUp, "Previous match  ⇧⌘G", false).Padding(4).Disabled(len(matches) == 0).Clicked() {
			step(-1)
		}
		if a.iconButton(c, "find-next", icChevronD, "Next match  ⌘G", false).Padding(4).Disabled(len(matches) == 0).Clicked() {
			step(1)
		}
		if a.iconButton(c, "find-close", icX, "Close find  Esc", false).Padding(4).Clicked() {
			a.closeFind()
		}
	})
}
