package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/daemon"
	"glitched/internal/plugins"
	"glitched/internal/provider/fake"
)

// revealSettle lets the plugins draw what changed, and the frame after.
func revealSettle(t *testing.T, h *harness) {
	t.Helper()
	settlePlugins(t, h.a, h.tt, h.p)
	h.tt.Advance(time.Second)
	settlePlugins(t, h.a, h.tt, h.p)
}

// revealOpen opens th as the palette does, with threads.open.
func revealOpen(t *testing.T, h *harness, th thread) {
	t.Helper()
	raw, _ := json.Marshal(map[string]string{"thread": th.id().String()})
	if _, err := h.a.pluginEffect("palette", plugins.Effect{Type: "threads.open", Args: raw}); err != nil {
		t.Fatal(err)
	}
	revealSettle(t, h)
	if h.a.current().id() != th.id() {
		t.Fatalf("opening %q selected %q", th.View.Title, h.a.current().View.Title)
	}
}

// revealRowOf is where the sidebar shows the row of the thread titled
// title, if it shows it in the window.
func revealRowOf(h *harness, title string) (ui.Rect, bool) {
	for _, x := range h.tt.AccessAll() {
		if x.Role != ui.RoleButton && x.Role != ui.RoleListItem {
			continue
		}
		if x.Name != title && !strings.HasPrefix(x.Name, title+" ") {
			continue
		}
		if !x.Offscreen && x.Bounds.X >= 0 && x.Bounds.X < h.a.sidebarWidth && x.Bounds.W > 0 && x.Bounds.H > 0 {
			return x.Bounds, true
		}
	}
	return ui.Rect{}, false
}

// revealNative turns the built-in sidebar off, for Glitched's own.
func revealNative(t *testing.T, h *harness) {
	t.Helper()
	if err := h.a.plugins.SetEnabled("sidebar", false); err != nil {
		t.Fatal(err)
	}
	h.a.pluginsChanged(nil)
}

// A child opened under its open parent scrolls into view, in the
// built-in sidebar and Glitched's own.
func TestTheSidebarScrollsToAChildOpenedUnderItsParent(t *testing.T) {
	for _, layout := range []string{"builtin", "native"} {
		t.Run(layout, func(t *testing.T) {
			h := start(t, pagedDir(t, pagedBatch{name: "Parent", n: 1}, pagedBatch{name: "Child", parent: "Parent 000", n: 55}))
			parent := h.thread("Parent 000")
			h.until("the children", func() bool { return len(h.a.kids[parent.id()]) == 55 })
			old := h.thread("Child 003")
			h.a.childrenOpen[parent.id()] = true
			if layout == "native" {
				revealNative(t, h)
			}
			revealSettle(t, h)
			if _, ok := revealRowOf(h, "Child 054"); !ok {
				t.Fatal("the fixture didn't show the opened parent's children")
			}
			if _, ok := revealRowOf(h, old.View.Title); ok {
				t.Fatal("the old child was already visible")
			}
			revealOpen(t, h, old)
			if r, ok := revealRowOf(h, old.View.Title); !ok {
				t.Errorf("%s: opened the old child but its expanded sidebar row stayed offscreen: %v", layout, r)
			}
		})
	}
}

// A thread opened while its project is filtered out doesn't scroll the
// sidebar to it once the filter clears, after the user went on to a new
// thread.
func TestANewThreadCancelsTheSidebarsScrollToAThreadFilteredOut(t *testing.T) {
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(false)))
	for i := range 60 {
		seed(t, d, "/work/hidden", fmt.Sprintf("Filtered %03d", i))
	}
	seed(t, d, "/work/shown", "Newer visible project")
	h := start(t, dir)
	old := h.thread("Filtered 003")
	shown := h.thread("Newer visible project")
	raw, _ := json.Marshal(map[string][]string{"keys": {h.a.projectOf(shown).key.String()}})
	if _, err := h.a.filterEffect(raw); err != nil {
		t.Fatal(err)
	}
	revealOpen(t, h, old)
	if _, ok := revealRowOf(h, old.View.Title); ok {
		t.Fatal("the target isn't filtered out")
	}
	h.click("New thread")
	revealSettle(t, h)
	if !h.a.current().id().IsZero() {
		t.Fatal("New thread did not select a draft")
	}
	if _, err := h.a.filterEffect(json.RawMessage(`{"keys":[]}`)); err != nil {
		t.Fatal(err)
	}
	revealSettle(t, h)
	if _, ok := revealRowOf(h, old.View.Title); ok {
		t.Error("clearing the filter jumped to the old thread after switching to a new draft")
	}
	if _, ok := revealRowOf(h, shown.View.Title); !ok {
		t.Error("the formerly visible top row disappeared")
	}
}

// A thread opened from the palette scrolls the sidebar to its row, the
// built-in's and Glitched's own.
func TestTheSidebarScrollsToAThreadOpenedFromThePalette(t *testing.T) {
	for _, builtin := range []bool{true, false} {
		h := start(t, pagedDir(t, pagedBatch{name: "Paged", n: 60}))
		h.until("the threads", func() bool { return len(h.a.itemsIn(catInbox)) == 60 })
		if !builtin {
			if err := h.a.plugins.SetEnabled("sidebar", false); err != nil {
				t.Fatal(err)
			}
			h.a.pluginsChanged(nil)
		}
		settlePlugins(t, h.a, h.tt, h.p)
		inSidebar := func(title string) bool {
			for _, x := range h.tt.AccessAll() {
				if strings.HasPrefix(x.Name, title+" ") && x.Bounds.X < 270 && x.Bounds.Y >= 0 && x.Bounds.Y+x.Bounds.H <= 860 {
					return true
				}
			}
			return false
		}
		if inSidebar("Paged 003") {
			t.Fatal("the sidebar shows Paged 003 before it is opened")
		}
		h.a.openCommandPalette()
		paletteShows(t, h)
		h.tt.Type("Paged 003")
		paletteShows(t, h)
		h.tt.Key(0, ui.KeyEnter)
		settlePlugins(t, h.a, h.tt, h.p)
		h.tt.Frame()
		h.tt.Frame()
		if h.a.current().View.Title != "Paged 003" || !inSidebar("Paged 003") {
			t.Errorf("built-in %v: the sidebar doesn't show Paged 003, opened from the palette", builtin)
		}
	}
}

// revealReplacement replaces the sidebar with a list of yours, keyed by
// keyExpr, each item naming the thread threadExpr.
func revealReplacement(t *testing.T, h *harness, keyExpr, threadExpr string) {
	t.Helper()
	writeOwnPlugin(t, h.a, "mine", sidebarManifest, fmt.Sprintf(`
export function place({items}) {
  return items.map(({context}) => ({
    type: "list", id: "rows", label: "Replacement sidebar", grow: true, noKeys: true,
    rowHeight: 32, items: context.sections.flatMap(s => s.rows).filter(r => r.thread).map(r => ({
      key: %s, thread: %s, label: r.title
    }))
  }));
}
export function rows({keys, place}) {
  const labels = new Map(place.context.sections.flatMap(s => s.rows).filter(r => r.thread).map(r => [%s, r.title]));
  return keys.map(key => ({type: "text", text: labels.get(key)}));
}
`, keyExpr, threadExpr, keyExpr))
	h.a.plugins.SetGranted("mine", "ui.replace", true)
	h.a.plugins.SetGranted("mine", "threads:read", true)
	revealSettle(t, h)
	s := h.a.plugins.App("mine")
	if s == nil || s.Err != nil || s.PlaceView("body", "inbox") == nil {
		t.Fatalf("the valid replacement didn't draw: %v", s)
	}
	if _, ok := h.tt.AccessByRole(ui.RoleList, "Replacement sidebar"); !ok {
		t.Fatal("the rendered sidebar didn't use the replacement")
	}
}

// Your sidebar scrolls to the row whose item names the thread opened,
// whatever its key.
func TestYourSidebarScrollsToTheRowThatNamesTheThreadOpened(t *testing.T) {
	h := start(t, pagedDir(t, pagedBatch{name: "Custom row", n: 60}))
	old := h.thread("Custom row 003")
	h.until("the custom rows", func() bool { return len(h.a.itemsIn(catInbox)) == 60 })
	revealReplacement(t, h, `"copy:" + r.thread`, `r.thread`)
	if _, ok := revealRowOf(h, old.View.Title); ok {
		t.Fatal("fixture already shows the old row")
	}
	revealOpen(t, h, old)
	if _, ok := revealRowOf(h, old.View.Title); !ok {
		t.Error("threads.open did not reveal the replacement row that names the thread")
	}
}
