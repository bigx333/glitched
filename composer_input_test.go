package main

import (
	"strings"
	"testing"
)

const composerManifest = `{"id": "my-composer", "name": "My composer", "main": "index.js", "contract": "glitched", "contractVersion": 2,
	"capabilities": ["ui.place", "ui.replace"], "glitched": {"places": [{"id": "composer", "at": "composer", "replaces": true}]}}`

func composerModule(body string) string {
	return `export function place({ items }) { return items.map(() => (` + body + `)) }`
}

// The composer is the built-in Composer plugin's, around the native
// composer.input. Yours replaces it once granted ui.replace, and may put
// widgets around composer.input; one that leaves it out, places it twice,
// draws nothing or fails gives way to Glitched's own composer.
func TestTheComposerIsABuiltInPluginYoursCanWrap(t *testing.T) {
	a := offline(t)
	tt := newTester(t, a, 1320, 860)
	shows := func(label string) bool {
		settleTester(tt)
		_, ok := tt.Find(label)
		return ok
	}
	composer := func() bool { return shows("Send") && shows("Access") && shows("Model") }
	if !composer() {
		t.Fatalf("no composer: %q", tt.Texts())
	}
	if drawer, drawn := a.partDrawer("composer"); !drawn || drawer.ID != "composer" || !drawer.Builtin {
		t.Fatalf("the composer isn't the built-in Composer's: %+v %v", drawer, drawn)
	}

	wrap := composerModule(`{ type: "column", children: [{ type: "text", text: "Mine above" }, { type: "native", id: "input", name: "composer.input" }] }`)
	writeOwnPlugin(t, a, "my-composer", composerManifest, wrap)
	if shows("Mine above") || !composer() {
		t.Fatal("yours drew the composer before it was granted ui.replace")
	}
	if err := a.plugins.SetGranted("my-composer", "ui.replace", true); err != nil {
		t.Fatal(err)
	}
	if !shows("Mine above") || !composer() {
		t.Fatalf("yours doesn't draw around composer.input: %q", tt.Texts())
	}

	for name, body := range map[string]string{
		"leaves it out":                  `{ type: "text", text: "Mine above" }`,
		"hides it in a closed dialog":    `{ type: "column", children: [{ type: "text", text: "Mine above" }, { type: "dialog", id: "d", title: "Waiting", children: [{ type: "native", id: "input", name: "composer.input" }] }] }`,
		"adds one in a popover":          `{ type: "column", children: [{ type: "text", text: "Mine above" }, { type: "native", id: "a", name: "composer.input" }, { type: "button", id: "more", style: "ghost", icon: "plus", tooltip: "More", open: true, popover: { type: "column", width: 400, children: [{ type: "native", id: "b", name: "composer.input" }] } }] }`,
		"adds one in an item's trailing": `{ type: "column", children: [{ type: "text", text: "Mine above" }, { type: "native", id: "a", name: "composer.input" }, { type: "item", id: "row", icon: "plus", text: "Row", trailing: [{ type: "native", id: "b", name: "composer.input" }] }] }`,
		"places it twice":                `{ type: "column", children: [{ type: "text", text: "Mine above" }, { type: "native", id: "a", name: "composer.input" }, { type: "native", id: "b", name: "composer.input" }] }`,
	} {
		writeOwnPlugin(t, a, "my-composer", composerManifest, composerModule(body))
		if shows("Mine above") || !composer() {
			t.Errorf("a layout that %s drew in place of Glitched's composer: %q", name, tt.Texts())
		}
		if note := a.partState(installed(a, "my-composer"), "composer").why; !strings.Contains(note, "places composer.input once") || !strings.Contains(note, "the built-in draws it") {
			t.Errorf("Settings says %q of a layout that %s", note, name)
		}
	}
	for name, js := range map[string]string{
		"throws":        brokenModule,
		"draws nothing": `export function place({ items }) { return items.map(() => null) }`,
	} {
		writeOwnPlugin(t, a, "my-composer", composerManifest, js)
		if !composer() {
			t.Errorf("a composer that %s left none: %q", name, tt.Texts())
		}
	}

	writeOwnPlugin(t, a, "my-composer", composerManifest, wrap)
	if !shows("Mine above") {
		t.Fatalf("yours doesn't draw again: %q", tt.Texts())
	}
	if err := a.plugins.SetEnabled("composer", false); err != nil {
		t.Fatal(err)
	}
	if err := a.plugins.SetEnabled("my-composer", false); err != nil {
		t.Fatal(err)
	}
	if shows("Mine above") || !composer() {
		t.Fatalf("with no Composer plugin on, Glitched's composer doesn't draw: %q", tt.Texts())
	}
}
