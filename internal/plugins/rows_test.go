package plugins

import (
	"strings"
	"testing"

	"glitched/internal/plugins/widget"
)

const rowsJS = `
let calls = 0, asked = [];
export function init() { return { state: { n: 0 } }; }
export function update({ state }) { return { state: { n: state.n + 1 } }; }
export function view({ state }) {
  return { body: { type: "text", text: "asked " + asked.join(",") } };
}
export function rows({ list, keys, state }) {
  calls++;
  asked.push(...keys);
  return keys.map((k) => k === "none" ? null : { type: "text", text: list + ":" + k + "@" + state.n + "#" + calls });
}
`

// A list's rows are drawn by the rows export for the keys asked, once for
// each view and version: a row asked again shows what was drawn, and a row
// drawn for an earlier view shows till it is drawn again.
func TestAListAsksForTheRowsShownOnce(t *testing.T) {
	state := t.TempDir()
	u, fx := newUIThread(), &effects{}
	writeModule(t, state, "rows", "", rowsJS)
	writeModule(t, state, "norows", "", `export function view() { return { body: { type: "text", text: "x" } }; }`)
	h := open(t, state, u, fx)
	s := session(t, h, "rows")
	u.until(t, "the view", func() bool { return s.Rev > 0 })
	at := RowAt{List: "l"}
	text := func(key, version string) string {
		if n := s.Row(at, widget.ListItem{Key: key, Version: version}); n != nil {
			return n.Text
		}
		return ""
	}
	drawn := func(key, version, want string) {
		t.Helper()
		u.until(t, key+" drawn as "+want, func() bool { return strings.HasPrefix(text(key, version), want) })
	}
	drawn("a", "", "l:a@0")
	drawn("b", "", "l:b@0")
	first := text("a", "")
	drawn("c", "", "l:c@0")
	if text("a", "") != first {
		t.Fatalf("a row asked again was drawn again: %s, then %s", first, text("a", ""))
	}
	if n := s.Row(at, widget.ListItem{Key: "none"}); n != nil {
		t.Fatal("a row the plugin drew null shows")
	}

	// A new version of one item draws it alone, its last drawing showing
	// meanwhile.
	before := text("b", "")
	if got := text("b", "2"); got != before {
		t.Fatalf("a row of a new version shows %q while it's drawn again, not its last drawing %q", got, before)
	}
	u.until(t, "b drawn again", func() bool { return text("b", "2") != before })
	if text("a", "") != first {
		t.Fatal("a new version of b drew a again")
	}

	// A new view, after the state changed, draws the rows shown again.
	rev := s.Rev
	s.Dispatch(UIEvent{Type: "click", Handler: "h", Node: "n"}, nil)
	u.until(t, "the next view", func() bool { return s.Rev > rev })
	if got := text("a", ""); got != first {
		t.Fatalf("a row shows %q while it's drawn for the new view, not its last drawing", got)
	}
	drawn("a", "", "l:a@1")
	rev = s.Rev
	s.Dispatch(UIEvent{Type: "click", Handler: "h", Node: "n"}, nil)
	u.until(t, "a view after", func() bool { return s.Rev > rev })
	if v := texts(s); !strings.Contains(v, "asked a,b,c,") || strings.Count(v, "none") != 1 || strings.Count(v, "a") != 3 {
		t.Fatalf("the plugin was asked for %s: the null row once", v)
	}
	if s.Err != nil {
		t.Fatal(s.Err)
	}

	no := session(t, h, "norows")
	u.until(t, "its view", func() bool { return no.Rev > 0 })
	no.Row(at, widget.ListItem{Key: "a"})
	u.until(t, "the error", func() bool { return no.Err != nil })
	if !strings.Contains(no.Err.Error(), "no rows export") {
		t.Fatal(no.Err)
	}
}
