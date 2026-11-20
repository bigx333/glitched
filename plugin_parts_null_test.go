package main

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/plugins"
)

const nullRows = `export function place({items}) { return items.map(() => null) }`

func allow(t *testing.T, a *app, id, capability string) {
	t.Helper()
	if err := a.plugins.SetGranted(id, capability, true); err != nil {
		t.Fatal(err)
	}
}

func turnOff(t *testing.T, a *app, id string) {
	t.Helper()
	if err := a.plugins.SetEnabled(id, false); err != nil {
		t.Fatal(err)
	}
}

// partsOfGlitched opens Settings › Plugins › Parts of Glitched in a
// window of its own.
func partsOfGlitched(t *testing.T, a *app) *ui.Tester {
	t.Helper()
	a.router.Push("/settings/plugins")
	a.pluginsView = 1
	tt := ui.NewTester(a.view, 1320, 2100)
	if !tt.HasText("PARTS A PLUGIN MAY DRAW") {
		t.Fatalf("Parts of Glitched didn't open: %q", tt.Texts())
	}
	return tt
}

// A part whose only plugin answers null for it has no drawer in Parts of
// Glitched.
func TestPartsNamesNoDrawerOfAPartAnsweredNull(t *testing.T) {
	h, _ := timelineHarness(t)
	a, tt := h.a, h.tt
	const id, part = "my-no-header", "thread.header.actions"
	turnOff(t, a, "header-actions")
	writeOwnPlugin(t, a, id, replacing(id, part), nullRows)
	allow(t, a, id, "ui.replace")
	settlePlugins(t, a, tt, h.p)
	if !tt.HasText(turnText(1)) || tt.HasText("Commit & push") {
		t.Fatal("the thread is not showing with the header actions absent")
	}
	for k := range a.placeControls {
		if k.plugin == id || k.plugin == "header-actions" {
			t.Fatalf("unexpected header renderer %+v", k)
		}
	}
	if st := a.partState(installed(a, id), part); st.why != "It answered null for every copy shown, and nothing draws it." {
		t.Errorf("Settings says %q of header buttons nothing draws", st.why)
	}
	parts := partsOfGlitched(t, a)
	_, link := parts.Access("Show " + id + ", which draws Thread header buttons")
	if link {
		t.Error("Parts links the all-null replacement as drawing, while the header buttons are absent")
	}
}

// A panel closed on the thread shown leaves no null behind it in Settings.
func TestAClosedPanelLeavesNoNullInSettings(t *testing.T) {
	h, th := checked(t)
	h.click("Edit " + sidebarPath)
	h.diffsRead()
	a, tt := h.a, h.tt
	const id, part = "my-null-diff", "thread.panel.diff"
	manifest := strings.Replace(replacing(id, part), `"ui.replace"]`, `"ui.replace","threads:read"]`, 1)
	writeOwnPlugin(t, a, id, manifest, nullRows)
	allow(t, a, id, "ui.replace")
	allow(t, a, id, "threads:read")
	settlePlugins(t, a, tt, h.p)
	if !a.showDiff || !tt.HasText("Changes") {
		t.Fatal("the built-in diff did not appear")
	}
	if st := a.partState(installed(a, id), part); st.word != "Answered null" {
		t.Fatalf("Settings says %q of the open diff answered null", st.word)
	}
	a.showDiff = false
	tt.Advance(time.Second)
	settlePlugins(t, a, tt, h.p)
	if a.selected != th.id() || tt.HasText("Changes") {
		t.Fatalf("the diff did not close on the same thread: selected %s, texts %q", a.selected, tt.Texts())
	}
	parts := partsOfGlitched(t, a)
	after := a.partState(installed(a, id), part)
	row := id + ": It answered null for every copy shown, so the built-in draws it."
	if after.word == "Answered null" || parts.HasText(row) {
		t.Error("a closed diff panel still reports null for the copies supposedly shown")
	}
}

const rowsOfB = `{type:"column",children:[{type:"text",text:"Replacement marker from B"},{type:"native",id:"body",name:"transcript.body"}]}`

// Turning a plugin off in Settings stops Settings naming it as who draws
// what another answered null for.
func TestAPluginTurnedOffInSettingsIsNoLongerNamedAsDrawing(t *testing.T) {
	h, _ := timelineHarness(t)
	a := h.a
	const part, id, backup = "timeline.message", "a-null", "b-backup"
	writeOwnPlugin(t, a, id, replacing(id, part), nullRows)
	writeOwnPlugin(t, a, backup, replacing(backup, part), rowsModule(rowsOfB))
	allow(t, a, id, "ui.replace")
	allow(t, a, backup, "ui.replace")
	settlePlugins(t, a, h.tt, h.p)
	if !h.tt.HasText("Replacement marker from B") {
		t.Fatal("B never drew the timeline")
	}
	parts := partsOfGlitched(t, a)
	line := id + ": It answered null for every copy shown, so b-backup draws it."
	if !parts.HasText(line) {
		t.Fatalf("Parts doesn't say b-backup draws what a-null answered null for: %q", parts.Texts())
	}
	a.showPlugin(installed(a, backup).Folder)
	settlePlugins(t, a, parts, h.p)
	if err := parts.Click("Enable " + backup); err != nil {
		t.Fatal(err)
	}
	settlePlugins(t, a, parts, h.p)
	off := a.partState(installed(a, backup), part).word == "Off"
	if !off {
		t.Fatal("the Settings switch did not disable B")
	}
	if err := parts.Click("Parts of Glitched"); err != nil {
		t.Fatal(err)
	}
	settlePlugins(t, a, parts, h.p)
	after := a.partState(installed(a, id), part)
	if parts.HasText(line) || strings.Contains(after.why, "b-backup draws it") {
		t.Error("the null explanation still says the just-disabled plugin draws")
	}
	a.router.Push("/")
	settlePlugins(t, a, h.tt, h.p)
	if h.tt.HasText("Replacement marker from B") || !h.tt.HasText(turnText(1)) {
		t.Error("did not see the signed fallback draw the thread again")
	}
}

// A plugin named Glitched is still a plugin, not Glitched's own drawing.
func TestAPluginNamedGlitchedIsNotGlitchedsOwnRow(t *testing.T) {
	h, _ := timelineHarness(t)
	a := h.a
	const part, id, backup = "timeline.message", "a-null", "b-backup"
	turnOff(t, a, "timeline")
	writeOwnPlugin(t, a, id, replacing(id, part), nullRows)
	writeOwnPlugin(t, a, backup, strings.Replace(replacing(backup, part), `"name": "b-backup"`, `"name": "Glitched"`, 1), rowsModule(rowsOfB))
	allow(t, a, id, "ui.replace")
	allow(t, a, backup, "ui.replace")
	settlePlugins(t, a, h.tt, h.p)
	if !h.tt.HasText("Replacement marker from B") || installed(a, backup).Name != "Glitched" {
		t.Fatal("the user plugin named Glitched did not draw")
	}
	st := a.partState(installed(a, id), part)
	parts := partsOfGlitched(t, a)
	if _, link := parts.Access("Show Glitched, which draws Timeline rows"); !link {
		t.Error("Parts doesn't name the plugin called Glitched as drawing the rows")
	}
	if st.why != "It answered null for every copy shown, so Glitched draws it." {
		t.Errorf("Settings says %q of rows the plugin called Glitched draws", st.why)
	}
}

// A plugin loaded again leaves no null of its last load in Settings.
func TestAPluginLoadedAgainLeavesNoNullOfItsLastLoad(t *testing.T) {
	h, _ := timelineHarness(t)
	a := h.a
	const id, part = "a-timeline-null", "timeline.message"
	manifest := replacing(id, part)
	writeOwnPlugin(t, a, id, manifest, nullRows)
	allow(t, a, id, "ui.replace")
	settlePlugins(t, a, h.tt, h.p)
	if !h.tt.HasText(turnText(1)) {
		t.Fatal("the selected timeline did not draw")
	}
	old := a.plugins.App(id)
	parts := partsOfGlitched(t, a)
	line := id + ": It answered null for every copy shown, so the built-in draws it."
	if !parts.HasText(line) {
		t.Fatalf("Parts doesn't say the rows were answered null: %q", parts.Texts())
	}
	writeOwnPlugin(t, a, id, manifest, rowsModule(rowsOfB))
	fresh := a.plugins.App(id)
	if old == nil || fresh == nil || fresh == old || !a.plugins.Allowed(id, "ui.replace") {
		t.Fatalf("reloading did not replace the plugin session under the same id and grant")
	}
	settlePlugins(t, a, parts, h.p)
	after := a.partState(installed(a, id), part)
	if parts.HasText(line) || after.word == "Answered null" {
		t.Error("Settings still attributes old null answers to the newly loaded plugin")
	}
	a.router.Push("/")
	settlePlugins(t, a, h.tt, h.p)
	if !h.tt.HasText("Replacement marker from B") || !h.tt.HasText(turnText(1)) {
		t.Fatal("the new plugin did not draw when chat returned")
	}
	if a.partState(installed(a, id), part).word != "Drawing it now" {
		t.Error("the new drawing's status did not settle")
	}
}

// A plugin that answered null for every copy doesn't hide a failed one
// whose last drawing stays.
func TestANullPluginDoesNotHideAFailedOnesLastDrawing(t *testing.T) {
	h, _ := timelineHarness(t)
	a, tt := h.a, h.tt
	const top, backup, part = "a-null", "b-cached", "thread.header.actions"
	turnOff(t, a, "header-actions")
	writeOwnPlugin(t, a, top, replacing(top, part), nullRows)
	writeOwnPlugin(t, a, backup, replacing(backup, part), `
export function init() { return {state:false} }
export function update({event,state}) { return {state:state || event.kind === "timer"} }
export function place({state,items}) {
	if (state) throw new Error("header threw after a real view")
	return items.map(() => ({type:"text",text:"Cached header from B"}))
}`)
	allow(t, a, top, "ui.replace")
	allow(t, a, backup, "ui.replace")
	settlePlugins(t, a, tt, h.p)
	if !tt.HasText("Cached header from B") {
		t.Fatal("B has no last-good header")
	}
	a.plugins.App(backup).Notify(plugins.AppEvent{Kind: "timer", ID: "throw-with-header"})
	settlePlugins(t, a, tt, h.p)
	err := a.plugins.Failed(backup)
	if err == nil || !strings.Contains(err.Error(), "header threw after a real view") || !tt.HasText("Cached header from B") {
		t.Fatalf("the failed B did not retain its last drawing: err=%v, marker=%v", err, tt.HasText("Cached header from B"))
	}
	st := a.partState(installed(a, top), part)
	drawer, drawn := a.partDrawer(part)
	parts := partsOfGlitched(t, a)
	_, link := parts.Access("Show b-cached, which draws Thread header buttons")
	if st.why != "It answered null for every copy shown, so b-cached draws it." {
		t.Errorf("Settings says %q of the header b-cached still draws", st.why)
	}
	if !drawn || drawer.ID != backup || !link {
		t.Errorf("Parts doesn't name b-cached, whose last header stays: %q (%v), linked %v", drawer.ID, drawn, link)
	}
}

// A plugin granted ahead of the one drawing stops Settings naming the old
// drawer for what another answered null for.
func TestAGrantAheadOfTheDrawerExpiresWhoSettingsNames(t *testing.T) {
	h, _ := timelineHarness(t)
	a := h.a
	const part, top, higher, former = "timeline.message", "a-null", "b-new", "c-former"
	writeOwnPlugin(t, a, top, replacing(top, part), nullRows)
	writeOwnPlugin(t, a, higher, replacing(higher, part), rowsModule(rowsOfB))
	writeOwnPlugin(t, a, former, replacing(former, part), rowsModule(strings.Replace(rowsOfB, "Replacement marker from B", "Former drawer marker", 1)))
	allow(t, a, top, "ui.replace")
	allow(t, a, former, "ui.replace")
	settlePlugins(t, a, h.tt, h.p)
	if !h.tt.HasText("Former drawer marker") || h.tt.HasText("Replacement marker from B") {
		t.Fatal("the granted lower-priority fixture did not draw ahead of the ungranted one")
	}
	topSession, formerSession := a.plugins.App(top), a.plugins.App(former)
	parts := partsOfGlitched(t, a)
	const line = "a-null: It answered null for every copy shown, so c-former draws it."
	if !parts.HasText(line) {
		t.Fatalf("Parts doesn't say c-former draws what a-null answered null for: %q", parts.Texts())
	}
	a.showPlugin(installed(a, higher).Folder)
	settlePlugins(t, a, parts, h.p)
	if err := parts.Click("Grant ui.replace to " + higher); err != nil {
		t.Fatal(err)
	}
	settlePlugins(t, a, parts, h.p)
	if !a.plugins.Allowed(higher, "ui.replace") || a.plugins.App(top) != topSession || a.plugins.App(former) != formerSession {
		t.Fatal("the Settings grant did not add B while keeping the two earlier sessions")
	}
	if err := parts.Click("Parts of Glitched"); err != nil {
		t.Fatal(err)
	}
	settlePlugins(t, a, parts, h.p)
	_, newLink := parts.Access("Show b-new, which draws Timeline rows")
	if newLink && parts.HasText(line) {
		t.Error("Parts names B as the drawer but A's null diagnostic still claims C draws the same timeline")
	}
	a.router.Push("/")
	settlePlugins(t, a, h.tt, h.p)
	if !h.tt.HasText("Replacement marker from B") || h.tt.HasText("Former drawer marker") {
		t.Error("the newly granted B did not win when the real timeline was rendered")
	}
	if st := a.partState(installed(a, top), part); st.why != "It answered null for every copy shown, so b-new draws it." {
		t.Errorf("once the rows drew again, Settings says %q", st.why)
	}
}

// A failed plugin that never drew isn't named as drawing; the one whose
// last drawing stays is.
func TestAFailedPluginThatNeverDrewIsNotNamed(t *testing.T) {
	h, _ := timelineHarness(t)
	a, tt := h.a, h.tt
	const part, top, blank, cached = "thread.header.actions", "a-null", "b-never-drew", "c-cached"
	turnOff(t, a, "header-actions")
	writeOwnPlugin(t, a, top, replacing(top, part), nullRows)
	writeOwnPlugin(t, a, blank, replacing(blank, part), brokenModule)
	writeOwnPlugin(t, a, cached, replacing(cached, part), `
export function init() { return {state:false} }
export function update({event,state}) { return {state:state || event.kind === "timer"} }
export function place({state,items}) {
	if (state) throw new Error("failed with a header drawing")
	return items.map(() => ({type:"text",text:"Header from C's cache"}))
}`)
	for _, id := range []string{top, blank, cached} {
		allow(t, a, id, "ui.replace")
	}
	settlePlugins(t, a, tt, h.p)
	if a.plugins.Failed(blank) == nil || !tt.HasText("Header from C's cache") {
		t.Fatal("B never failed or C did not establish a visible drawing")
	}
	s := a.plugins.App(cached)
	s.Notify(plugins.AppEvent{Kind: "timer", ID: "fail-after-cached-header"})
	settlePlugins(t, a, tt, h.p)
	if a.plugins.Failed(cached) == nil || !tt.HasText("Header from C's cache") {
		t.Fatal("the failed C's last drawing was not visible")
	}
	st := a.partState(installed(a, top), part)
	parts := partsOfGlitched(t, a)
	_, b := parts.Access("Show b-never-drew, which draws Thread header buttons")
	_, c := parts.Access("Show c-cached, which draws Thread header buttons")
	if st.why != "It answered null for every copy shown, so c-cached draws it." {
		t.Errorf("Settings says %q of the header c-cached still draws", st.why)
	}
	if b || !c {
		t.Errorf("Parts links b-never-drew %v and c-cached %v; only c-cached draws", b, c)
	}
}
