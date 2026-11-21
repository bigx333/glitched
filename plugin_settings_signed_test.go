package main

import (
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/egoist/mygo/ui"

	"glitched/internal/plugins"
	"glitched/internal/plugins/bundled"
)

// The ui.replace row warns that what a plugin draws is the plugin's, not
// Glitched's: a built-in Glitched's release key signed draws Glitched's
// own, so its row isn't listed, and it still draws.
func TestUIReplaceIsListedOnlyForAPluginGlitchedDidntSign(t *testing.T) {
	const warning = "What you see in those parts is the plugin's, not Glitched's."
	builtin := func(t *testing.T, a *app, id string) plugins.Plugin {
		t.Helper()
		for _, p := range a.plugins.Installed() {
			if p.ID == id && p.Builtin {
				return p
			}
		}
		t.Fatalf("no built-in %s", id)
		return plugins.Plugin{}
	}
	open := func(t *testing.T, a *app, p plugins.Plugin) *ui.Tester {
		t.Helper()
		a.router.Push("/settings/plugins")
		a.showPlugin(p.Folder)
		tt := newTester(t, a, 1320, 3000)
		settleTester(tt)
		return tt
	}
	listed := func(tt *ui.Tester, name string) bool {
		_, ok := tt.Access("Grant ui.replace to " + name)
		return ok
	}

	t.Run("a signed built-in", func(t *testing.T) {
		a := offline(t)
		p := builtin(t, a, "greetings")
		if p.Trust != plugins.TrustBuiltin {
			t.Fatalf("greetings isn't a signed built-in: %+v", p)
		}
		tt := open(t, a, p)
		if listed(tt, "Greetings") || tt.HasText(warning) {
			t.Errorf("Glitched's own greetings list ui.replace: %q", tt.Texts())
		}
		if !tt.HasText("WHAT IT MAY DO") || !tt.HasText("Without asking, as any plugin may:") {
			t.Errorf("what it may do without asking went with the row: %q", tt.Texts())
		}
		if !a.plugins.Allowed("greetings", "ui.replace") {
			t.Error("the built-in lost ui.replace with its row")
		}
		if s := pluginSummary(p); s != "Draws New thread greeting, Empty thread" {
			t.Errorf("its summary counts the grant it doesn't list: %q", s)
		}
	})

	t.Run("a copy of it", func(t *testing.T) {
		a := offline(t)
		if _, err := a.plugins.Copy("greetings"); err != nil {
			t.Fatal(err)
		}
		a.plugins.Reload()
		a.pluginsChanged(nil)
		p := installed(a, "greetings")
		if p.Trust == plugins.TrustBuiltin {
			t.Fatalf("the copy reads as the built-in: %+v", p)
		}
		if tt := open(t, a, p); !listed(tt, "Greetings") || !tt.HasText(warning) {
			t.Errorf("the copy doesn't list ui.replace: %q", tt.Texts())
		}
		if !a.plugins.Allowed("greetings", "ui.replace") {
			t.Error("the copy didn't keep the built-in's ui.replace")
		}
	})

	t.Run("a plugin of yours", func(t *testing.T) {
		a := offline(t)
		writeOwnPlugin(t, a, "hello", `{"id": "hello", "name": "Hello", "version": "0.1.0", "main": "index.js", "contract": "glitched", "contractVersion": 2, "capabilities": ["ui.place", "ui.replace"], "glitched": {"places": [{"id": "hero", "at": "hero.heading", "replaces": true}]}}`, "export function place() { return null }")
		if err := a.plugins.SetGranted("hello", "ui.replace", true); err != nil {
			t.Fatal(err)
		}
		a.pluginsChanged(nil)
		if tt := open(t, a, installed(a, "hello")); !listed(tt, "Hello") || !tt.HasText(warning) {
			t.Errorf("a plugin of yours doesn't list ui.replace: %q", tt.Texts())
		}
	})

	t.Run("a built-in that fails its signature", func(t *testing.T) {
		was := bundled.FS
		t.Cleanup(func() { bundled.FS = was })
		changed := fstest.MapFS{}
		fs.WalkDir(was, ".", func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			b, err := fs.ReadFile(was, path)
			if path == "greetings/main.js" {
				b = append(b, "\n// changed\n"...)
			}
			changed[path] = &fstest.MapFile{Data: b, Mode: 0o644}
			return err
		})
		bundled.FS = changed
		a := offline(t)
		p := builtin(t, a, "greetings")
		if p.Trust != plugins.TrustTampered {
			t.Fatalf("the changed greetings: %+v", p)
		}
		if tt := open(t, a, p); !listed(tt, "Greetings") || !tt.HasText(warning) {
			t.Errorf("the changed built-in doesn't list ui.replace: %q", tt.Texts())
		}
	})

	t.Run("a built-in whose ui.replace you took back, granted again", func(t *testing.T) {
		a := offline(t)
		if err := a.plugins.SetGranted("palette", "ui.replace", false); err != nil {
			t.Fatal(err)
		}
		a.pluginsChanged(nil)
		tt := open(t, a, builtin(t, a, "palette"))
		if !listed(tt, "Command palette") {
			t.Fatal("no switch to grant it back")
		}
		if err := tt.Click("Grant ui.replace to Command palette"); err != nil {
			t.Fatal(err)
		}
		settleTester(tt)
		tt.Key(0, ui.KeySpace)
		settleTester(tt)
		if !a.plugins.Allowed("palette", "ui.replace") || !a.plugins.Allowed("palette", "commands") {
			t.Errorf("granting ui.replace back took another grant: ui.replace %v, commands %v", a.plugins.Allowed("palette", "ui.replace"), a.plugins.Allowed("palette", "commands"))
		}
		if listed(tt, "Command palette") {
			t.Error("granted again, it's still listed")
		}
	})
}
