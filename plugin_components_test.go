package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"glitched/internal/plugins"
	"glitched/internal/plugins/pack"
)

// writeOwnPlugin writes a plugin of the user's into the app's plugins
// folder, signed as theirs, and reloads.
func writeOwnPlugin(t *testing.T, a *app, folder, manifest, js string) {
	t.Helper()
	dst := filepath.Join(plugins.Dir(a.dir), folder)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"plugin.json": manifest, "index.js": js} {
		if err := os.WriteFile(filepath.Join(dst, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	key, err := pack.LoadKey(a.dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := pack.Sign(dst, key); err != nil {
		t.Fatal(err)
	}
	a.plugins.Reload()
	a.pluginsChanged(nil)
}

func replacing(id, at string) string {
	return `{"id": "` + id + `", "name": "` + id + `", "main": "index.js", "contract": "glitched", "contractVersion": 2,
	"capabilities": ["ui.place", "ui.replace"], "glitched": {"places": [{"id": "row", "at": "` + at + `", "replaces": true}]}}`
}

const mineModule = `export function place({ items }) {
	return items.map(() => ({ type: "button", id: "mine", label: "Mine" }))
}`

const brokenModule = `export function place() { throw new Error("broken on purpose") }`

// The header's buttons are the built-in Header actions plugin's, till a
// plugin of yours replaces them; yours failing brings the built-in back,
// as does turning it off. A copy of the built-in runs in its place, and
// Revert to built-in brings the built-in back.
func TestTheHeaderActionsAreABuiltInPluginYoursReplaces(t *testing.T) {
	a := offline(t)
	tt := newTester(t, a, 1320, 860)
	shows := func(label string) bool {
		settleTester(tt)
		_, ok := tt.Find(label)
		return ok
	}
	if !shows("Commit & push") || !shows("Toggle diff  ⌘D") {
		t.Fatalf("the built-in doesn't draw the header's buttons: %q", tt.Texts())
	}

	writeOwnPlugin(t, a, "mine", replacing("mine", "thread.header.actions"), mineModule)
	if !shows("Commit & push") || shows("Mine") {
		t.Fatal("yours drew before it was granted ui.replace")
	}
	if err := a.plugins.SetGranted("mine", "ui.replace", true); err != nil {
		t.Fatal(err)
	}
	if !shows("Mine") || shows("Commit & push") {
		t.Fatalf("yours doesn't replace the built-in: %q", tt.Texts())
	}

	writeOwnPlugin(t, a, "mine", replacing("mine", "thread.header.actions"), brokenModule)
	if !shows("Commit & push") || shows("Mine") {
		t.Fatalf("a broken replacement left no built-in: %q", tt.Texts())
	}
	p := installed(a, "mine")
	if note := a.replaceNote(p, "thread.header.actions"); !strings.Contains(note, "failed, so the built-in draws it") || !strings.Contains(note, "broken on purpose") {
		t.Errorf("Settings says %q of the broken one", note)
	}
	if err := a.plugins.SetEnabled("mine", false); err != nil {
		t.Fatal(err)
	}

	copied, err := a.plugins.Copy("header-actions")
	if err != nil {
		t.Fatal(err)
	}
	js := filepath.Join(copied, "main.js")
	b, _ := os.ReadFile(js)
	os.WriteFile(js, []byte(strings.ReplaceAll(string(b), `"Commit & push"`, `"Ship it"`)), 0o644)
	a.plugins.Reload()
	a.pluginsChanged(nil)
	if !shows("Ship it") || shows("Commit & push") {
		t.Fatalf("your edited copy doesn't run in the built-in's place: %q", tt.Texts())
	}
	if err := a.plugins.Revert("header-actions"); err != nil {
		t.Fatal(err)
	}
	a.pluginsChanged(nil)
	if !shows("Commit & push") || shows("Ship it") {
		t.Fatalf("revert didn't bring the built-in back: %q", tt.Texts())
	}
}

func installed(a *app, id string) plugins.Plugin {
	for _, p := range a.plugins.Installed() {
		if p.ID == id && !p.Builtin {
			return p
		}
	}
	return plugins.Plugin{}
}

// A plugin that names an off-limits component doesn't load, whatever it
// is granted, and Settings says why.
func TestAPluginCantReplaceTheApprovalCard(t *testing.T) {
	a := offline(t)
	writeOwnPlugin(t, a, "fake-approvals", replacing("fake-approvals", "approval.card"), mineModule)
	a.plugins.SetGranted("fake-approvals", "ui.replace", true)
	a.plugins.Reload()
	a.router.Push("/settings/plugins")
	tt := newTester(t, a, 1320, 2000)
	settleTester(tt)
	if !tt.HasText("approval.card, which is off limits to plugins") {
		t.Fatalf("Settings doesn't say why: %q", tt.Texts())
	}
}
