package main

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/daemon"
	"glitched/internal/provider/fake"
)

const sidebarManifest = `{"id": "mine", "name": "mine", "main": "index.js", "contract": "glitched", "contractVersion": 2,
	"capabilities": ["ui.place", "ui.replace", "threads:read"], "glitched": {"places": [{"id": "body", "at": "sidebar.body", "replaces": true}]}}`

// The sidebar's threads are the built-in Sidebar plugin's, till a plugin
// of yours replaces them; yours throwing, answering null or failing to
// draw its rows brings the built-in back, and with the built-in off
// Glitched draws its own.
func TestTheSidebarIsABuiltInPluginYoursReplaces(t *testing.T) {
	h := start(t, inboxDir(t))
	h.thread("SIP re-INVITE handling")
	shows := func(text string) bool {
		settlePlugins(t, h.a, h.tt, h.p)
		return h.tt.HasText(text)
	}
	// A row of yours fails only once it is asked for, a frame or so after
	// the list it is in shows.
	builtin := func() bool {
		for range 40 {
			app := h.a.plugins.App("sidebar")
			if shows("SIP re-INVITE handling") && app != nil && app.PlaceView("body", "inbox") != nil {
				return true
			}
			h.tt.Advance(50 * time.Millisecond)
		}
		return false
	}
	if !builtin() {
		t.Fatalf("the built-in doesn't draw the sidebar's threads: %q", h.tt.Texts())
	}

	writeOwnPlugin(t, h.a, "mine", sidebarManifest, mineModule)
	h.a.plugins.SetGranted("mine", "ui.replace", true)
	h.a.plugins.SetGranted("mine", "threads:read", true)
	if !shows("Mine") || shows("SIP re-INVITE handling") {
		t.Fatalf("yours doesn't replace the built-in: %q", h.tt.Texts())
	}
	for name, js := range map[string]string{
		"throws": brokenModule,
		"null":   `export function place({ items }) { return items.map(() => null) }`,
		"rows": `export function place({ items }) {
	return items.map(() => ({ type: "list", id: "l", label: "Mine", height: 100, items: [{ key: "a", label: "Mine" }] }))
}
export function rows() { throw new Error("broken on purpose") }`,
	} {
		writeOwnPlugin(t, h.a, "mine", sidebarManifest, js)
		if !builtin() {
			t.Fatalf("yours that %s left no built-in: %q", name, h.tt.Texts())
		}
	}

	h.a.plugins.SetEnabled("mine", false)
	if err := h.a.plugins.SetEnabled("sidebar", false); err != nil {
		t.Fatal(err)
	}
	h.a.pluginsChanged(nil)
	if !shows("SIP re-INVITE handling") || len(h.a.plugins.Replacers("sidebar.body")) > 0 {
		t.Fatalf("with no plugin to draw them, Glitched's own threads don't show: %q", h.tt.Texts())
	}
}

// The built-in's rows do what the sidebar's own did: a click shows the
// thread, its composer taking the focus, and the arrow keys move nothing;
// pointed at, a row's snooze and settle take its time's place, and stay
// while its popover of times is open; its menu is the thread's.
func TestTheSidebarsRowsKeepTheirWays(t *testing.T) {
	h := start(t, inboxDir(t))
	const sip = "SIP re-INVITE handling"
	th := h.thread(sip)
	settlePlugins(t, h.a, h.tt, h.p)
	h.tt.Advance(time.Second) // the rows folding in

	h.click(sip)
	h.tt.Advance(time.Second)
	if h.a.selected != th.id() {
		t.Fatalf("a click on the row didn't show its thread")
	}
	h.tt.Type("hello")
	if h.a.draft != "hello" {
		t.Errorf("typed after the click, the draft is %q: the thread didn't give its composer the focus", h.a.draft)
	}
	for _, k := range []ui.Key{ui.KeyDown, ui.KeyUp, ui.KeyEnd} {
		h.tt.Key(0, k)
		h.tt.Frame()
		if h.a.selected != th.id() {
			t.Fatalf("%v moved the sidebar's choice", k)
		}
	}

	h.tt.HoldModifiers(ui.Cmd)
	h.tt.Frame()
	if _, ok := h.tt.Find("⌘"); !ok {
		t.Errorf("⌘ held shows no keycaps beside the plugin's rows")
	}
	if !h.tt.HasText(sip) {
		t.Errorf("⌘ held hid the rows: %q", h.tt.Texts())
	}
	h.tt.HoldModifiers(0)
	h.tt.Frame()
	if _, ok := h.tt.Find("⌘"); ok {
		t.Errorf("the keycaps stay after ⌘ is let go")
	}

	r, _ := h.tt.Find(sip)
	h.tt.Move(r.X+r.W/2, r.Y+r.H/2)
	h.tt.Advance(time.Second)
	if !h.tt.HasText("Snooze") || !h.tt.HasText("Settle") {
		t.Fatalf("the row pointed at shows no actions: %q", h.tt.Texts())
	}
	h.click("Snooze")
	settlePlugins(t, h.a, h.tt, h.p)
	h.tt.Advance(time.Second)
	if !h.tt.HasText("Tomorrow") {
		t.Fatalf("Snooze opened no times: %q", h.tt.Texts())
	}
	h.tt.Move(900, 500)
	settlePlugins(t, h.a, h.tt, h.p)
	h.tt.Advance(time.Second)
	if !h.tt.HasText("Snooze") {
		t.Fatalf("the actions went while their popover is open: %q", h.tt.Texts())
	}
	h.tt.Key(0, ui.KeyEscape)
	h.tt.Advance(time.Second)

	if err := h.tt.RightClick(sip); err != nil {
		t.Fatal(err)
	}
	if m := h.tt.Menu(); !slices.Contains(m, "Rename") || !slices.Contains(m, "Snooze") {
		t.Fatalf("the row's menu %q isn't the thread's", m)
	}
}

// Each row is a button named by what it shows, in the sidebar's order,
// and each part of the sidebar a list named for its sections.
func TestTheSidebarsRowsAreNamedInOrder(t *testing.T) {
	h := start(t, inboxDir(t))
	h.thread("SIP re-INVITE handling")
	settlePlugins(t, h.a, h.tt, h.p)
	h.tt.Advance(time.Second) // the rows folding in
	inbox, ok := h.tt.AccessByRole(ui.RoleList, "Inbox")
	if !ok {
		t.Fatal("the inbox isn't a list named Inbox")
	}
	var rows []string
	for _, x := range h.tt.AccessAll() {
		if x.Role == ui.RoleButton && x.Bounds.X < inbox.Bounds.X+inbox.Bounds.W && x.Bounds.Y >= inbox.Bounds.Y && x.Bounds.Y < inbox.Bounds.Y+inbox.Bounds.H {
			rows = append(rows, x.Name)
		}
	}
	// Its title, and how long ago it changed, but while it's unread.
	var want []string
	for _, it := range h.a.itemsIn(catInbox) {
		name := title(it.t)
		if !h.a.backend.Unread(it.t.Thread) {
			name += " " + ago(it.t.View.UpdatedAt, h.a.now)
		}
		want = append(want, name)
	}
	if !slices.Equal(rows, want) {
		t.Errorf("the rows read %q, want %q", rows, want)
	}
}

// A thread's title, however long, is no part of its row's version, which
// is at most 256 bytes: one long title leaves every thread in the inbox.
func TestTheSidebarShowsEveryThreadWhateverItsTitle(t *testing.T) {
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(false)))
	long := "Long title: " + strings.Repeat("練習", 41)
	seed(t, d, "/work/mygo", long)
	seed(t, d, "/work/mygo", "A thread with a short title")
	h := start(t, dir)
	h.thread(long)
	h.thread("A thread with a short title")
	settlePlugins(t, h.a, h.tt, h.p)
	h.tt.Advance(time.Second)
	if err := h.a.plugins.App("sidebar").Err; err != nil {
		t.Fatalf("the built-in failed: %v", err)
	}
	if _, ok := h.tt.AccessByRole(ui.RoleList, "Inbox"); !ok || !h.tt.HasText("A thread with a short title") {
		t.Fatalf("the inbox lost a thread to another's long title: %q", h.tt.Texts())
	}
}

// A copy of yours that fails to draw the threads as they are now gives
// way to Glitched's own, never to its drawing of the threads as they were.
func TestTheSidebarNeverShowsAFailedCopysOldThreads(t *testing.T) {
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(false)))
	seed(t, d, "/work/mygo", "The first thread")
	h := start(t, dir)
	h.thread("The first thread")
	writeOwnPlugin(t, h.a, "mine", sidebarManifest, `export function place({ items }) {
	return items.map(({ context }) => {
		const n = context.sections.reduce((n, s) => n + s.rows.length, 0)
		return { type: "text", text: n > 1 ? 42 : "Mine shows " + n }
	})
}`)
	h.a.plugins.SetGranted("mine", "ui.replace", true)
	h.a.plugins.SetGranted("mine", "threads:read", true)
	if err := h.a.plugins.SetEnabled("sidebar", false); err != nil {
		t.Fatal(err)
	}
	h.a.pluginsChanged(nil)
	settlePlugins(t, h.a, h.tt, h.p)
	if !h.tt.HasText("Mine shows 1") {
		t.Fatalf("yours doesn't draw the sidebar: %q", h.tt.Texts())
	}
	seed(t, d, "/work/mygo", "The second thread")
	h.thread("The second thread")
	settlePlugins(t, h.a, h.tt, h.p)
	h.tt.Advance(time.Second)
	if h.tt.HasText("Mine shows 1") || !h.tt.HasText("The first thread") || !h.tt.HasText("The second thread") {
		t.Fatalf("a failed copy's old drawing stayed: %q", h.tt.Texts())
	}
}

// Your plugin loaded again draws its list, though its last load drew as
// many views of something else: its controls are its own, not the last
// load's.
func TestTheSidebarYoursDrawsItsListLoadedAgain(t *testing.T) {
	h := start(t, inboxDir(t))
	h.thread("SIP re-INVITE handling")
	writeOwnPlugin(t, h.a, "mine", sidebarManifest, mineModule)
	h.a.plugins.SetGranted("mine", "ui.replace", true)
	h.a.plugins.SetGranted("mine", "threads:read", true)
	settlePlugins(t, h.a, h.tt, h.p)
	if !h.tt.HasText("Mine") {
		t.Fatalf("yours doesn't draw the sidebar: %q", h.tt.Texts())
	}
	rev := h.a.plugins.App("mine").Rev
	writeOwnPlugin(t, h.a, "mine", sidebarManifest, `export function place({ items }) {
	return items.map(() => ({ type: "list", id: "l", label: "Mine", height: 100, items: [{ key: "a", label: "Row a" }] }))
}
export function rows({ keys }) { return keys.map(() => ({ type: "text", text: "Row a drawn" })) }`)
	settlePlugins(t, h.a, h.tt, h.p)
	if got := h.a.plugins.App("mine").Rev; got != rev {
		t.Skipf("the new load's view is %d, the last's %d: not the case this is about", got, rev)
	}
	h.tt.Advance(time.Second)
	settlePlugins(t, h.a, h.tt, h.p)
	if !h.tt.HasText("Row a drawn") {
		t.Fatalf("yours loaded again draws no list: %q", h.tt.Texts())
	}
}
