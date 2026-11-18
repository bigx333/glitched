package main

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
)

// codeBlock is the draft once it holds code blocks, as T3 Code's composer
// does: ``` and Enter, ending a line of its own, make one there, an inset
// box headed by its language, Plain text unless the ``` named one. The
// draft is then pieces, text and code by turns, each in a text area of its
// own with an undo of its own, and goes to the agent as one message, the
// code fenced and tagged with its language. Backspace in an empty block
// takes it away, the text around it coming back together.
type codeBlock struct {
	on   bool
	segs []*segment // text, code, text, …: text first and last
}

// segment is a piece of the draft: text, or a block's code and its tag (""
// is Plain text). id keys its text area: a piece made anew, as a block
// opens or goes, is a text area anew. open is whether text left empty
// shows, as the caret went to it.
type segment struct {
	code bool
	tag  string
	text string
	id   int
	open bool
}

// paneState is what the composer keeps of one of the draft's text areas from
// frame to frame: its caret and text as last laid out, whether a key came
// to it since, which may have moved the caret, and how it laid out its
// lines. The draft without blocks is pane 0.
type paneState struct {
	caret int
	text  string
	keyed bool
	lines ui.TextLines
}

// codeFocus is where the focus goes in the next frame: a pane, and the
// caret there, a rune offset, or -1 for the end.
type codeFocus struct {
	set       bool
	id, caret int
	quiet     bool // the draft isn't scrolled to the caret
}

// codeCarry is a text area the focus left, and the text it held, and the
// string it's bound to: what's typed into it before the next frame goes
// where the focus went. A text area gone from that frame is built once
// more out of sight for it.
type codeCarry struct {
	set   bool
	id    int
	text  string
	bind  *string
	backs int // Backspaces that came after it, before any typing
}

// staleKey takes a key that came to the text area the focus left before
// the next frame: Enter and typing go in it, as text to carry; a
// Backspace before any is counted, to go where the focus went; other keys
// that would edit it or move its caret go.
func (a *app) staleKey(k ui.TextKey) bool {
	switch k.Key {
	case ui.KeyEnter, ui.KeyTab:
		return false
	case ui.KeyBackspace:
		if k.Mods == 0 && k.Text() != a.codeCarry.text {
			return false // takes back what was typed
		}
		if k.Mods == 0 {
			a.codeCarry.backs++
		}
		return true
	case ui.KeyDelete, ui.KeyUp, ui.KeyDown, ui.KeyLeft, ui.KeyRight, ui.KeyHome, ui.KeyEnd, ui.KeyPageUp, ui.KeyPageDown:
		return true
	}
	if k.Mods&(ui.Cmd|ui.Ctrl) != 0 && (k.Key == ui.KeyZ || k.Key == ui.KeyY || k.Key == ui.KeyX || k.Key == ui.KeyA) {
		return true
	}
	return false
}

// backspace is a Backspace in pane id, its caret at caret, or -1 for its
// end, as it comes between frames: in an empty block, it takes the block
// away; in empty text, it goes back into the code; otherwise it takes
// the letter before the caret. It reports whether it did anything.
func (a *app) backspace(id, caret int) bool {
	text := &a.draft
	i := a.seg(id)
	if id != 0 {
		if i < 0 {
			return false
		}
		s := a.code.segs[i]
		text = &s.text
		switch {
		case s.text == "" && s.code:
			a.closeAt(i)
			return true
		case s.text == "":
			s.open = false
			if i > 0 {
				a.focusPane(a.code.segs[i-1].id, -1)
			} else {
				a.focusPane(a.code.segs[1].id, 0)
			}
			return true
		}
	}
	r := []rune(*text)
	if caret < 0 || caret > len(r) {
		caret = len(r)
	}
	if caret == 0 {
		return false
	}
	*text = string(r[:caret-1]) + string(r[caret:])
	a.focusPane(id, caret-1)
	return true
}

// maxDraftHeight is how high the composer's draft grows, holding blocks,
// before it scrolls: as high as ten lines of a draft without.
const maxDraftHeight = 240

func (a *app) paneOf(id int) *paneState {
	if a.panes == nil {
		a.panes = map[int]*paneState{}
	}
	p := a.panes[id]
	if p == nil {
		p = &paneState{}
		a.panes[id] = p
	}
	return p
}

// newSeg is a piece of the draft, with a text area of its own, which
// holds text from the start: no line broken at its end was typed.
func (a *app) newSeg(code bool, tag, text string) *segment {
	a.codeSeq++
	a.paneOf(a.codeSeq).text = text
	return &segment{code: code, tag: tag, text: text, id: a.codeSeq}
}

// fenceOpenBut reports whether a piece of text other than pane id's has a
// fence typed in it half open: Enter doesn't send it, as from a draft.
func (a *app) fenceOpenBut(id int) bool {
	return slices.ContainsFunc(a.code.segs, func(s *segment) bool { return !s.code && s.id != id && unclosedFence(s.text) })
}

// seg is where the piece with text area id is in the draft, or -1.
func (a *app) seg(id int) int {
	return slices.IndexFunc(a.code.segs, func(s *segment) bool { return s.id == id })
}

// focusPane sends the focus to pane id in the next frame, its caret at
// caret.
func (a *app) focusPane(id, caret int) { a.codeFocus = codeFocus{set: true, id: id, caret: caret} }

// moved notes that the focus leaves pane id, which holds text: what's
// typed into it before the next frame follows the focus. It's noted
// before the pane's piece goes from the draft.
func (a *app) moved(id int, text string) {
	bind := &a.draft
	if id != 0 {
		bind = &a.code.segs[a.seg(id)].text
	}
	a.codeCarry = codeCarry{set: true, id: id, text: text, bind: bind}
}

// focusBack sends the focus back to the pane that had it last, as the
// language menu takes it, where its caret was.
func (a *app) focusBack() {
	id := a.codeLast
	if i := a.seg(id); a.code.on && (i < 0 || !a.code.segs[i].shown()) {
		id = a.code.segs[1].id
	} else if !a.code.on {
		id = 0
	}
	a.focusPane(id, a.paneOf(id).caret)
	// Where the language menu was, the draft stays scrolled.
	a.codeFocus.quiet = true
}

// shown reports whether the piece shows: code, or text that holds some or
// that the caret went to.
func (s *segment) shown() bool { return s.code || s.text != "" || s.open }

// runes is how many runes s holds.
func runes(s string) int { return utf8.RuneCountInString(s) }

// paneCaret is where pane id's caret is as a key comes to it with text:
// where it was as last laid out, or after what was typed at it since. ok
// is false when that isn't known, as after another key since, which may
// have moved it.
func (a *app) paneCaret(id int, text string) (caret int, ok bool) {
	p := a.paneOf(id)
	if p.keyed {
		return 0, false
	}
	if text == p.text {
		return p.caret, true
	}
	// Typed at the caret: the text before it and after it are still there.
	r := []rune(p.text)
	at := min(max(p.caret, 0), len(r))
	head, tail := string(r[:at]), string(r[at:])
	if len(text) > len(p.text) && strings.HasPrefix(text, head) && strings.HasSuffix(text, tail) {
		return runes(text) - runes(tail), true
	}
	return 0, false
}

// atEdge reports whether pane id's caret, as a key comes to it with text,
// is on the text area's top row, for up, or its bottom row, as the area
// lays out its lines, wrapped: there, Up or Down leaves the pane. A caret
// where a wrapped line breaks, which shows at the end of one row or the
// start of the next, is on neither: the text area moves it, and the next
// key leaves.
func (a *app) atEdge(id int, text string, up bool) bool {
	caret, ok := a.paneCaret(id, text)
	if !ok {
		return false
	}
	line, col := lineCol(text, caret)
	rows := a.paneOf(id).lines.Rows(line)
	if up {
		return line == 0 && (len(rows) < 2 || col < rows[1])
	}
	return line == strings.Count(text, "\n") && (len(rows) < 2 || col > rows[len(rows)-1])
}

// lineCol is the line caret, a rune offset into text, is on, and how far
// into it, in runes.
func lineCol(text string, caret int) (line, col int) {
	r := []rune(text)
	head := string(r[:min(max(caret, 0), len(r))])
	return strings.Count(head, "\n"), runes(head[strings.LastIndexByte(head, '\n')+1:])
}

// openAt opens a block where caret, a rune offset into text, ends a
// fence's opening in it, the text of pane id: the draft, or a piece of
// text, which the text over the fence, the block and the text under it
// take the place of. held is what pane id's text area holds.
func (a *app) openAt(id int, text string, caret int, held string) bool {
	before, tag, after, ok := fenceLine(text, caret)
	if !ok {
		return false
	}
	if strings.TrimSpace(before) == "" {
		before = ""
	}
	if strings.TrimSpace(after) == "" {
		after = ""
	}
	a.moved(id, held)
	over, code, under := a.newSeg(false, "", before), a.newSeg(true, tag, ""), a.newSeg(false, "", after)
	if id == 0 {
		a.code = codeBlock{on: true, segs: []*segment{over, code, under}}
		a.draft = ""
	} else {
		i := a.seg(id)
		a.code.segs = slices.Replace(a.code.segs, i, i+1, over, code, under)
	}
	a.focusPane(code.id, 0)
	return true
}

// closeAt takes away the empty block at i, the text over it and under it
// coming together, the caret between; the last block gone, the draft is
// that text.
func (a *app) closeAt(i int) {
	segs := a.code.segs
	before, after := strings.TrimRight(segs[i-1].text, "\n"), strings.TrimLeft(segs[i+1].text, "\n")
	text := before
	if before != "" && after != "" {
		text += "\n"
	}
	text += after
	a.moved(segs[i].id, "")
	if len(segs) == 3 {
		// The draft holds the text as it comes back, rather than as typed:
		// a fence and a line break at its end open no block.
		a.code, a.draft, a.fenceSaw = codeBlock{}, text, text
		a.focusPane(0, runes(before))
		return
	}
	m := a.newSeg(false, "", text)
	m.open = true
	a.code.segs = slices.Replace(segs, i-1, i+2, m)
	a.focusPane(m.id, runes(before))
}

// arrowOut sends the focus from the piece at i, which holds text, to the
// one over it, or under it, showing it: false if there's none. Text left
// empty goes.
func (a *app) arrowOut(i int, text string, up bool) bool {
	j, caret := i+1, 0
	if up {
		j, caret = i-1, -1
	}
	if j < 0 || j >= len(a.code.segs) {
		return false
	}
	a.moved(a.code.segs[i].id, text)
	if s := a.code.segs[i]; !s.code && strings.TrimSpace(text) == "" {
		s.text, s.open = "", false
	}
	for k, s := range a.code.segs {
		// Blank text a click left goes too: a key moves no pointer.
		if k != i && k != j && !s.code && strings.TrimSpace(s.text) == "" {
			s.text, s.open = "", false
		}
	}
	a.code.segs[j].open = true
	a.focusPane(a.code.segs[j].id, caret)
	return true
}

// typedText is what the draft holds as typed: its text, or, as it holds
// blocks, every piece's.
func (a *app) typedText() string {
	if !a.code.on {
		return a.draft
	}
	var b strings.Builder
	for _, s := range a.code.segs {
		b.WriteString(s.text)
		b.WriteByte(0)
	}
	return b.String()
}

// codeMessage is the draft as it goes, its blocks fenced.
func (a *app) codeMessage() string {
	var parts []string
	for i, s := range a.code.segs {
		p := s.text
		switch {
		case s.code:
			p = fenced(s.tag, p)
		case i == 0:
			p = strings.TrimRight(p, " \t\n")
		default:
			p = strings.TrimSpace(p)
		}
		if strings.TrimSpace(p) != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, "\n")
}

// codeKey takes the draft's Enter, as it has no blocks, before it sends:
// ``` and Enter, or Shift+Enter, open a block there, and Enter in a fence
// typed by hand breaks the line. It reports whether the key is the
// block's, and whether it takes it from the text area.
func (a *app) codeKey(k ui.TextKey) (own, take bool) {
	if a.code.on || k.Key != ui.KeyEnter || k.Mods&^ui.Shift != 0 {
		return false, false
	}
	text := k.Text()
	caret, ok := a.paneCaret(0, text)
	if !ok {
		caret = runes(text)
	}
	if a.openAt(0, text, caret, text) {
		return true, true
	}
	if halfFence(text, caret) {
		return true, false
	}
	return false, false
}

// halfFence reports whether Enter, with the caret at caret in text, would
// send a fence typed by hand half open, or at the end of a fence's opening
// that didn't open a block: it breaks the line instead.
func halfFence(text string, caret int) bool {
	return unclosedFence(text) || unclosedFence(throughLine(text, caret)) && endsFenceLine(text, caret)
}

// segKey takes the keys of a piece of the draft holding blocks. In code,
// Enter breaks the line, and a fence typed at its end, and Enter, closes
// the block; Backspace in it empty takes it away. In text, Enter sends,
// as from a draft, and ``` and Enter, or Shift+Enter, open a block there.
// Up and Down from the top or bottom row go to the piece over or under.
// Backspace in empty text over or under a block takes it away.
func (a *app) segKey(t thread, id int) func(ui.TextKey) bool {
	return func(k ui.TextKey) bool {
		i := a.seg(id)
		if i < 0 {
			return false
		}
		s, text := a.code.segs[i], k.Text()
		caret, known := a.paneCaret(id, text)
		at := caret
		if !known {
			at = runes(text)
		}
		enter := k.Key == ui.KeyEnter && k.Mods&^ui.Shift == 0
		switch {
		case enter && s.code && known && caret == runes(text):
			code, ok := closedCode(text)
			if !ok {
				return false
			}
			s.text = code
			k.SetText(code)
			a.arrowOut(i, code, false)
		case enter && !s.code && a.openAt(id, text, at, text):
		case k.Key == ui.KeyEnter && k.Mods == 0 && !s.code:
			if halfFence(text, at) || a.fenceOpenBut(id) {
				return false
			}
			// The text area, gone as it sends, is bound to the text as it
			// was: what's typed after the Enter goes on into the draft.
			a.moved(id, text)
			was := s.text
			s.text = text
			if a.send(t); !a.code.on {
				s.text = was
			}
		case k.Key == ui.KeyBackspace && k.Mods == 0 && text == "" && s.code:
			a.closeAt(i)
		case k.Key == ui.KeyBackspace && k.Mods == 0 && text == "":
			a.moved(id, text)
			s.open = false
			if i > 0 {
				a.focusPane(a.code.segs[i-1].id, -1)
			} else {
				a.focusPane(a.code.segs[1].id, 0)
			}
		case (k.Key == ui.KeyUp || k.Key == ui.KeyDown) && k.Mods == 0 && a.atEdge(id, text, k.Key == ui.KeyUp):
			return a.arrowOut(i, text, k.Key == ui.KeyUp)
		default:
			return false
		}
		return true
	}
}

// typedFence opens a block on a draft that is only a fence's opening
// and the line Enter broke after it, as the text area breaks it when the
// Enter never came to draftKey: an input method's, as Windows's text
// suggestions take the keys typed, or one the system sent as text.
func (a *app) typedFence() {
	was := a.fenceSaw
	a.fenceSaw = a.draft
	// Only as the line breaks at the end: not on a draft that came to end
	// so by what was taken from it.
	if a.code.on || !grewLine(was, a.draft) {
		return
	}
	if text := strings.TrimSuffix(a.draft, "\n"); a.openAt(0, text, runes(text), a.draft) {
		a.fenceSaw = ""
	}
}

// grewLine reports whether now is was with a line broken at its end.
func grewLine(was, now string) bool {
	return strings.HasSuffix(now, "\n") && len(now) > len(was) && strings.HasPrefix(now, was)
}

// typedInSeg is typedFence for a piece of a draft holding blocks, whose
// text grew from was to now: a fence and a line break at the end of text
// open a block there, and at the end of code close it.
func (a *app) typedInSeg(id int, was, now string) {
	i := a.seg(id)
	if i < 0 || !grewLine(was, now) {
		return
	}
	s, text := a.code.segs[i], strings.TrimSuffix(now, "\n")
	if !s.code {
		a.openAt(id, text, runes(text), now)
		return
	}
	if code, ok := closedCode(text); ok {
		s.text = code
		a.arrowOut(i, code, false)
	}
}

// longCodePaste is the longest paste a code block takes in: past it, the
// text area, which lays out all it holds as it's edited, would lag.
const longCodePaste = 64 << 10

// codeLanguage is a language the block's picker offers: its name, and the
// tag its fence takes.
type codeLanguage struct{ name, tag string }

// codeLanguages are what the picker offers, Plain text first.
var codeLanguages = []codeLanguage{
	{"Plain text", ""},
	{"Go", "go"},
	{"TypeScript", "typescript"},
	{"JavaScript", "javascript"},
	{"Python", "python"},
	{"Rust", "rust"},
	{"Swift", "swift"},
	{"Shell", "bash"},
	{"JSON", "json"},
	{"YAML", "yaml"},
	{"HTML", "html"},
	{"CSS", "css"},
	{"SQL", "sql"},
	{"Markdown", "markdown"},
	{"Diff", "diff"},
}

// codeAliases are the other tags a fence may be typed with, as the tag the
// picker knows them by.
var codeAliases = map[string]string{
	"text": "", "plaintext": "", "txt": "",
	"golang": "go", "ts": "typescript", "js": "javascript", "py": "python",
	"rs": "rust", "sh": "bash", "shell": "bash", "zsh": "bash",
	"yml": "yaml", "md": "markdown", "patch": "diff",
}

// codeLanguageName is what the picker calls tag: a tag it doesn't know,
// as typed after the ```, is its own name.
func codeLanguageName(tag string) string {
	for _, l := range codeLanguages {
		if l.tag == tag {
			return l.name
		}
	}
	return tag
}

// fenceOpener matches a draft that is only a fence's opening: ``` and,
// maybe, its language.
var fenceOpener = regexp.MustCompile("^\\s*````*[ \\t]*([A-Za-z0-9_+#.-]*)\\s*$")

// fenceTag reports whether draft is only a fence's opening, and the tag
// it names, as the picker knows it.
func fenceTag(draft string) (tag string, ok bool) {
	m := fenceOpener.FindStringSubmatch(draft)
	if m == nil {
		return "", false
	}
	tag = strings.ToLower(m[1])
	if t, ok := codeAliases[tag]; ok {
		tag = t
	}
	return tag, true
}

// fenceLine reports whether caret, a rune offset into text, ends a line
// that is a fence's opening, out of any fence typed over it and with none
// closing it under it: the text over it and under it, their line breaks
// trimmed, and the tag it names.
func fenceLine(text string, caret int) (before, tag, after string, ok bool) {
	r := []rune(text)
	caret = min(max(caret, 0), len(r))
	head, tail := string(r[:caret]), string(r[caret:])
	start := strings.LastIndexByte(head, '\n') + 1
	end, rest := len(tail), ""
	if i := strings.IndexByte(tail, '\n'); i >= 0 {
		end, rest = i, tail[i+1:]
	}
	if strings.TrimSpace(tail[:end]) != "" {
		return "", "", "", false
	}
	line, above := head[start:]+tail[:end], head[:max(start-1, 0)]
	if tag, ok = fenceTag(line); !ok || unclosedFence(above) || !unclosedFence(line+"\n"+rest) {
		return "", "", "", false
	}
	return strings.TrimRight(above, "\n"), tag, strings.TrimLeft(rest, "\n"), true
}

// unclosedFence reports whether draft has a fence it opens but doesn't
// close, as typed by hand after other text: Enter in it breaks the line
// rather than send half a block. As CommonMark has it, a line of backticks
// with more after them is a fence only if none of that is a backtick, and
// only a bare fence at least as long as the one it opened closes it.
func unclosedFence(draft string) bool {
	open := 0 // the open fence's length, or 0
	for l := range strings.SplitSeq(draft, "\n") {
		l = strings.TrimSpace(l)
		n := len(l) - len(strings.TrimLeft(l, "`"))
		switch {
		case n < 3 || strings.Contains(l[n:], "`"):
		case open == 0:
			open = n
		case n >= open && l[n:] == "":
			open = 0
		}
	}
	return open > 0
}

// fenced is code as a fenced block tagged tag, the lines around it trimmed
// but not the indent of its first; a fence in the code takes a longer one.
func fenced(tag, code string) string {
	code = strings.TrimRight(strings.TrimLeft(code, "\n"), " \t\n")
	if strings.TrimSpace(code) == "" {
		return ""
	}
	fence := "```"
	for strings.Contains(code, fence) {
		fence += "`"
	}
	return fence + tag + "\n" + code + "\n" + fence
}

// closedCode reports whether text, the code, ends in a fence that closes the
// block, rather than one in the code, as Markdown's: the code without it.
func closedCode(text string) (string, bool) {
	i := strings.LastIndexByte(text, '\n')
	if l := strings.TrimSpace(text[i+1:]); len(l) < 3 || strings.Trim(l, "`") != "" {
		return "", false
	}
	code := ""
	if i >= 0 {
		code = text[:i]
	}
	return code, !unclosedFence(code)
}

// endsFenceLine reports whether caret, a rune offset into text, ends a line
// that is a fence's opening.
func endsFenceLine(text string, caret int) bool {
	head := throughLine(text, caret)
	r := []rune(text)
	tail := head[len(string(r[:min(max(caret, 0), len(r))])):]
	_, ok := fenceTag(head[strings.LastIndexByte(head, '\n')+1:])
	return ok && strings.TrimSpace(tail) == ""
}

// throughLine is text up to the end of the line caret, a rune offset into
// it, is on.
func throughLine(text string, caret int) string {
	r := []rune(text)
	head := string(r[:min(max(caret, 0), len(r))])
	if i := strings.IndexByte(text[len(head):], '\n'); i >= 0 {
		return text[:len(head)+i]
	}
	return text
}

// inserted reports whether now is was with text put in it, and that text.
func inserted(was, now string) (string, bool) {
	n := 0
	for n < len(was) && n < len(now) && was[n] == now[n] {
		n++
	}
	for n > 0 && n < len(was) && !utf8.RuneStart(was[n]) {
		n--
	}
	if len(now) <= len(was) || !strings.HasSuffix(now, was[n:]) {
		return "", false
	}
	return now[n : len(now)-len(was)+n], true
}

// draftAreas are the draft's text areas: the one text area, or, once it
// holds blocks, a text area for each piece, the blocks inset in the card
// and headed by their language pickers. The pieces grow with what they
// hold, none scrolling on its own: they scroll together, the composer
// following the caret, so that no text goes under a block.
func (a *app) draftAreas(c *ui.Context, t thread, key, placeholder string, ahead, large bool, in float32) {
	// keyed has a pane's keys note that one came since the last frame: its
	// caret may have moved.
	keyed := func(id int, fn func(ui.TextKey) bool) func(ui.TextKey) bool {
		return func(k ui.TextKey) bool {
			defer func() { a.paneOf(id).keyed = true }()
			if c := a.codeCarry; c.set && c.id == id {
				// The focus left the pane as an earlier key came: what's
				// typed goes in its text, and after it where the focus
				// went, as does a Backspace before any of it. Other keys
				// that would change it, or move in it, go.
				return a.staleKey(k)
			}
			return fn(k)
		}
	}
	// pasted has a pane's pastes, as they come after the focus left it,
	// taken as pastes where the focus went, where they go on to.
	pasted := func(id int, fn func(string) bool) func(string) bool {
		return func(text string) bool {
			if c, f := a.codeCarry, a.codeFocus; c.set && c.id == id && f.set {
				if i := a.seg(f.id); f.id != 0 && i >= 0 {
					return a.onPasteIn(a.code.segs[i])(text)
				} else if f.id == 0 && !a.code.on {
					return a.onPaste(text)
				}
			}
			return fn(text)
		}
	}
	live := func(id int) bool {
		if !a.code.on {
			return id == 0
		}
		i := a.seg(id)
		return i >= 0 && a.code.segs[i].shown()
	}
	areaKey := func(id int) string {
		if id == 0 {
			return key
		}
		return fmt.Sprintf("%s-%d", key, id)
	}
	carry := a.codeCarry
	a.codeCarry = codeCarry{}
	if ahead {
		carry.set = false
	}
	if carry.set {
		// Backspaces that came after the focus left go where it went.
		for range carry.backs {
			if f := a.codeFocus; !f.set || !live(f.id) || !a.backspace(f.id, f.caret) {
				break
			}
		}
	}
	// The text area the focus left, still there, built as ever.
	stays := carry.set && live(carry.id)
	// carried puts what was typed into the text area the focus left,
	// holding now, where the focus went, and reports whether there was
	// any.
	dest := a.codeFocus
	carried := func(now string) bool {
		ins, ok := inserted(carry.text, now)
		if !ok || ins == "" || !dest.set || dest.id == carry.id || !live(dest.id) {
			return false
		}
		text := &a.draft
		if dest.id != 0 {
			text = &a.code.segs[a.seg(dest.id)].text
		}
		r := []rune(*text)
		at := dest.caret
		if at < 0 || at > len(r) {
			at = len(r)
		}
		*text = string(r[:at]) + ins + string(r[at:])
		a.focusPane(dest.id, at+runes(ins))
		return true
	}
	// took builds the text area the focus left, gone from this frame,
	// once more out of sight, bound as it was, for what was typed into it
	// since.
	took := func() {
		ui.TextArea(c.Key(areaKey(carry.id)), carry.bind).Size(0, 0).MinHeight(0).Padding(0).Border(0, ui.Transparent).Absolute().Opacity(0)
		carried(*carry.bind)
		// The piece left is out of sight, empty, if it's still there.
		*carry.bind = ""
	}
	if f := a.codeFocus; f.set && !live(f.id) {
		// For a piece that went before it showed.
		a.codeFocus = codeFocus{}
	}
	for id := range a.panes {
		if !live(id) {
			delete(a.panes, id)
		}
	}
	// shown keeps what pane id's text area holds, and where its caret is,
	// for its keys in the next frame, giving it the focus if it's to have
	// it; it reports whether it has the focus with its caret moved, or
	// its text changed, since the last frame.
	shown := func(area ui.Element, id int, text string) bool {
		p := a.paneOf(id)
		if ahead {
			p.keyed = true
			return false
		}
		focused := false
		if f := a.codeFocus; f.set && f.id == id {
			n := f.caret
			if n < 0 {
				n = runes(text)
			}
			area.Focus().SetTextSelection(n, n)
			focused = !f.quiet
			a.codeFocus = codeFocus{}
		}
		_, caret := area.TextAnchorCaret()
		moved := focused || caret != p.caret || text != p.text
		p.caret, p.text, p.keyed = caret, text, false
		area.TrackLines(&p.lines)
		if !area.Focused() {
			return false
		}
		a.codeLast = id
		return moved
	}
	outer := ui.Column(c).Key("code-block")
	if large {
		outer.Grow(1).MinHeight(0)
	}
	// A click in the card's room under the pieces, as in the new thread's
	// larger card, is the draft's.
	roomClicked := a.code.on && !ahead && outer.Clicked()
	outer.Children(func() {
		if !a.code.on {
			if carry.set && !stays {
				ui.Scroll(c).Key("code-scroll").Absolute().Opacity(0).Children(func() {
					ui.Column(c).Key(fmt.Sprintf("pane-%d", carry.id)).Children(func() {
						ui.Column(c).Key("area").Children(took)
					})
				})
			}
			box := ui.Column(c).Key("code-box")
			if large {
				box.Grow(1)
			}
			box.Children(func() {
				area := ui.TextArea(c.Key(key), &a.draft).
					Background(ui.Transparent).Border(0, ui.Transparent).FocusRing(false).
					Placeholder(placeholder).Lines(2, 10).Padding(14, 16, 4).
					OnPaste(pasted(0, a.onPaste)).OnTextKey(keyed(0, a.draftKey(t))).Opacity(in)
				if !ahead {
					// Opening, it takes the focus, but from the bar of
					// a selection, which keeps what's typed in it.
					if !a.quote.focused {
						area.AutoFocus()
					}
					area.Bind(&a.draftArea)
					if a.draftToEnd {
						n := runes(a.draft)
						area.SetTextSelection(n, n)
						a.draftToEnd = false
					}
				}
				if large {
					area.Grow(1)
				}
				shown(area, 0, a.draft)
			})
			return
		}
		if carry.set && !stays && carry.id == 0 {
			ui.Column(c).Key("code-box").Absolute().Opacity(0).Children(took)
		}
		// The area the composer's shortcuts focus: the one that had the
		// focus last, or the first block's.
		bound := a.code.segs[1].id
		if i := a.seg(a.codeLast); i >= 0 && a.code.segs[i].shown() {
			bound = a.codeLast
		}
		type grown struct {
			id       int
			was, now string
		}
		var grew []grown // the panes whose text grew a line at its end
		focusedID := -1  // the piece with the focus
		writing := false // a key came to it, or its text changed
		var caret func() // shows the focused pane's caret
		scroll := ui.Scroll(c).Key("code-scroll").MinHeight(0).MaxHeight(maxDraftHeight).TrackScroll(&a.codeScroll)
		if large {
			scroll.Grow(1)
		}
		scroll.Children(func() {
			if carry.set && !stays && carry.id != 0 {
				ui.Column(c).Key(fmt.Sprintf("pane-%d", carry.id)).Absolute().Opacity(0).Children(func() {
					ui.Column(c).Key("area").Children(took)
				})
			}
			for i, s := range a.code.segs {
				if !s.shown() {
					continue
				}
				col := ui.Column(c).Key(fmt.Sprintf("pane-%d", s.id))
				if s.code {
					col.Margin(10, 10, 2).Radius(10).Background(palette.text.Alpha(0.05))
				}
				col.Children(func() {
					if s.code {
						a.codePicker(c, s)
					}
					ui.Column(c).Key("area").Children(func() {
						was, wasKeyed := a.paneOf(s.id).text, a.paneOf(s.id).keyed
						area := ui.TextArea(c.Key(areaKey(s.id)), &s.text).
							Background(ui.Transparent).Border(0, ui.Transparent).FocusRing(false).
							Placeholder("").MinHeight(0).
							OnPaste(pasted(s.id, a.onPasteIn(s))).OnTextKey(keyed(s.id, a.segKey(t, s.id))).Opacity(in)
						top := float32(4)
						switch {
						case s.code:
							area.Label("Code").Lines(1, maxPieceLines).Padding(4, 12, 10).Font("monospace").FontSize(12.5)
						case i == 0:
							top = 14
							area.Label("Message").Lines(1, maxPieceLines).Padding(14, 16, 0)
						default:
							area.Label("Message").Lines(1, maxPieceLines).Padding(8, 16, 4)
							top = 8
						}
						if !ahead && s.id == bound {
							// Back from another page, the draft takes the
							// focus there, as it does without blocks.
							if !a.quote.focused {
								area.AutoFocus()
							}
							area.Bind(&a.draftArea)
						}
						if shown(area, s.id, s.text) {
							a.codeReveal = 3
						}
						if area.Focused() {
							// Text with the caret in it shows, emptied or
							// not, whether the caret came by a click or a key.
							s.open, focusedID = true, s.id
							writing = wasKeyed || s.text != was
						}
						if area.Focused() && a.codeReveal > 0 {
							caret = func() { a.revealCaret(c, area, scroll, s.id, top) }
						}
						if stays && carry.id == s.id && carried(s.text) {
							// Typed where the focus left: it went on.
							s.text = carry.text
						}
						if !ahead && grewLine(was, s.text) {
							grew = append(grew, grown{s.id, was, s.text})
						}
					})
				})
			}
		})
		if focusedID >= 0 && writing && !a.codeFocus.set {
			// Empty text the caret left, by a click too, goes as what has
			// the caret is written in: not as the click comes, which a
			// double click or a drag goes on from.
			for _, s := range a.code.segs {
				if !s.code && strings.TrimSpace(s.text) == "" && s.id != focusedID {
					s.text, s.open = "", false
				}
			}
		}
		if focusedID < 0 && roomClicked {
			a.focusBack()
		}
		if w := scroll.Bounds().W; w != a.codeScrollW {
			// Laid out at another width, the lines wrap anew: the caret
			// may have gone out of sight.
			a.codeScrollW, a.codeReveal = w, 2
			c.AnimationFrame()
		}
		if caret != nil {
			caret()
		}
		for _, g := range grew {
			a.typedInSeg(g.id, g.was, g.now)
		}
	})
}

// maxPieceLines is as many lines as a piece of a draft holding blocks
// grows to: past any it holds, as the draft scrolls, not the piece.
const maxPieceLines = 1 << 16

// revealCaret scrolls the draft, as it holds blocks, as little as shows
// pane id's caret, its text area, area, top below the top of its column:
// a text area that grows with its text never scrolls to its caret, which
// may be past the scroll's bottom. The boxes it goes by are the last
// frame's, so it goes on in the next.
func (a *app) revealCaret(c *ui.Context, area, scroll ui.Element, id int, top float32) {
	a.codeReveal--
	c.AnimationFrame()
	ab, sb := area.Bounds(), scroll.Bounds()
	if ab.H <= 0 || sb.H <= 0 {
		return
	}
	p := a.paneOf(id)
	line, col := lineCol(p.text, p.caret)
	rows := p.lines.Rows(line)
	r := 0
	for r+1 < len(rows) && rows[r+1] <= col {
		r++
	}
	h := (p.lines.Top(line+1) - p.lines.Top(line)) / float32(len(rows))
	y := ab.Y - sb.Y + a.codeScroll.Y + top + p.lines.Top(line) + float32(r)*h
	end := y + h
	if r > 0 && rows[r] == col {
		// At a wrap the caret may draw at the end of the row over it, as
		// Left takes it there: both rows show.
		y -= h
	}
	switch {
	case y < a.codeScroll.Y:
		a.codeScroll.Y = y
	case end+4 > a.codeScroll.Y+sb.H:
		a.codeScroll.Y = end + 4 - sb.H
	}
}

// codePicker is the language picker heading block s.
func (a *app) codePicker(c *ui.Context, s *segment) {
	ui.Row(c).Key("code-head").Padding(6, 6, 0).Children(func() {
		b := ghost(c, "code-language", false).Padding(3, 6).Gap(4).Label("Code block language").Children(func() {
			ui.Icon(c, icFile).FontSize(12)
			ui.Text(c, codeLanguageName(s.tag)).FontSize(11.5)
			ui.Icon(c, icChevronD).FontSize(10)
		})
		b.Menu(func(m *ui.Menu) {
			for _, l := range codeLanguages {
				if m.Item(l.name).Checked(s.tag == l.tag).Chosen() {
					s.tag = l.tag
					a.focusBack()
					if i := a.seg(a.codeLast); i >= 0 && a.code.segs[i].code && a.codeLast != s.id {
						// Chosen from another block's code: the caret
						// goes to this block, to write in the language.
						a.focusPane(s.id, a.paneOf(s.id).caret)
						a.codeFocus.quiet = true
					}
				}
			}
		})
		// The menu button takes the focus as the pointer opens it: the
		// menu, chosen from or not, gives it back to the piece that had
		// it. Reached from the keyboard, it keeps it.
		if b.Focused() && !b.FocusVisible() && !a.codeFocus.set {
			a.focusBack()
		}
	})
}

// codeTitle is a thread's title from its first message, with a code
// block: its first line that isn't a fence's, of the text over the block,
// or of the code, as pasted in too.
func codeTitle(message string) string {
	for l := range strings.SplitSeq(message, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "```") {
			return l
		}
	}
	return ""
}
