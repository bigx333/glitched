package main

import (
	"strings"
	"testing"
	"time"

	"glitched/internal/daemon"
	"glitched/internal/provider/fake"
)

// chromeModule replaces each component it is placed at with a line naming
// it.
const chromeModule = `export function place({ at, items }) {
	return items.map(() => ({ type: "text", text: "Mine: " + at }))
}`

// Settings' About and Keybindings pages and the hero's greeting are drawn
// by built-in plugins; yours replaces them once granted ui.replace, and a
// broken one brings the built-ins back.
func TestTheChromeIsBuiltInPluginsYoursReplace(t *testing.T) {
	a := offline(t)
	tt := newTester(t, a, 1320, 860)
	shows := func(text string) bool {
		settleTester(tt)
		_, ok := tt.Find(text)
		return ok
	}
	open := func(path string) {
		a.router.Push(path)
		settleTester(tt)
	}
	builtIn := map[string]string{"/": "What should we build?", "/settings/about": "Check for updates", "/settings/keybindings": "Toggle sidebar"}
	for path, text := range builtIn {
		open(path)
		if !shows(text) {
			t.Fatalf("%s: the built-in doesn't draw %q: %q", path, text, tt.Texts())
		}
	}

	manifest := `{"id": "mine", "name": "mine", "main": "index.js", "contract": "glitched", "contractVersion": 2,
		"capabilities": ["ui.place", "ui.replace"], "glitched": {"places": [
			{"id": "hero", "at": "hero.heading", "replaces": true},
			{"id": "about", "at": "settings.page.about", "replaces": true},
			{"id": "keys", "at": "settings.page.keybindings", "replaces": true}]}}`
	writeOwnPlugin(t, a, "mine", manifest, chromeModule)
	if err := a.plugins.SetGranted("mine", "ui.replace", true); err != nil {
		t.Fatal(err)
	}
	for path, component := range map[string]string{"/": "hero.heading", "/settings/about": "settings.page.about", "/settings/keybindings": "settings.page.keybindings"} {
		open(path)
		if !shows("Mine: "+component) || shows(builtIn[path]) {
			t.Errorf("%s: yours doesn't replace the built-in: %q", path, tt.Texts())
		}
	}

	writeOwnPlugin(t, a, "mine", manifest, brokenModule)
	for path, text := range builtIn {
		open(path)
		if !shows(text) {
			t.Errorf("%s: a broken replacement left no built-in: %q", path, tt.Texts())
		}
	}
}

// About's update check is the app's: the page says it runs, then a toast
// says how it went.
func TestAboutChecksForUpdates(t *testing.T) {
	a := offline(t)
	a.router.Push("/settings/about")
	tt := newTester(t, a, 1320, 860)
	settleTester(tt)
	if err := tt.Click("Check for updates"); err != nil {
		t.Fatal(err)
	}
	settleTester(tt)
	if !tt.HasText("Checking…") {
		t.Fatalf("the check doesn't show it runs: %q", tt.Texts())
	}
	tt.Advance(2 * time.Second)
	settleTester(tt)
	if !tt.HasText("You're up to date") || !tt.HasText("Check for updates") {
		t.Fatalf("the check doesn't say how it went: %q", tt.Texts())
	}
}

// A plugin of yours whose init waits on the app, which answers on the UI
// thread, doesn't hold the first frame: the built-in draws it, and yours
// takes over once it answered.
func TestAPluginWaitingOnTheAppDoesntHoldTheFirstFrame(t *testing.T) {
	a := offline(t)
	tt := newTester(t, a, 1320, 860)
	settleTester(tt)
	writeOwnPlugin(t, a, "slow", `{"id": "slow", "name": "slow", "main": "index.js", "contract": "glitched", "contractVersion": 2,
		"capabilities": ["ui.place", "ui.replace"], "glitched": {"places": [{"id": "about", "at": "settings.page.about", "replaces": true}]}}`,
		`export async function init() { await glitched.toast("Starting"); return { state: {} } }
export function place({ items }) { return items.map(() => ({ type: "text", text: "Slow about" })) }`)
	if err := a.plugins.SetGranted("slow", "ui.replace", true); err != nil {
		t.Fatal(err)
	}
	a.router.Push("/settings/about")
	start := time.Now()
	tt.Frame()
	if took := time.Since(start); took >= firstDrawWait {
		t.Errorf("the first frame took %v, waiting on a plugin that waits on it", took)
	}
	if !tt.HasText("Check for updates") || tt.HasText("Slow about") {
		t.Fatalf("the first frame shows %q, want the built-in's About", tt.Texts())
	}
	settleTester(tt)
	if !tt.HasText("Slow about") || !strings.Contains(strings.Join(tt.Texts(), "|"), "Starting") {
		t.Fatalf("yours doesn't take over once it answered: %q", tt.Texts())
	}
}

// A thread with no messages greets you with the built-in's line; yours
// replaces it, and a broken one brings it back.
func TestAnEmptyThreadsGreetingIsReplaceable(t *testing.T) {
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(checks))
	seed(t, d, gitWorkspace(t), "Empty one")
	h := start(t, dir)
	h.a.select_(h.thread("Empty one"))
	shows := func(text string) bool {
		settlePlugins(t, h.a, h.tt, h.p)
		_, ok := h.tt.Find(text)
		return ok
	}
	if !shows("Send a message to start the conversation.") {
		t.Fatalf("the built-in doesn't greet an empty thread: %q", h.tt.Texts())
	}
	manifest := `{"id": "mine", "name": "mine", "main": "index.js", "contract": "glitched", "contractVersion": 2,
		"capabilities": ["ui.place", "ui.replace"], "glitched": {"places": [{"id": "empty", "at": "thread.empty", "replaces": true}]}}`
	writeOwnPlugin(t, h.a, "mine", manifest, `export function place({ items }) {
	return items.map((it) => ({ type: "text", text: it.context && it.context.thread.id ? "Mine: a thread" : "Mine: none" }))
}`)
	if err := h.a.plugins.SetGranted("mine", "ui.replace", true); err != nil {
		t.Fatal(err)
	}
	if !shows("Mine: a thread") || shows("Send a message to start the conversation.") {
		t.Fatalf("yours doesn't replace the built-in: %q", h.tt.Texts())
	}
	writeOwnPlugin(t, h.a, "mine", manifest, brokenModule)
	if !shows("Send a message to start the conversation.") {
		t.Fatalf("a broken replacement left no built-in: %q", h.tt.Texts())
	}
}
