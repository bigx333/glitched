package main

import (
	"math"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
)

// wrapMark ends a line of an agent's text that goes on in the next one,
// and spaceMark is a space that ends a line. No text plain.Text shows can
// hold either, so neither passes for anything the agent typed.
const wrapMark, spaceMark = '↩', '·'

// verbatimFirstW is the width a verbatim text wraps to before it was laid
// out: narrow enough that its lines fit any card.
const verbatimFirstW = 240

// verbatim is an agent's text, as plain.Text shows it, drawn as typed: in
// a fixed-width font, each character in a cell of its own, a line too long
// for the width wrapped at a cell and ended with wrapMark, and a space
// ending a line shown as spaceMark. Left to wrap a line itself, the text
// engine breaks it where it likes: on Linux, it hyphenates a long word, a
// wrap draws as a line break, and a space where it wraps draws as none.
// Each line is as tall as any other. Copy copies the text as typed. It
// returns where the last frame drew each of its lines.
func (a *app) verbatim(c *ui.Context, key any, s string, size float32, col ui.Color) []textLine {
	box := ui.Box(c.Key(verbatimKey{key})).MinWidth(0).Selectable()
	var text ui.Element
	cell, line := c.MeasureText(0, ui.Span{Text: strings.Repeat("0", 64), Font: "monospace", Size: size})
	mark, _ := c.MeasureText(0, ui.Span{Text: string(wrapMark), Font: "monospace", Size: size})
	w := box.Bounds().W
	if w <= 0 {
		// Not laid out yet: the next frame wraps it to its width.
		w = verbatimFirstW
		c.AnimationFrame()
	}
	// A cell to spare, for a cut's … in a font of its own.
	cols := max(8, int((w-mark)/(cell/64))-1)
	spans := verbatimSpans(s, cols, ui.Span{Font: "monospace", Size: size, Color: col},
		ui.Span{Font: "monospace", Size: size, Color: palette.muted})
	// A mark or a cut's … the font lacks draws in a taller one, as ↩ does
	// on macOS, and a fixed line height only grows the line holding it:
	// the lines are as tall as the font's with the marks the text draws.
	var marks string
	for _, r := range "…" + string(wrapMark) + string(spaceMark) {
		if slices.ContainsFunc(spans, func(sp ui.Span) bool { return strings.ContainsRune(sp.Text, r) }) {
			marks += string(r)
		}
	}
	if marks != "" {
		_, line = c.MeasureText(0, ui.Span{Text: "0" + marks, Font: "monospace", Size: size})
	}
	box.CopyData(func(parts []ui.SelectedText) transfer.Data {
		var out []string
		for _, p := range parts {
			out = append(out, typed(p.Text, p.Start, p.End))
		}
		return transfer.TextData(strings.Join(out, "\n"))
	}).Children(func() {
		text = ui.RichText(c, spans...).FixedLineHeight(line)
	})
	return textLines(text.Bounds(), line, wraps(s, cols))
}

// textLine is a line of a text where the last frame drew it, and the runes
// of the text it shows, from from to to, to being where it ends: the
// break after it, or the end of the text.
type textLine struct {
	at       ui.Rect
	from, to int
}

// textLines are the lines of a text the last frame drew at, each high,
// that shows the runes ws; none when the last frame did not draw it as
// that many lines.
func textLines(at ui.Rect, high float32, ws [][2]int) []textLine {
	if at.H <= 0 || math.Abs(float64(at.H-high*float32(len(ws)))) > 0.5 {
		return nil
	}
	lines := make([]textLine, len(ws))
	for i, w := range ws {
		lines[i] = textLine{ui.Rect{X: at.X, Y: at.Y + high*float32(i), W: at.W, H: high}, w[0], w[1]}
	}
	return lines
}

// wraps are the runes of s each line of it shows, cut to cols cells as
// verbatimSpans cuts it: from, and to, where the line ends.
func wraps(s string, cols int) [][2]int {
	var ws [][2]int
	start := 0
	for _, l := range strings.Split(s, "\n") {
		n := utf8.RuneCountInString(l)
		for j := 0; ; j += cols {
			ws = append(ws, [2]int{start + j, start + min(j+cols, n)})
			if j+cols >= n {
				break
			}
		}
		start += n + 1
	}
	return ws
}

// verbatimKey keys a verbatim text apart from its field's other elements.
type verbatimKey struct{ key any }

// verbatimSpans are s's lines cut to cols cells each, a cut line ending
// in wrapMark, and the spaces that end each line shown as spaceMark: the
// text in the style of text, the marks in that of marks.
func verbatimSpans(s string, cols int, text, marks ui.Span) []ui.Span {
	var spans []ui.Span
	add := func(st ui.Span, r rune) {
		if n := len(spans); n > 0 && spans[n-1].Color == st.Color {
			spans[n-1].Text += string(r)
			return
		}
		st.Text = string(r)
		spans = append(spans, st)
	}
	for i, l := range strings.Split(s, "\n") {
		if i > 0 {
			add(text, '\n')
		}
		rs := []rune(l)
		end := len(rs)
		for end > 0 && rs[end-1] == ' ' {
			end--
		}
		for j, r := range rs {
			if j > 0 && j%cols == 0 {
				add(marks, wrapMark)
				add(marks, '\n')
			}
			if j >= end {
				add(marks, spaceMark)
			} else {
				add(text, r)
			}
		}
	}
	return spans
}

// typed is the runes from start to end of a verbatim text, as the agent
// typed them: without the wraps, and with spaces for the marks that show
// them.
func typed(shown string, start, end int) string {
	rs := []rune(shown)
	var b strings.Builder
	for i := max(0, start); i < min(end, len(rs)); i++ {
		switch {
		case rs[i] == wrapMark, rs[i] == '\n' && i > 0 && rs[i-1] == wrapMark:
		case rs[i] == spaceMark:
			b.WriteRune(' ')
		default:
			b.WriteRune(rs[i])
		}
	}
	return b.String()
}
