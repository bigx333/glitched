package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// General and Appearance are drawn by the built-in Settings pages, which
// set what you choose through the app; yours replaces them once granted
// ui.replace and settings:write, and a broken one brings them back.
func TestGeneralAndAppearanceAreBuiltInPluginsYoursReplace(t *testing.T) {
	a := offline(t)
	tt := newTester(t, a, 1320, 860)
	open := func(path string) {
		a.router.Push(path)
		settleTester(tt)
	}
	open("/settings/general")
	if err := tt.Click("Stream assistant messages"); err != nil {
		t.Fatal(err)
	}
	settleTester(tt)
	if !a.settings.streaming {
		t.Fatal("the built-in General's switch didn't set streaming")
	}
	open("/settings/appearance")
	if err := tt.Click("Mocha"); err != nil {
		t.Fatal(err)
	}
	settleTester(tt)
	if err := tt.Click(accents[2].name); err != nil {
		t.Fatal(err)
	}
	settleTester(tt)
	if a.settings.theme != "Mocha" || a.settings.accent != 2 {
		t.Fatalf("the built-in Appearance chose %q and accent %d, want Mocha and %d", a.settings.theme, a.settings.accent, 2)
	}
	if !tt.HasText("Liquid Glass") {
		t.Fatalf("the material isn't drawn under the plugin's part: %q", tt.Texts())
	}

	manifest := `{"id": "mine", "name": "mine", "main": "index.js", "contract": "glitched", "contractVersion": 2,
		"capabilities": ["ui.place", "ui.replace", "settings:write"], "glitched": {"places": [
			{"id": "general", "at": "settings.page.general", "replaces": true},
			{"id": "appearance", "at": "settings.page.appearance", "replaces": true}]}}`
	writeOwnPlugin(t, a, "mine", manifest, `export function place({ at, items }) {
	return items.map(() => ({ type: "button", id: "b", label: "Mine: " + at, onClick: "go" }))
}
export async function update({ event }) {
	if (event.kind === "ui") await glitched.app.settings.set("theme", "Dawn")
	return {}
}`)
	if err := a.plugins.SetGranted("mine", "ui.replace", true); err != nil {
		t.Fatal(err)
	}
	open("/settings/general")
	if tt.HasText("Mine: settings.page.general") || !tt.HasText("Stream assistant messages") {
		t.Fatalf("yours drew General without settings:write: %q", tt.Texts())
	}
	if err := a.plugins.SetGranted("mine", "settings:write", true); err != nil {
		t.Fatal(err)
	}
	for path, at := range map[string]string{"/settings/general": "settings.page.general", "/settings/appearance": "settings.page.appearance"} {
		open(path)
		if !tt.HasText("Mine: "+at) || tt.HasText("Stream assistant messages") || tt.HasText("Accent color") {
			t.Errorf("%s: yours doesn't replace the built-in: %q", path, tt.Texts())
		}
	}
	if !tt.HasText("Liquid Glass") {
		t.Errorf("the material went with the built-in Appearance: %q", tt.Texts())
	}
	if err := tt.Click("Mine: settings.page.appearance"); err != nil {
		t.Fatal(err)
	}
	settleTester(tt)
	if a.settings.theme != "Dawn" {
		t.Errorf("your plugin's set chose %q, want Dawn", a.settings.theme)
	}

	writeOwnPlugin(t, a, "mine", manifest, brokenModule)
	open("/settings/appearance")
	if !tt.HasText("Accent color") {
		t.Fatalf("a broken replacement left no built-in: %q", tt.Texts())
	}
}

// app.settings.set takes a setting the pages could have chosen and
// refuses any other, or a setting a plugin doesn't set.
func TestAppSettingsAreSetOnlyToWhatThePagesOffer(t *testing.T) {
	a := offline(t)
	set := func(key string, value any) error {
		b, _ := json.Marshal(map[string]any{"key": key, "value": value})
		_, err := a.setAppSetting(b)
		return err
	}
	for _, ok := range []struct {
		key   string
		value any
	}{{"theme", "Dark"}, {"accent", accents[1].name}, {"newThread", "New worktree"}, {"timestamps", "24-hour"}, {"confirmDelete", false}, {"streaming", true}, {"notifications", false}, {"defaultModel", modelNames()[1]}} {
		if err := set(ok.key, ok.value); err != nil {
			t.Errorf("%s = %v: %v", ok.key, ok.value, err)
		}
	}
	got := a.appSettings()
	if got.Theme != "Dark" || got.Accent != accents[1].name || got.NewThread != "New worktree" || got.Timestamps != "24-hour" || got.ConfirmDelete || !got.Streaming || got.Notifications || got.DefaultModel != modelNames()[1] {
		t.Errorf("after the sets, the settings are %+v", got)
	}
	for _, bad := range []struct {
		key   string
		value any
		want  string
	}{{"theme", "Neon", "can't be"}, {"accent", "Plaid", "can't be"}, {"timestamps", 12, "timestamps"}, {"confirmDelete", "no", "confirmDelete"}, {"fontSize", 14, "no setting a plugin sets"}, {"claudePath", "/bin/sh", "no setting a plugin sets"}, {"defaultModel", "GPT-1", "defaultModel is one of"}} {
		if err := set(bad.key, bad.value); err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("%s = %v: %v, want %q", bad.key, bad.value, err, bad.want)
		}
	}
	if a.appSettings() != got {
		t.Errorf("a refused set changed the settings: %+v", a.appSettings())
	}
}

const neonManifest = `{"id": "neon", "name": "neon", "main": "index.js", "contract": "glitched", "contractVersion": 2, "glitched": {"themes": [
	{"name": "Neon", "dark": true, "colors": {"bg": "#05050a", "sidebar": "#0a0a14", "surface": "#10101e", "hover": "#ffffff0d", "active": "#ffffff17",
		"border": "#ffffff14", "text": "#f0f0ff", "muted": "#a0a0c0", "faint": "#9090b0", "green": "#39ff14", "red": "#ff3131", "amber": "#ffd300", "blue": "#00e5ff"}}]}}`

// A plugin's theme is one more look in Appearance, which colors the app
// as the app's own do; it goes with the plugin.
func TestAPluginAddsATheme(t *testing.T) {
	a := offline(t)
	tt := newTester(t, a, 1320, 860)
	writeOwnPlugin(t, a, "neon", neonManifest, "")
	a.router.Push("/settings/appearance")
	settleTester(tt)
	if err := tt.Click("Neon"); err != nil {
		t.Fatalf("Appearance doesn't offer the plugin's theme: %v, %q", err, tt.Texts())
	}
	settleTester(tt)
	if a.settings.theme != "Neon" || a.theme.name != "Neon" || palette.bg != ui.Hex("#05050a") || !a.theme.dark {
		t.Fatalf("choosing Neon chose %q, colored by %q", a.settings.theme, a.theme.name)
	}
	if err := a.plugins.SetEnabled("neon", false); err != nil {
		t.Fatal(err)
	}
	a.pluginsChanged(nil)
	settleTester(tt)
	if tt.HasText("Neon") || a.theme.name == "Neon" {
		t.Errorf("the theme stayed once its plugin was off: %q", a.theme.name)
	}
}

// settings.json choosing a plugin's theme, which isn't there as the app
// starts, gets it once the plugin runs.
func TestAPluginsThemeChosenBeforeItLoadsIsShownOnceItDoes(t *testing.T) {
	first := offline(t)
	writeOwnPlugin(t, first, "neon", neonManifest, "")
	dir := first.dir
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"theme": "Neon"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &posts{wake: make(chan struct{}, 1)}
	a := newApp(dir, nil, p.post)
	offlinePosts.Store(a, p)
	t.Cleanup(func() { offlinePosts.Delete(a) })
	tt := newTester(t, a, 1320, 860)
	settleTester(tt)
	if a.settings.theme != "Neon" || a.theme.name != "Neon" {
		t.Fatalf("the app started on %q, colored by %q, want Neon", a.settings.theme, a.theme.name)
	}
}

// A plugin's settings page is listed in Settings, not over it, and opens
// there.
func TestAPluginsSettingsPageIsInSettings(t *testing.T) {
	a := offline(t)
	tt := newTester(t, a, 1320, 860)
	writeOwnPlugin(t, a, "prefs", `{"id": "prefs", "name": "Prefs", "main": "index.js", "contract": "glitched", "contractVersion": 2,
		"contributes": {"pages": [{"id": "home", "title": "Prefs page"}]}, "glitched": {"settingsPages": ["home"]}}`,
		`export function view() { return { subtitle: "Mine to set.", body: { type: "text", text: "Prefs body" } } }`)
	a.router.Push("/")
	settleTester(tt)
	if tt.HasText("Prefs page") {
		t.Fatalf("the settings page is listed over Settings: %q", tt.Texts())
	}
	a.router.Push("/settings/general")
	settleTester(tt)
	if err := tt.Click("Prefs page"); err != nil {
		t.Fatalf("Settings doesn't list it: %v, %q", err, tt.Texts())
	}
	settleTester(tt)
	if a.router.Path() != "/settings/plugin/prefs/home" || !tt.HasText("Prefs body") || !tt.HasText("Mine to set.") {
		t.Fatalf("at %s it shows %q", a.router.Path(), tt.Texts())
	}
}
