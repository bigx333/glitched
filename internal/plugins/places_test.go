package plugins

import (
	"strings"
	"testing"
	"time"

	"glitched/internal/plugins/widget"
)

// placeTest is a plugin of one thread.row place, whose module is js, with
// its app session.
func placeTest(t *testing.T, js string) (*Host, *Session, *uiThread) {
	t.Helper()
	state, u := t.TempDir(), newUIThread()
	writeModule(t, state, "rows", badgeManifest(`"ui.place", "threads:read"`), js)
	h := Open(state, Options{Post: u.post, Logf: t.Logf})
	t.Cleanup(h.Close)
	if err := h.SetGranted("rows", "threads:read", true); err != nil {
		t.Fatal(err)
	}
	s := h.App("rows")
	if s == nil {
		t.Fatal("no app session")
	}
	return h, s, u
}

// settled waits till the session has drawn and its drawing is on the UI
// thread.
func settled(t *testing.T, h *Host, u *uiThread, what string, ok func() bool) {
	t.Helper()
	u.until(t, what, func() bool { return h.Idle() && len(u.q) == 0 && ok() })
}

// countingModule draws each copy as its key and the number of copies
// drawn so far, so a copy drawn again says so.
const countingModule = `let drawn = 0
export function init() { return { state: {} } }
export function update() { return { state: { n: Date.now() } } }
export function place({ items }) {
	return items.map(({ key, context }) => context === "hide" ? null : { type: "text", text: key + " " + ++drawn })
}`

func text(s *Session, key string) string {
	if n := s.PlaceView("badge", key); n != nil {
		return n.Text
	}
	return ""
}

// A component's copy drawn for the first time, as a row scrolled into
// view, mustn't take the other copies' drawings away while it is drawn.
func TestDrawingACopyFirstKeepsTheOthersDrawings(t *testing.T) {
	h, s, u := placeTest(t, countingModule)
	s.SetItems("badge", []PlaceItem{{Key: "a", Version: "1"}, {Key: "b", Version: "1"}})
	settled(t, h, u, "a and b drawn", func() bool { return text(s, "a") != "" && text(s, "b") != "" })
	// The frame has drawn only c so far when it waits for c.
	s.DrawFirst("badge", []PlaceItem{{Key: "c", Version: "1"}}, []string{"c"}, time.Now().Add(5*time.Second))
	if text(s, "c") == "" || text(s, "a") == "" || text(s, "b") == "" {
		t.Fatalf("after drawing c first: a %q, b %q, c %q; want all three drawn", text(s, "a"), text(s, "b"), text(s, "c"))
	}
}

// Only a copy that came or changed is drawn again when the copies shown
// change; anything else draws them all.
func TestOnlyTheCopiesThatChangedAreDrawnAgain(t *testing.T) {
	h, s, u := placeTest(t, countingModule)
	s.SetItems("badge", []PlaceItem{{Key: "a", Version: "1"}})
	settled(t, h, u, "a drawn", func() bool { return text(s, "a") == "a 1" })
	s.SetItems("badge", []PlaceItem{{Key: "a", Version: "1"}, {Key: "b", Version: "1"}})
	settled(t, h, u, "b drawn", func() bool { return text(s, "b") != "" })
	if a, b := text(s, "a"), text(s, "b"); a != "a 1" || b != "b 2" {
		t.Fatalf("with b come, a %q and b %q; want a kept as a 1, b drawn as b 2", a, b)
	}
	s.SetItems("badge", []PlaceItem{{Key: "a", Version: "2"}, {Key: "b", Version: "1"}})
	settled(t, h, u, "a drawn again", func() bool { return text(s, "a") == "a 3" })
	if b := text(s, "b"); b != "b 2" {
		t.Errorf("b %q after only a changed; want b 2", b)
	}
	// An event may change what every copy shows: all are drawn.
	s.Notify(AppEvent{Kind: "timer", ID: "tick"})
	settled(t, h, u, "both drawn after an event", func() bool { return text(s, "b") != "b 2" })
	if a, b := text(s, "a"), text(s, "b"); a == "a 3" || b == "b 2" {
		t.Errorf("after an event a %q, b %q; want both drawn again", a, b)
	}
}

// A copy the plugin answers null for has no drawing, even one it had.
func TestANullAnswerClearsTheCopy(t *testing.T) {
	h, s, u := placeTest(t, countingModule)
	s.SetItems("badge", []PlaceItem{{Key: "a", Version: "1", Context: "show"}})
	settled(t, h, u, "a drawn", func() bool { return text(s, "a") != "" })
	rev := s.Rev
	s.SetItems("badge", []PlaceItem{{Key: "a", Version: "2", Context: "hide"}})
	settled(t, h, u, "a drawn null", func() bool { return s.Rev > rev })
	if n := s.PlaceView("badge", "a"); n != nil {
		t.Fatalf("a answered null still shows %q", n.Text)
	}
}

// place may read the plugin's settings: changing one draws every copy
// again, whether or not the plugin hears setting.changed.
func TestChangingASettingDrawsTheCopiesAgain(t *testing.T) {
	h, s, u := placeTest(t, `export function init() { return { state: {} } }
export function place({ items }) { return items.map(() => ({ type: "text", text: String(host.setting("show")) })) }`)
	s.SetItems("badge", []PlaceItem{{Key: "a", Version: "1"}})
	settled(t, h, u, "a drawn", func() bool { return text(s, "a") == "true" })
	if err := h.SetSetting("rows", "show", false); err != nil {
		t.Fatal(err)
	}
	settled(t, h, u, "a drawn with the setting", func() bool { return text(s, "a") == "false" })
}

// A copy the worker has already answered null for, at the version shown,
// has nothing to wait for.
func TestACopyAlreadyAnsweredNullIsNotWaitedFor(t *testing.T) {
	h, s, u := placeTest(t, countingModule)
	items := []PlaceItem{{Key: "a", Version: "1", Context: "hide"}, {Key: "b", Version: "1"}}
	s.SetItems("badge", items)
	settled(t, h, u, "a and b answered", func() bool { return text(s, "b") != "" })
	deadline := time.Now().Add(time.Second)
	s.DrawFirst("badge", items, []string{"a"}, deadline)
	if !time.Now().Before(deadline) {
		t.Fatal("DrawFirst waited its whole deadline for a copy already answered null")
	}
}

// A drawing published late, for versions the frame has since moved past,
// mustn't replace the newer drawing.
func TestAnOlderDrawingNeverReplacesANewerOne(t *testing.T) {
	h, s, u := placeTest(t, countingModule)
	s.SetItems("badge", []PlaceItem{{Key: "a", Version: "1"}})
	settled(t, h, u, "a drawn", func() bool { return text(s, "a") != "" })
	s.SetItems("badge", []PlaceItem{{Key: "a", Version: "2", Context: "hide"}})
	deadline := time.Now().Add(3 * time.Second)
	for !h.Idle() || len(u.q) != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("want the null drawing queued, have %d", len(u.q))
		}
		time.Sleep(time.Millisecond)
	}
	s.DrawFirst("badge", []PlaceItem{{Key: "a", Version: "3"}, {Key: "b", Version: "1"}}, []string{"b"}, time.Now().Add(2*time.Second))
	a := text(s, "a")
	if a == "" || text(s, "b") == "" {
		t.Fatalf("the first draw left a %q, b %q; want both drawn", a, text(s, "b"))
	}
	(<-u.q)()
	if text(s, "a") != a {
		t.Fatalf("the late null drawing of a's version 2 replaced its version 3 %q with %q", a, text(s, "a"))
	}
}

// A list in a copy keeps showing its rows while another copy is drawn
// first, and they are drawn again for the drawing that copy is in.
func TestAListInACopyKeepsItsRowsWhileAnotherIsDrawnFirst(t *testing.T) {
	h, s, u := placeTest(t, `let calls = 0
export function place({ items }) { return items.map(({ key }) => ({ type: "text", text: key })) }
export function rows({ keys, place }) { calls++; return keys.map((k) => ({ type: "text", text: place.key + ":" + k + "#" + calls })) }`)
	s.SetItems("badge", []PlaceItem{{Key: "a", Version: "1"}})
	settled(t, h, u, "a drawn", func() bool { return text(s, "a") == "a" })
	at := RowAt{List: "l", Place: &PlaceRef{ID: "badge", Key: "a"}}
	row := func() string {
		if n := s.Row(at, widget.ListItem{Key: "r"}); n != nil {
			return n.Text
		}
		return ""
	}
	u.until(t, "a's row drawn", func() bool { return row() != "" })
	first := row()
	s.DrawFirst("badge", []PlaceItem{{Key: "a", Version: "1"}, {Key: "b", Version: "1"}}, []string{"b"}, time.Now().Add(2*time.Second))
	if text(s, "a") != "a" || text(s, "b") != "b" {
		t.Fatalf("after b was drawn first: a %q, b %q", text(s, "a"), text(s, "b"))
	}
	if got := row(); got != first {
		t.Fatalf("a's row shows %q while b is drawn first, not its last drawing %q", got, first)
	}
	u.until(t, "a's row drawn for the new drawing", func() bool { return row() != first && strings.HasPrefix(row(), "a:r#") })
	if s.Err != nil {
		t.Fatal(s.Err)
	}
}

// A copy the worker has drawn, in a drawing posted to the UI thread but
// not yet shown, is shown at once rather than waited for.
func TestACopyDrawnButNotYetShownIsNotWaitedFor(t *testing.T) {
	h, s, u := placeTest(t, countingModule)
	settled(t, h, u, "the first drawing", func() bool { return s.Rev > 0 })
	items := []PlaceItem{{Key: "a", Version: "1"}}
	s.SetItems("badge", items)
	deadline := time.Now().Add(3 * time.Second)
	for !h.Idle() || len(u.q) != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("want a's drawing posted, have %d", len(u.q))
		}
		time.Sleep(time.Millisecond)
	}
	deadline = time.Now().Add(time.Second)
	s.DrawFirst("badge", items, []string{"a"}, deadline)
	if !time.Now().Before(deadline) || text(s, "a") == "" {
		t.Fatalf("DrawFirst waited till its deadline for a drawing already posted: a %q", text(s, "a"))
	}
}
