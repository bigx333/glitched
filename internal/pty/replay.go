package pty

import (
	"bytes"
	"strconv"
)

// An edit replaces the bytes from start to end of a replay with repl.
type edit struct {
	start, end int
	repl       []byte
}

// replayEdits returns, in order, the edits that keep a replay from
// prompting a new terminal emulator to answer: an emulator types its
// answers into whatever runs in the shell now. Terminal queries go, and
// Kitty graphics commands are made quiet. Everything else stays byte for
// byte, including sequences cut off at the end of p.
func replayEdits(p []byte) []edit {
	var edits []edit
	for i := 0; ; {
		j := bytes.IndexByte(p[i:], 0x1b)
		if j < 0 || i+j+1 >= len(p) {
			return edits
		}
		i += j
		switch p[i+1] {
		case '[':
			end, ok := csiEnd(p, i+2)
			if ok && csiQuery(p[i+2:end-1], p[end-1]) {
				edits = append(edits, edit{start: i, end: end})
			}
			i = end
		case 'Z': // DECID
			edits = append(edits, edit{start: i, end: i + 2})
			i += 2
		case ']', 'P', '_':
			body, end, ok := stringEnd(p, i+2, p[i+1] == ']')
			if !ok {
				i = end
				continue
			}
			switch {
			case p[i+1] == ']' && oscQuery(p[i+2:body]),
				p[i+1] == 'P' && dcsQuery(p[i+2:body]):
				edits = append(edits, edit{start: i, end: end})
			case p[i+1] == '_' && body > i+2 && p[i+2] == 'G':
				if e, ok := quietGraphics(p, i, i+3, body, end); ok {
					edits = append(edits, e)
				}
			}
			i = end
		default:
			i++
		}
	}
}

const replayLookback = 4096

// replayEditsWithPrefix returns the edits for p given prefix, the bytes
// immediately before p that the client already received. Any query whose
// escape sequence started in prefix and finishes in p is replaced with CAN
// (0x18) so the emulator aborts the half-received sequence without replying.
func replayEditsWithPrefix(prefix, p []byte) []edit {
	if len(prefix) == 0 {
		return replayEdits(p)
	}
	n := len(prefix)
	buf := make([]byte, n+len(p))
	copy(buf, prefix)
	copy(buf[n:], p)
	raw := replayEdits(buf)
	var out []edit
	for _, e := range raw {
		switch {
		case e.end <= n:
		case e.start < n:
			out = append(out, edit{start: 0, end: e.end - n, repl: []byte{0x18}})
		default:
			out = append(out, edit{start: e.start - n, end: e.end - n, repl: e.repl})
		}
	}
	return out
}

// stripReplayQueries returns p with its replayEdits applied.
func stripReplayQueries(p []byte) []byte {
	out, _ := splice(p, 0, len(p), replayEdits(p))
	return out
}

// splice copies p[from:to] with the edits inside it applied. edits holds
// those that start at or after from, in order; splice returns the rest.
func splice(p []byte, from, to int, edits []edit) ([]byte, []edit) {
	out := make([]byte, 0, to-from)
	for len(edits) > 0 && edits[0].end <= to {
		e := edits[0]
		out = append(out, p[from:e.start]...)
		out = append(out, e.repl...)
		from = e.end
		edits = edits[1:]
	}
	return append(out, p[from:to]...), edits
}

// csiEnd returns the index after the CSI sequence whose parameters start
// at i, and whether it is complete; an incomplete one ends where it broke.
func csiEnd(p []byte, i int) (int, bool) {
	for i < len(p) && p[i] >= 0x20 && p[i] <= 0x3f {
		i++
	}
	if i < len(p) && p[i] >= 0x40 && p[i] <= 0x7e {
		return i + 1, true
	}
	return i, false
}

// csiQuery reports whether CSI params final asks the terminal something.
func csiQuery(params []byte, final byte) bool {
	var prefix byte
	if len(params) > 0 && params[0] >= '<' && params[0] <= '?' {
		prefix = params[0]
	}
	inter := bytes.TrimLeft(params, "0123456789:;<=>?")
	switch {
	case final == 'p':
		return string(inter) == "$" // DECRQM
	case len(inter) > 0:
		return false
	case final == 'c':
		return prefix == 0 || prefix == '>' || prefix == '=' // DA1, DA2, DA3
	case final == 'n':
		return prefix == 0 || prefix == '?' // DSR, CPR, color scheme
	case final == 'q':
		return prefix == '>' // XTVERSION
	case final == 'u':
		return prefix == '?' // Kitty keyboard flags
	case final == 'x':
		return prefix == 0 // DECREQTPARM
	case final == 't' && prefix == 0:
		op, _, _ := bytes.Cut(params, []byte(";"))
		switch string(op) {
		case "11", "13", "14", "15", "16", "18", "19", "20", "21": // XTWINOPS reports
			return true
		}
	}
	return false
}

// stringEnd finds the end of the OSC, DCS or APC string whose body starts
// at i: ST, or BEL for an OSC. It returns where the body ends, the index
// after the terminator, and whether the string is complete; an aborted or
// cut-off one ends where it broke.
func stringEnd(p []byte, i int, bel bool) (body, end int, ok bool) {
	for j := i; j < len(p); j++ {
		switch p[j] {
		case 0x07:
			if bel {
				return j, j + 1, true
			}
		case 0x18, 0x1a: // CAN, SUB
			return j, j, false
		case 0x1b:
			if j+1 < len(p) && p[j+1] == '\\' {
				return j, j + 2, true
			}
			return j, j, false
		}
	}
	return len(p), len(p), false
}

// oscQuery reports whether OSC body asks for a color or the clipboard.
func oscQuery(body []byte) bool {
	ps, rest, _ := bytes.Cut(body, []byte(";"))
	fields := bytes.Split(rest, []byte(";"))
	n, err := strconv.Atoi(string(ps))
	switch {
	case err != nil:
		return false
	case n == 4 || n == 5 || n >= 10 && n <= 19 || n == 52:
		for _, f := range fields {
			if string(f) == "?" {
				return true
			}
		}
	case n == 21: // Kitty color protocol
		for _, f := range fields {
			if bytes.HasSuffix(f, []byte("=?")) {
				return true
			}
		}
	case n == 5522: // Kitty clipboard protocol
		for _, kv := range bytes.Split(fields[0], []byte(":")) {
			if string(kv) == "type=read" {
				return true
			}
		}
	}
	return false
}

// dcsQuery reports whether DCS body is DECRQSS or XTGETTCAP.
func dcsQuery(body []byte) bool {
	body = bytes.TrimLeft(body, "0123456789;")
	return bytes.HasPrefix(body, []byte("$q")) || bytes.HasPrefix(body, []byte("+q"))
}

// quietGraphics returns the edit of the Kitty graphics command from start
// to end, whose keys start at keys and whose body ends at body: a query
// goes, and any other command gets q=2, which keeps the terminal from
// answering it.
func quietGraphics(p []byte, start, keys, body, end int) (edit, bool) {
	ctl := p[keys:body]
	if i := bytes.IndexByte(ctl, ';'); i >= 0 {
		ctl = ctl[:i]
	}
	var out [][]byte
	hasQ, quiet := false, false
	for kv := range bytes.SplitSeq(ctl, []byte(",")) {
		switch {
		case string(kv) == "a=q":
			return edit{start: start, end: end}, true
		case bytes.HasPrefix(kv, []byte("q=")):
			hasQ, quiet = true, string(kv) == "q=2"
			kv = []byte("q=2")
		case len(kv) == 0:
			continue
		}
		out = append(out, kv)
	}
	if quiet {
		return edit{}, false
	}
	if !hasQ {
		out = append(out, []byte("q=2"))
	}
	return edit{start: keys, end: keys + len(ctl), repl: bytes.Join(out, []byte(","))}, true
}
