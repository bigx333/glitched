package plugins

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"glitched/internal/plugins/widget"
)

const rowsJS = `
let calls = 0, asked = [];
export function init() { return { state: { n: 0 } }; }
export function update({ state, event }) { return { state: { n: state.n + 1, broken: event.handler === "break" } }; }
export function view({ state }) {
  if (state.broken) throw new Error("broken");
  return { body: { type: "column", children: [{ type: "text", text: "asked " + asked.join(",") }, { type: "list", id: "l", label: "rows", height: 120 }] } };
}
export function rows({ list, keys, state }) {
  calls++;
  asked.push(...keys);
  return keys.map((k) => k === "none" ? null : k === "bad" ? { type: "input", id: "i" } : { type: "text", text: list + ":" + k + "@" + state.n + "#" + calls });
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
	s.Row(at, widget.ListItem{Key: "bad"})
	s.Row(at, widget.ListItem{Key: "d"})
	drawn("d", "", "l:d@0")
	u.until(t, "the bad row's error", func() bool { return s.Err != nil })
	if !strings.Contains(s.Err.Error(), "bad: ") || s.Row(at, widget.ListItem{Key: "bad"}) != nil {
		t.Fatalf("a row that doesn't check: %v", s.Err)
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
	if v := texts(s); !strings.Contains(v, "asked a,b,c,") || strings.Count(v, "none") != 1 || strings.Count(v, "bad") != 1 {
		t.Fatalf("the plugin was asked for %s: the null and the bad row once", v)
	}
	if s.Err != nil {
		t.Fatal(s.Err)
	}

	// A view refused keeps the last good one, whose rows are drawn with
	// the state it was drawn with.
	good := s.Rev
	s.Dispatch(UIEvent{Type: "click", Handler: "break", Node: "n"}, nil)
	u.until(t, "the view refused", func() bool { return s.Err != nil })
	if s.Rev != good {
		t.Fatal("a view that threw was shown")
	}
	drawn("e", "", "l:e@2")

	// Scrolled through, a list keeps the rows it showed last.
	for i := range MaxRowsKept + 100 {
		s.Row(at, widget.ListItem{Key: fmt.Sprint("k", i)})
	}
	last := fmt.Sprint("k", MaxRowsKept+99)
	u.until(t, "the last row", func() bool { return s.Row(at, widget.ListItem{Key: last}) != nil })
	if n := len(s.rows.lists[at.key()].drawn); n > MaxRowsKept {
		t.Fatalf("a list keeps %d rows drawn", n)
	}

	no := session(t, h, "norows")
	u.until(t, "its view", func() bool { return no.Rev > 0 })
	no.Row(at, widget.ListItem{Key: "a"})
	u.until(t, "the error", func() bool { return no.Err != nil })
	if !strings.Contains(no.Err.Error(), "no rows export") {
		t.Fatal(no.Err)
	}
}

// A list's row keeps its drawing while other copies are drawn first and
// their lists read, however many, until its own list is read again.
func TestARowKeepsItsDrawingWhileOtherListsAreDrawnFirst(t *testing.T) {
	h, s, u := placeTest(t, listModule+`
export function rows({ keys, place }) { return keys.map((k) => ({ type: "text", text: place.key + ":" + k })) }`)
	copies := []PlaceItem{{Key: "a", Version: "1"}}
	s.SetItems("badge", copies)
	settled(t, h, u, "a drawn", func() bool { return s.PlaceView("badge", "a") != nil })
	u.until(t, "a's row drawn", func() bool { return listRow(t, s, "a") == "a:r" })
	for _, key := range []string{"b", "c", "d"} {
		copies = append(copies, PlaceItem{Key: key, Version: "1"})
		s.DrawFirst("badge", copies, []string{key}, time.Now().Add(2*time.Second))
		listRow(t, s, key)
	}
	if got := listRow(t, s, "a"); got != "a:r" {
		t.Fatalf("after b, c and d were drawn first and their lists read, a's row shows %q, not its drawing", got)
	}
}

// A row drawn null for a view the session has moved past doesn't clear
// the row's last drawing while it is drawn for the view shown.
func TestANullRowForAnOlderViewKeepsTheLastDrawing(t *testing.T) {
	h, s, u := placeTest(t, listModule+`
export function rows({ state, keys }) { return keys.map((k) => state === 1 ? null : { type: "text", text: k + " at " + state }) }`)
	a, b := PlaceItem{Key: "a", Version: "1"}, PlaceItem{Key: "b", Version: "1"}
	s.SetItems("badge", []PlaceItem{a})
	settled(t, h, u, "a drawn", func() bool { return s.PlaceView("badge", "a") != nil })
	u.until(t, "a's row drawn", func() bool { return listRow(t, s, "a") == "r at 0" })
	rev := s.Rev
	s.Notify(AppEvent{Kind: "timer"})
	settled(t, h, u, "the view of state 1", func() bool { return s.Rev > rev })
	listRow(t, s, "a")
	posted(t, h, u, 1)
	s.Notify(AppEvent{Kind: "timer"})
	s.DrawFirst("badge", []PlaceItem{a, b}, []string{"b"}, time.Now().Add(2*time.Second))
	(<-u.q)()
	if got := listRow(t, s, "a"); got != "r at 0" {
		t.Fatalf("after the null row of state 1 came, a's row shows %q, not its last drawing", got)
	}
	u.until(t, "a's row drawn for state 2", func() bool { return listRow(t, s, "a") == "r at 2" })
}

// The rows of a list no drawing shows any more go: a copy gone takes its
// list's rows with it.
func TestTheRowsOfAListGoneAreDropped(t *testing.T) {
	h, s, u := placeTest(t, listModule+"\nexport function rows({ keys }) { return keys.map((k) => ({ type: \"text\", text: k })) }")
	a, b := PlaceItem{Key: "a", Version: "1"}, PlaceItem{Key: "b", Version: "1"}
	s.SetItems("badge", []PlaceItem{a, b})
	settled(t, h, u, "a and b drawn", func() bool { return s.PlaceView("badge", "b") != nil })
	u.until(t, "their rows drawn", func() bool { return listRow(t, s, "a") == "r" && listRow(t, s, "b") == "r" })
	s.SetItems("badge", []PlaceItem{a})
	settled(t, h, u, "b gone", func() bool { return s.PlaceView("badge", "b") == nil })
	listRow(t, s, "a")
	if _, ok := s.rows.lists[rowKey{"badge", "b", "l"}]; ok || len(s.rows.lists) != 1 {
		t.Fatalf("with b gone, the session keeps the rows of %d lists, b's among them: %v", len(s.rows.lists), ok)
	}
}
