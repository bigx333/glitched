package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const barManifest = `{"id": "my-bar", "name": "My bar", "main": "index.js", "contract": "glitched", "contractVersion": 2,
	"capabilities": ["ui.place", "ui.replace"], "glitched": {"places": [{"id": "bar", "at": "message.toolbar", "replaces": true}]}}`

func barModule(body string) string {
	return `export function place({ items }) { return items.map(({ key, context }) => (` + body + `)) }`
}

// A message's toolbar is the built-in Message toolbar's. Yours replaces it
// once granted ui.replace, and may reorder, drop or add to its actions,
// but never do more than they do: Edit & resend stays a message of
// yours', Retry a reply's, and a plugin's command one that is there. One
// that fails or answers null gives way to Glitched's own toolbar.
func TestTheMessageToolbarIsABuiltInPluginYoursCanReplace(t *testing.T) {
	h, _ := checked(t)
	const msg, reply = "Sort them by most recent activity.", "One test fails."
	settle := func() { settlePlugins(t, h.a, h.tt, h.p) }
	point := func(at string) {
		h.tt.Move(800, 30)
		h.tt.Advance(time.Second)
		h.hover(at)
		h.tt.Advance(time.Second)
		settle()
	}
	settle()
	if drawer, drawn := h.a.partDrawer("message.toolbar"); !drawn || drawer.ID != "message-toolbar" || !drawer.Builtin {
		t.Fatalf("the toolbar isn't the built-in Message toolbar's: %+v %v", drawer, drawn)
	}
	point(msg)
	if !h.tt.HasText("Copy") || !h.tt.HasText("Edit & resend") {
		t.Fatalf("the built-in shows no Copy and Edit & resend: %q", h.tt.Texts())
	}

	writeOwnPlugin(t, h.a, "my-bar", barManifest, barModule(`{ type: "row", gap: 6, children: [{ type: "text", text: "Mine " + context.role }, { type: "native", id: "copy", name: "message.copy" }] }`))
	allow(t, h.a, "my-bar", "ui.replace")
	point(msg)
	if !h.tt.HasText("Mine user") || !h.tt.HasText("Copy") || h.tt.HasText("Edit & resend") {
		t.Fatalf("yours doesn't draw the toolbar: %q", h.tt.Texts())
	}
	h.click("Copy")
	if h.tt.Clipboard() != msg {
		t.Errorf("Copy in yours copied %q", h.tt.Clipboard())
	}

	writeOwnPlugin(t, h.a, "my-bar", barManifest, barModule(`{ type: "row", children: [{ type: "text", text: "Mine " + context.role }, { type: "native", id: "edit", name: "message.edit" }, { type: "native", id: "retry", name: "message.retry" }, { type: "native", id: "item", name: "message.item", props: { index: 3 } }] }`))
	point(reply)
	if !h.tt.HasText("Mine assistant") || h.tt.HasText("Edit & resend") || h.tt.HasText("Quote") {
		t.Errorf("yours did more for a reply than its toolbar does: %q", h.tt.Texts())
	}
	point(msg)
	if !h.tt.HasText("Mine user") || h.tt.HasText("Retry") {
		t.Errorf("yours did more for a message of yours than its toolbar does: %q", h.tt.Texts())
	}

	for name, js := range map[string]string{
		"throws":       brokenModule,
		"answers null": `export function place({ items }) { return items.map(() => null) }`,
	} {
		writeOwnPlugin(t, h.a, "my-bar", barManifest, js)
		point(msg)
		if h.tt.HasText("Mine user") || !h.tt.HasText("Copy") || !h.tt.HasText("Edit & resend") {
			t.Errorf("a toolbar that %s left Glitched's out: %q", name, h.tt.Texts())
		}
		if why := h.a.partState(installed(h.a, "my-bar"), "message.toolbar").why; why == "" {
			t.Errorf("Settings says nothing of a toolbar that %s", name)
		}
	}
}

// Glitched draws the built-in toolbar's own drawing while the plugin has
// drawn none: its main.js and messageToolbarRow draw every toolbar alike,
// a plugin's commands in it too.
func TestGlitchedWaitsForTheBuiltInToolbarWithItsOwnDrawing(t *testing.T) {
	h, th := checked(t)
	writeOwnPlugin(t, h.a, "mine", menusManifest, menusModule)
	allow(t, h.a, "mine", "threads:read")
	settlePlugins(t, h.a, h.tt, h.p)
	roles := map[string]bool{}
	for _, e := range entriesOf(th) {
		if e.kind != entryUser && e.kind != entryAssistant {
			continue
		}
		got := h.a.plugins.App("message-toolbar").PlaceView("bar", e.key)
		if got == nil {
			continue
		}
		m := messageToolbar{Key: e.key, Role: "assistant", Items: 1}
		if e.kind == entryUser {
			m.Role = "user"
		}
		roles[m.Role] = true
		want, _ := json.Marshal(messageToolbarRow(m))
		have, _ := json.Marshal(got)
		if string(have) != string(want) {
			t.Errorf("the built-in draws the %s toolbar %s as %s, Glitched as %s", m.Role, e.key, have, want)
		}
		if !strings.Contains(string(have), "message.item") {
			t.Errorf("the %s toolbar has no plugin command: %s", m.Role, have)
		}
	}
	if !roles["user"] || !roles["assistant"] {
		t.Fatalf("the built-in drew toolbars for only %v", roles)
	}
}

// With the built-in off, a toolbar of yours that fails after it drew
// gives way to Glitched's own, not to its last drawing.
func TestAToolbarThatFailsGivesWayToGlitchedsOwn(t *testing.T) {
	h, _ := checked(t)
	const msg = "Sort them by most recent activity."
	turnOff(t, h.a, "message-toolbar")
	writeOwnPlugin(t, h.a, "my-bar", barManifest, `let n = 0
export function place({ items }) {
	if (items.some(({ context }) => context.items) && ++n) throw new Error("no room for commands")
	return items.map(({ context }) => ({ type: "row", children: [{ type: "text", text: "Mine " + context.role }, { type: "native", id: "copy", name: "message.copy" }] }))
}`)
	allow(t, h.a, "my-bar", "ui.replace")
	point := func() {
		h.tt.Move(800, 30)
		h.tt.Advance(time.Second)
		h.hover(msg)
		h.tt.Advance(time.Second)
		settlePlugins(t, h.a, h.tt, h.p)
	}
	point()
	if !h.tt.HasText("Mine user") {
		t.Fatalf("yours doesn't draw the toolbar: %q", h.tt.Texts())
	}
	writeOwnPlugin(t, h.a, "mine", menusManifest, menusModule)
	point()
	if h.tt.HasText("Mine user") || !h.tt.HasText("Copy") || !h.tt.HasText("Edit & resend") {
		t.Fatalf("a toolbar that failed left Glitched's out: %q", h.tt.Texts())
	}
	if h.a.plugins.Failed("my-bar") == nil {
		t.Errorf("my-bar didn't fail")
	}
	if why := h.a.partState(installed(h.a, "my-bar"), "message.toolbar").why; !strings.Contains(why, "Glitched draws its own") {
		t.Errorf("Settings says %q of a toolbar that failed with the built-in off", why)
	}
}

// message.item draws a command only at a whole-number index one has:
// one past them, below 0, between two or none draws nothing.
func TestAToolbarsCommandIsOnlyOneThatIsThere(t *testing.T) {
	h, _ := checked(t)
	const msg = "Sort them by most recent activity."
	writeOwnPlugin(t, h.a, "mine", menusManifest, menusModule)
	for _, tc := range []struct {
		props string
		quote bool
	}{
		{", props: { index: 0 }", true},
		{", props: { index: 1 }", false},
		{", props: { index: -1 }", false},
		{", props: { index: 0.5 }", false},
		{", props: { index: 1e30 }", false},
		{"", false},
	} {
		writeOwnPlugin(t, h.a, "my-bar", barManifest, barModule(`{ type: "row", children: [{ type: "text", text: "Mine" }, { type: "native", id: "item", name: "message.item"`+tc.props+` }] }`))
		allow(t, h.a, "my-bar", "ui.replace")
		h.tt.Move(800, 30)
		h.tt.Advance(time.Second)
		h.hover(msg)
		h.tt.Advance(time.Second)
		settlePlugins(t, h.a, h.tt, h.p)
		if !h.tt.HasText("Mine") || h.tt.HasText("Quote") != tc.quote {
			t.Errorf("message.item%s: Quote shown %v, want %v: %q", tc.props, h.tt.HasText("Quote"), tc.quote, h.tt.Texts())
		}
	}
}
