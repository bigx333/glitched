package main

import (
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"glitched/internal/plugins"
	"glitched/internal/plugins/pack"
)

// TestPluginsShot writes PNGs of Settings › Plugins to the folder PSHOT
// names, one per state, theme and window size: PSTATE picks states
// (comma separated, all without it).
func TestPluginsShot(t *testing.T) {
	out := os.Getenv("PSHOT")
	if out == "" {
		t.Skip()
	}
	os.MkdirAll(out, 0o755)
	want := os.Getenv("PSTATE")
	for _, state := range []string{"builtins", "mixed", "thirty"} {
		if want != "" && !strings.Contains(","+want+",", ","+state+",") {
			continue
		}
		a := offline(t)
		pluginShotState(t, a, state)
		// The app's own window first, so that a plugin that fails as it
		// draws there has failed.
		tt := newTester(t, a, 1320, 860)
		settleTester(tt)
		a.router.Push("/settings/plugins")
		for _, v := range []struct {
			name  string
			w, h  int
			light bool
			glass bool
			open  string
		}{
			{"dark", 1320, 860, false, true, ""},
			{"light", 1320, 860, true, true, ""},
			{"dark-open", 1320, 860, false, true, "release-captain"},
			{"dark-narrow", 760, 860, false, true, ""},
			{"dark-narrow-open", 760, 860, false, true, "release-captain"},
			{"dark-tall", 1320, 3400, false, true, ""},
			{"dark-tall-open", 1320, 5200, false, true, "all"},
			{"dark-parts", 1320, 1500, false, true, "parts"},
			{"dark-narrow-parts", 760, 860, false, true, "parts"},
			{"light-open", 1320, 860, true, true, "release-captain"},
			{"dark-noglass-open", 1320, 860, false, false, "release-captain"},
		} {
			a.pluginsView = 0
			if v.open == "parts" {
				a.pluginsView = 1
			}
			for _, p := range a.plugins.Installed() {
				a.pluginsOpen[p.Folder] = v.open == "all" || slices.Contains(strings.Split(v.open, ","), filepath.Base(p.Folder))
			}
			a.settings.theme, a.settings.glass = darkTheme.name, v.glass
			if v.light {
				a.settings.theme = lightTheme.name
			}
			tt.SetSize(v.w, v.h)
			settleTester(tt)
			tt.Advance(time.Second)
			f, err := os.Create(filepath.Join(out, fmt.Sprintf("%s-%s.png", state, v.name)))
			if err != nil {
				t.Fatal(err)
			}
			png.Encode(f, tt.Image())
			f.Close()
		}
	}
}

// pluginShotState installs what a state shows into the app's plugins
// folder.
func pluginShotState(t *testing.T, a *app, state string) {
	t.Helper()
	if state == "builtins" {
		return
	}
	dir := plugins.Dir(a.dir)
	raw := func(folder, manifest, js string) string {
		dst := filepath.Join(dir, folder)
		os.MkdirAll(dst, 0o755)
		os.WriteFile(filepath.Join(dst, "plugin.json"), []byte(manifest), 0o644)
		os.WriteFile(filepath.Join(dst, "index.js"), []byte(js), 0o644)
		return dst
	}
	if state == "thirty" {
		for i := range 24 {
			id := fmt.Sprintf("tool-%02d", i)
			caps := [][]string{{"clipboard"}, {"ui.place", "notify"}, {"threads:read", "ui.place"}, {"threads:read", "threads:write", "ui.place", "ui.replace"}}[i%4]
			writeOwnPlugin(t, a, id, fmt.Sprintf(`{"id": %q, "name": "Tool %02d", "version": "0.%d.0", "description": "One of many small plugins of yours.", "main": "index.js", "contract": "glitched", "contractVersion": 2, "capabilities": ["%s"]}`, id, i, i, strings.Join(caps, `", "`)), "export function view() {}")
		}
		return
	}
	if _, err := a.plugins.Copy("greetings"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile("examples/plugins/focus-timer/plugin.json")
	js, _ := os.ReadFile("examples/plugins/focus-timer/index.js")
	writeOwnPlugin(t, a, "focus-timer", string(b), string(js))
	writeOwnPlugin(t, a, "release-captain", `{"id": "release-captain", "name": "Release captain for the whole monorepo and its satellites", "version": "2.14.0-beta.3", "description": "Watches your release branches, drafts the notes, opens the thread that cuts the release and tells the channel when it ships.", "main": "index.js", "contract": "glitched", "contractVersion": 2,
		"capabilities": ["clipboard", "notify", "composer", "threads:read", "threads:write", "messages:read", "ui.place", "ui.replace", "tools", "programs", "service", "settings:write"],
		"permissions": {"network": ["api.github.com", "hooks.slack.com"]},
		"glitched": {"programs": ["git", "gh"], "places": [{"id": "actions", "at": "thread.header.actions", "replaces": true}, {"id": "about", "at": "settings.page.about", "replaces": true}]}}`,
		`export function place() { return [] }`)
	writeOwnPlugin(t, a, "broken-loader", `{"id": "broken-loader", "name": "Broken loader", "version": "0.0.1", "main": "index.js", "contract": "glitched", "contractVersion": 2, "capabilities": ["ui.place", "ui.replace"], "glitched": {"places": [{"id": "card", "at": "approval.card", "replaces": true}]}}`, "export function place() { return [] }")
	writeOwnPlugin(t, a, "thrower", `{"id": "thrower", "name": "Thrower", "version": "0.3.0", "description": "Draws the project strip, badly.", "main": "index.js", "contract": "glitched", "contractVersion": 2, "capabilities": ["ui.place", "ui.replace", "threads:read"], "glitched": {"places": [{"id": "strip", "at": "sidebar.projects", "replaces": true}]}}`,
		`export function place() { throw new Error("Cannot read properties of undefined (reading 'projects') at place (index.js:1:31) while drawing sidebar.projects for the window that has the long title") }`)
	a.plugins.SetGranted("thrower", "ui.replace", true)
	a.plugins.SetGranted("thrower", "threads:read", true)
	raw("scratchpad", `{"id": "scratchpad", "name": "Scratchpad", "version": "0.1.0", "description": "Something you started writing.", "main": "index.js", "contract": "glitched", "contractVersion": 2, "capabilities": ["clipboard"]}`, "export function view() {}")
	theirs, err := pack.LoadKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := pack.Sign(raw("linear-sync", `{"id": "linear-sync", "name": "Linear sync", "version": "1.2.0", "description": "Shows the Linear issue of each thread.", "main": "index.js", "contract": "glitched", "contractVersion": 2, "capabilities": ["threads:read", "ui.place"], "permissions": {"network": ["api.linear.app"]}}`, "export function view() {}"), theirs); err != nil {
		t.Fatal(err)
	}
	a.plugins.Reload()
	a.pluginsChanged(nil)
}
