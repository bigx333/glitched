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

func label(s *Session, key string) string {
	if n := s.PlaceView("badge", key); n != nil {
		return n.Label
	}
	return ""
}

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
export function place({ items }) { return items.map(({ key }) => ({ type: "list", id: "l", label: key, height: 120 })) }
export function rows({ keys, place }) { calls++; return keys.map((k) => ({ type: "text", text: place.key + ":" + k + "#" + calls })) }`)
	s.SetItems("badge", []PlaceItem{{Key: "a", Version: "1"}})
	settled(t, h, u, "a drawn", func() bool { return label(s, "a") == "a" })
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
	if label(s, "a") != "a" || label(s, "b") != "b" {
		t.Fatalf("after b was drawn first: a %q, b %q", label(s, "a"), label(s, "b"))
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

// posted waits till the worker is idle with n callbacks posted to the UI
// thread and not yet run.
func posted(t *testing.T, h *Host, u *uiThread, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !h.Idle() || len(u.q) != n {
		if time.Now().After(deadline) {
			t.Fatalf("want %d callbacks posted, have %d", n, len(u.q))
		}
		time.Sleep(time.Millisecond)
	}
}

const contextModule = `export function place({ items }) {
	if (items.some((i) => i.context === "bad")) throw new Error("bad context")
	return items.map(({ context }) => context === "null" ? null : { type: "text", text: context })
}`

// A drawing that failed for a version the copy has moved past doesn't end
// a first draw of the version shown.
func TestAFailureOfAnOlderVersionDoesNotEndTheFirstDraw(t *testing.T) {
	h, s, u := placeTest(t, contextModule)
	settled(t, h, u, "the first drawing", func() bool { return s.Rev > 0 })
	s.SetItems("badge", []PlaceItem{{Key: "a", Version: "1", Context: "bad"}})
	posted(t, h, u, 1)
	s.DrawFirst("badge", []PlaceItem{{Key: "a", Version: "2", Context: "good"}}, []string{"a"}, time.Now().Add(2*time.Second))
	if text(s, "a") != "good" || s.Err != nil {
		t.Fatalf("the first draw of version 2 ended with a %q, error %v", text(s, "a"), s.Err)
	}
}

// A null drawn for a version the copy has moved past doesn't clear its
// last drawing while the version shown is drawn.
func TestANullForAnOlderVersionKeepsTheLastDrawing(t *testing.T) {
	h, s, u := placeTest(t, contextModule)
	s.SetItems("badge", []PlaceItem{{Key: "a", Version: "1", Context: "first"}})
	settled(t, h, u, "a drawn", func() bool { return text(s, "a") == "first" })
	s.SetItems("badge", []PlaceItem{{Key: "a", Version: "2", Context: "null"}})
	posted(t, h, u, 1)
	s.SetItems("badge", []PlaceItem{{Key: "a", Version: "3", Context: "third"}})
	(<-u.q)()
	if got := text(s, "a"); got != "first" {
		t.Fatalf("while version 3 is drawn, a shows %q, not its last drawing", got)
	}
	settled(t, h, u, "version 3 drawn", func() bool { return text(s, "a") == "third" })
}

// listModule draws each copy as a list of two rows, and counts timers in
// its state.
const listModule = `export function init() { return { state: 0 } }
export function update({ state, event }) { return { state: event.kind === "timer" ? state + 1 : state } }
export function place({ items }) {
	return items.map(({ key }) => ({ type: "list", id: "l", label: key, height: 120,
		items: [{ key: "r", label: "first " + key, version: "1" }, { key: "r2", label: "second " + key, version: "1" }] }))
}`

// listRow is the first row of copy key's list as the plugin drew it.
func listRow(t *testing.T, s *Session, key string) string {
	t.Helper()
	view := s.PlaceView("badge", key)
	if view == nil || view.Type != "list" || len(view.Items) == 0 {
		t.Fatalf("copy %q has no list: %+v, error %v", key, view, s.Err)
	}
	if n := s.Row(RowAt{List: view.ID, Place: &PlaceRef{ID: "badge", Key: key, Context: s.PlaceContext("badge", key)}}, view.Items[0]); n != nil {
		return n.Text
	}
	return ""
}

// Rows that failed for an earlier view leave no error once a newer one is
// shown.
func TestRowsThatFailedForAnOlderViewLeaveNoError(t *testing.T) {
	h, s, u := placeTest(t, listModule+`
export function rows({ state, keys }) {
	if (state === 0) throw new Error("rows failed")
	return keys.map((k) => ({ type: "text", text: k + " at " + state }))
}`)
	a, b := PlaceItem{Key: "a", Version: "1"}, PlaceItem{Key: "b", Version: "1"}
	s.SetItems("badge", []PlaceItem{a})
	settled(t, h, u, "a drawn", func() bool { return s.PlaceView("badge", "a") != nil })
	listRow(t, s, "a")
	posted(t, h, u, 1)
	s.Notify(AppEvent{Kind: "timer"})
	s.DrawFirst("badge", []PlaceItem{a, b}, []string{"b"}, time.Now().Add(2*time.Second))
	if s.PlaceView("badge", "b") == nil || s.Err != nil {
		t.Fatalf("after b was drawn first: b %+v, error %v", s.PlaceView("badge", "b"), s.Err)
	}
	(<-u.q)()
	u.until(t, "a's row drawn for the new view", func() bool { return listRow(t, s, "a") == "r at 1" })
	if s.Err != nil {
		t.Fatalf("rows that failed for the earlier view: %v", s.Err)
	}
}

// A copy answered null, then drawn by a drawing posted but not yet shown,
// after an event or a retry, shows that drawing at once.
func TestAPostedDrawingReplacesAnEarlierNull(t *testing.T) {
	for _, change := range []string{"event", "retry"} {
		t.Run(change, func(t *testing.T) {
			h, s, u := placeTest(t, `let starts = 0
export function init() { return { state: ++starts > 1 } }
export function update({ state, event }) { return { state: state || event.kind === "timer" } }
export function place({ state, items }) { return items.map(() => state ? { type: "text", text: "ready" } : null) }`)
			items := []PlaceItem{{Key: "a", Version: "1"}}
			s.SetItems("badge", items)
			settled(t, h, u, "a answered null", func() bool { return s.Rev > 0 && s.PlaceView("badge", "a") == nil })
			if change == "retry" {
				s.Retry()
			} else {
				s.Notify(AppEvent{Kind: "timer"})
			}
			posted(t, h, u, 1)
			s.DrawFirst("badge", items, []string{"a"}, time.Now().Add(time.Second))
			if got := text(s, "a"); got != "ready" {
				t.Fatalf("a shows %q, not the drawing already posted", got)
			}
		})
	}
}

// contextListModule draws each copy as a list of one row, with the copy's
// context as its label and the row's, but for the contexts "no list",
// "null" and "failed".
const contextListModule = `export function place({items}) {
  if (items.some(({context}) => context === "failed")) throw new Error("place refused");
  return items.map(({context}) => context === "no list" ? {type: "text", text: context}
    : context === "null" ? null
    : {type: "list", id: "l", height: 120, label: context, items: [{key: "r", label: "row", version: context}]});
}
export function rows({place, keys}) { return keys.map(() => ({type: "text", text: place.context})) }
`

// A first draw that shows, on its way, a drawing posted before it, of a
// copy without its list, leaves the list's rows to the drawing it ends on.
func TestAFirstDrawKeepsAListsRowsThroughADrawingShownOnItsWay(t *testing.T) {
	h, s, u := placeTest(t, contextListModule)
	a := PlaceItem{Key: "a", Version: "1", Context: "last row"}
	s.SetItems("badge", []PlaceItem{a})
	settled(t, h, u, "initial copy", func() bool { return s.PlaceView("badge", "a") != nil })
	u.until(t, "initial row", func() bool { return listRow(t, s, "a") == "last row" })
	s.SetItems("badge", []PlaceItem{{Key: "a", Version: "2", Context: "no list"}})
	posted(t, h, u, 1)
	a.Version, a.Context = "3", "new row"
	b := PlaceItem{Key: "b", Version: "1", Context: "new b"}
	s.DrawFirst("badge", []PlaceItem{a, b}, []string{"b"}, time.Now().Add(2*time.Second))
	if n := s.PlaceView("badge", "a"); n == nil || n.Label != "new row" || s.PlaceView("badge", "b") == nil {
		t.Fatalf("DrawFirst did not publish both current copies: a=%+v b=%+v", n, s.PlaceView("badge", "b"))
	}
	if got := listRow(t, s, "a"); got != "last row" {
		t.Errorf("DrawFirst was given a:v3 and b, then a:v3's list showed %q before its row refresh; want %q", got, "last row")
	}
	u.until(t, "row refresh", func() bool { return listRow(t, s, "a") == "new row" })
}

// A copy answered null stays so till a drawing of its version comes.
func TestADrawingOfAnOlderVersionNeverReplacesACopyAnsweredNull(t *testing.T) {
	h, s, u := placeTest(t, contextListModule)
	a := PlaceItem{Key: "a", Version: "1", Context: "null"}
	s.SetItems("badge", []PlaceItem{{Key: "b", Version: "1", Context: "other"}, a})
	settled(t, h, u, "a answered and b visible", func() bool { return s.PlaceView("badge", "b") != nil })
	if s.PlaceView("badge", "a") != nil || s.places.answered[[2]string{"badge", "a"}] != "1" {
		t.Fatal("a was not answered null")
	}
	s.SetItems("badge", []PlaceItem{{Key: "a", Version: "2", Context: "obsolete list"}})
	posted(t, h, u, 1)
	s.SetItems("badge", []PlaceItem{{Key: "a", Version: "3", Context: "failed"}})
	posted(t, h, u, 2)
	(<-u.q)()
	(<-u.q)()
	if n := s.PlaceView("badge", "a"); n != nil {
		t.Errorf("after a:v1 was accepted null and a:v3 failed, obsolete a:v2 showed and stayed: %s %q", n.Type, n.Label)
	}
}

// A copy without a version, gone and back before the drawing without it
// shows, keeps its drawing and its list's rows.
func TestACopyGoneAndBackUnversionedKeepsItsDrawing(t *testing.T) {
	h, s, u := placeTest(t, contextListModule)
	a := PlaceItem{Key: "a", Context: "last row"}
	s.SetItems("badge", []PlaceItem{a})
	settled(t, h, u, "unversioned copy", func() bool { return s.PlaceView("badge", "a") != nil })
	u.until(t, "initial row", func() bool { return listRow(t, s, "a") == "last row" })
	s.SetItems("badge", nil)
	posted(t, h, u, 1)
	s.SetItems("badge", []PlaceItem{a})
	posted(t, h, u, 2)
	(<-u.q)()
	if s.PlaceView("badge", "a") == nil {
		t.Error("an omission from an old items snapshot cleared a currently requested empty-version copy")
	}
	(<-u.q)()
	if got := listRow(t, s, "a"); got != "last row" {
		t.Errorf("after return, copy's list showed %q before refresh; want %q", got, "last row")
	}
}

// A row asked for a copy removed and drawn again within one first draw
// never shows in the copy's new list.
func TestARowOfACopyRemovedWithinAFirstDrawDoesNotShowWhenItReturns(t *testing.T) {
	h, s, u := placeTest(t, `export function place({ items }) {
	return items.map(({ context }) => ({ type: "list", id: "l", height: 120, label: context, items: [{ key: "r", label: "row", version: context }] }))
}
export function rows({ place, keys }) { return keys.map(() => ({ type: "text", text: place.context })) }`)
	a := PlaceItem{Key: "a", Version: "1", Context: "old"}
	s.SetItems("badge", []PlaceItem{a})
	settled(t, h, u, "a drawn", func() bool { return s.PlaceView("badge", "a") != nil })
	listRow(t, s, "a")
	posted(t, h, u, 1)
	s.SetItems("badge", nil)
	posted(t, h, u, 2)
	a.Version, a.Context = "2", "new"
	s.DrawFirst("badge", []PlaceItem{a}, []string{"a"}, time.Now().Add(2*time.Second))
	listRow(t, s, "a")
	(<-u.q)()
	if got := listRow(t, s, "a"); got != "" {
		t.Fatalf("a's new list shows %q, the row asked before a was removed", got)
	}
	u.until(t, "a's new row", func() bool { return listRow(t, s, "a") == "new" })
}

// A copy drawn null on a first draw's way, and drawn again with its list
// as the first draw ends, keeps its list's rows.
func TestACopyDrawnNullOnAFirstDrawsWayKeepsItsListsRows(t *testing.T) {
	h, s, u := placeTest(t, contextListModule)
	a := PlaceItem{Key: "a", Version: "1", Context: "last row"}
	s.SetItems("badge", []PlaceItem{a})
	settled(t, h, u, "a drawn", func() bool { return s.PlaceView("badge", "a") != nil })
	u.until(t, "a's row drawn", func() bool { return listRow(t, s, "a") == "last row" })
	s.SetItems("badge", []PlaceItem{{Key: "a", Version: "2", Context: "null"}})
	posted(t, h, u, 1)
	a.Version, a.Context = "3", "current row"
	s.DrawFirst("badge", []PlaceItem{a, {Key: "b", Version: "1", Context: "b"}}, []string{"b"}, time.Now().Add(2*time.Second))
	if label(s, "a") != "current row" {
		t.Fatalf("the first draw ended with a as %q", label(s, "a"))
	}
	if got := listRow(t, s, "a"); got != "last row" {
		t.Fatalf("after a was drawn null on the way, its list shows %q, not its last drawing", got)
	}
}

// A list read from a drawing no longer shown, as a renderer still drawing
// one it took before, draws no row: its rows never reach the list shown
// next with its id.
func TestAListNoDrawingShowsDrawsNoRow(t *testing.T) {
	h, s, u := placeTest(t, contextListModule)
	a := PlaceItem{Key: "a", Version: "1", Context: "first"}
	s.SetItems("badge", []PlaceItem{a})
	settled(t, h, u, "a drawn", func() bool { return s.PlaceView("badge", "a") != nil })
	taken := s.PlaceView("badge", "a")
	u.until(t, "a's row drawn", func() bool { return listRow(t, s, "a") == "first" })
	s.SetItems("badge", []PlaceItem{{Key: "a", Version: "2", Context: "no list"}})
	settled(t, h, u, "a drawn without its list", func() bool { return s.PlaceView("badge", "a").Type == "text" })
	if n := s.Row(RowAt{List: "l", Place: &PlaceRef{ID: "badge", Key: "a", Context: "taken"}}, taken.Items[0]); n != nil {
		t.Fatalf("a list no drawing shows drew %q", n.Text)
	}
	settled(t, h, u, "the worker done", func() bool { return true })
	a.Version, a.Context = "3", "again"
	s.SetItems("badge", []PlaceItem{a})
	settled(t, h, u, "a drawn with its list", func() bool { return label(s, "a") == "again" })
	if got := listRow(t, s, "a"); got != "" {
		t.Fatalf("a's list shown again shows %q, a row asked of the list no drawing showed", got)
	}
	u.until(t, "a's row drawn again", func() bool { return listRow(t, s, "a") == "again" })
}
