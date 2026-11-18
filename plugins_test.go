package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/plugins"
	"glitched/internal/plugins/pack"
)

// install copies examples/plugins/name into the state directory's plugins.
func install(t *testing.T, dir, name string) {
	t.Helper()
	src := filepath.Join("examples/plugins", name)
	dst := filepath.Join(plugins.Dir(dir), name)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	signAsOwn(t, dir, dst)
}

// signAsOwn signs the plugin in folder with the device's key of the
// state directory dir, as Glitched signs the plugins you write.
func signAsOwn(t *testing.T, dir, folder string) {
	t.Helper()
	key, err := pack.LoadKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := pack.Sign(folder, key); err != nil {
		t.Fatal(err)
	}
}

// pluginApp is the app, with no engine, on a state directory with the
// plugins installed.
func pluginApp(t *testing.T, names ...string) *harness {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		install(t, dir, n)
	}
	p := &posts{wake: make(chan struct{}, 1)}
	a := newApp(dir, nil, p.post)
	t.Cleanup(a.plugins.Close)
	h := &harness{t, a, ui.NewTester(a.view, 1320, 1000), p}
	h.tt.Frame()
	return h
}

// openPlugin opens the plugin's row in Settings › Plugins, and waits for
// its detail to grow into view.
func (h *harness) openPlugin(name string) {
	h.t.Helper()
	h.click(name)
	h.tt.Advance(300 * time.Millisecond)
}

// typeInto clicks the input under the label, and types.
func (h *harness) typeInto(label, text string) {
	h.t.Helper()
	r, ok := h.tt.Find(label)
	if !ok {
		h.t.Fatalf("no %q in %q", label, h.tt.Texts())
	}
	h.tt.ClickAt(r.X+20, r.Y+r.H-14) // the field: its label over its input
	h.tt.Type(text)
	h.tt.Frame()
}

func TestAPluginPageShowsAndItsCommandRuns(t *testing.T) {
	h := pluginApp(t, "hello")
	h.click("Hello")
	if h.a.router.Path() != "/plugins/hello/home" {
		t.Fatalf("path %q", h.a.router.Path())
	}
	h.until("the plugin's page", func() bool { return h.tt.HasText("Greeted 0 times") })
	for _, want := range []string{"A sample plugin's page, drawn by Glitched", "GREETINGS", "mygo-plugin", "Open"} {
		if !h.tt.HasText(want) {
			t.Errorf("no %q in %q", want, h.tt.Texts())
		}
	}
	h.click("Greet")
	h.until("the page read again after greet", func() bool { return h.tt.HasText("Greeted once") })
}

func TestACounterPluginDrawsNativeWidgetsAndTakesEvents(t *testing.T) {
	h := pluginApp(t, "counter")
	h.click("Counter")
	h.until("the counter", func() bool { return h.tt.HasText("Count: 0") })
	h.click("More")
	h.until("one", func() bool { return h.tt.HasText("Count: 1") })
	h.click("More")
	h.until("two", func() bool { return h.tt.HasText("Count: 2") })
	h.click("Less")
	h.until("one again", func() bool { return h.tt.HasText("Count: 1") })

	h.typeInto("Name", "Ada")
	h.until("the greeting", func() bool { return h.tt.HasText("Hello, Ada!") })
	if v := h.a.pluginControls[pluginKey{"counter", "home"}].Values()["name"]; v != "Ada" {
		t.Fatalf("the input holds %q", v)
	}
	if h.tt.HasText("The name has") {
		t.Fatal("details show before they're asked for")
	}
	h.click("Show details")
	h.until("the details", func() bool { return h.tt.HasText("The name has 3 characters.") })
	h.click("Show details")
	h.until("the details gone", func() bool { return !h.tt.HasText("The name has") })
	if !h.tt.HasText("Count: 1") || !h.tt.HasText("Hello, Ada!") {
		t.Fatalf("the state went: %q", h.tt.Texts())
	}

	// Away and back: the page keeps its state while the app runs.
	h.click("Settings")
	h.tt.Frame()
	h.click("Back to app")
	h.click("Counter")
	h.tt.Frame()
	if !h.tt.HasText("Count: 1") {
		t.Fatalf("back on the page: %q", h.tt.Texts())
	}
}

func TestASlowPluginLeavesTheWindowBe(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(plugins.Dir(dir), "slow")
	os.MkdirAll(dst, 0o755)
	os.WriteFile(filepath.Join(dst, "plugin.json"), []byte(`{"id": "slow", "name": "Slow", "main": "index.js", "contributes": {"pages": [{"id": "home", "title": "Slow"}]}}`), 0o644)
	os.WriteFile(filepath.Join(dst, "index.js"), []byte(`export function view() {
		const until = Date.now() + 2000; while (Date.now() < until) {}
		return { body: { type: "text", text: "done at last" } } }`), 0o644)
	signAsOwn(t, dir, dst)
	p := &posts{wake: make(chan struct{}, 1)}
	a := newApp(dir, nil, p.post)
	t.Cleanup(a.plugins.Close)
	h := &harness{t, a, ui.NewTester(a.view, 1320, 900), p}
	h.tt.Frame()
	h.click("Slow")
	start := time.Now()
	for range 5 {
		h.tt.Frame()
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("five frames took %v while the plugin worked", d)
	}
	if !h.tt.HasText("Loading…") {
		t.Fatalf("no loading state: %q", h.tt.Texts())
	}
	h.until("the slow view", func() bool { return h.tt.HasText("done at last") })
}

func TestSnippetsStartsAThreadOnceGranted(t *testing.T) {
	dir := tempDir(t)
	install(t, dir, "snippets")
	h := start(t, dir)
	h.tt.SetSize(1320, 1100)
	h.click("Snippets")
	h.until("the denial of threads:read", func() bool { return h.tt.HasText("Grant “threads:read” to Snippets") })

	h.click("New snippet")
	h.until("the form", func() bool { return h.tt.HasText("Prompt") })
	h.typeInto("Title", "Say hi")
	h.typeInto("Prompt", "Say hello to the user")
	h.click("Drafts it in the composer")
	h.tt.Frame()
	if err := h.tt.ChooseMenuItem("Starts a thread right away"); err != nil {
		h.click("Starts a thread right away")
	}
	h.click("Pin to the top")
	h.click("Save")
	h.until("the saved snippet", func() bool { return h.tt.HasText("Saved “Say hi”.") && h.tt.HasText("Pinned") })

	h.click("Run")
	h.until("the denial of threads:write", func() bool { return h.tt.HasText("Grant “threads:write” to Snippets") })
	if len(h.local().Threads) != 0 {
		t.Fatal("a thread started without the grant")
	}

	h.a.router.Push("/settings/plugins")
	h.tt.Frame()
	h.openPlugin("Snippets")
	for about, grant := range map[string]string{
		"Read your threads' titles, workspaces and status, and show a thread": "Grant threads:read to Snippets",
		"Start threads and send messages to them, which runs agents":          "Grant threads:write to Snippets",
	} {
		if !h.tt.HasText(about) {
			t.Fatalf("no %q in %q", about, h.tt.Texts())
		}
		h.click(grant)
		h.tt.Frame()
	}
	got := h.a.plugins.Installed()[0].Capabilities
	if !got[2].Granted || !got[3].Granted {
		t.Fatalf("grants %+v", got)
	}

	h.click("Back to app")
	h.click("Snippets")
	h.until("the threads, now granted", func() bool { return h.tt.HasText("No threads yet.") })
	h.click("Run")
	th := h.thread("Say hi")
	if th.View.Workspace != homeDir() {
		t.Fatalf("workspace %q", th.View.Workspace)
	}
	h.until("the thread in the page", func() bool {
		return h.tt.HasText("Started a thread.") && h.tt.HasText("Say hi") && h.tt.HasText("Open")
	})

	// The palette runs the plugin's commands: the pinned snippet goes to
	// the composer.
	for _, cmd := range h.a.commands() {
		if cmd.label == "Snippets: Draft the first pinned snippet" {
			cmd.run()
		}
	}
	h.until("the draft", func() bool { return h.a.draft == "Say hello to the user" })
	if h.a.router.Path() != "/" {
		t.Fatalf("path %q", h.a.router.Path())
	}
}

func TestABrokenPluginShowsWhyAndTheOthersRun(t *testing.T) {
	dir := t.TempDir()
	install(t, dir, "counter")
	dst := filepath.Join(plugins.Dir(dir), "broken")
	os.MkdirAll(dst, 0o755)
	os.WriteFile(filepath.Join(dst, "plugin.json"), []byte(`{"id": "broken", "name": "Broken", "main": "index.js", "contributes": {"pages": [{"id": "home", "title": "Broken"}]}}`), 0o644)
	os.WriteFile(filepath.Join(dst, "index.js"), []byte(`export function view() { return { body: { type: "script", text: "alert(1)" } } }`), 0o644)
	signAsOwn(t, dir, dst)
	p := &posts{wake: make(chan struct{}, 1)}
	a := newApp(dir, nil, p.post)
	t.Cleanup(a.plugins.Close)
	h := &harness{t, a, ui.NewTester(a.view, 1320, 900), p}
	h.tt.Frame()
	h.click("Broken")
	h.until("why", func() bool { return h.tt.HasText(`unknown widget type "script"`) })
	if !h.tt.HasText("Start over") {
		t.Fatalf("no way to start over: %q", h.tt.Texts())
	}
	h.click("Counter")
	h.until("the counter", func() bool { return h.tt.HasText("Count: 0") })

	// Off in Settings: its page leaves the sidebar, and comes back on.
	h.a.router.Push("/settings/plugins")
	h.tt.Frame()
	if !h.tt.HasText("Broken") || !h.tt.HasText("Counter") {
		t.Fatalf("settings: %q", h.tt.Texts())
	}
	h.click("Enable Broken")
	h.tt.Frame()
	if a.plugins.Installed()[0].Enabled {
		t.Fatal("still enabled")
	}
	h.click("Back to app")
	h.tt.Frame()
	if h.tt.HasText("Broken") {
		t.Fatalf("a disabled plugin's page in the sidebar: %q", h.tt.Texts())
	}
	if _, ok := h.tt.Find("Counter"); !ok {
		t.Fatal("the counter went with it")
	}
	if strings.Contains(strings.Join(h.tt.Texts(), "|"), "script") {
		t.Fatal("the broken page still shows")
	}
}

// Someone else's plugin, installed from its package in Settings, waits on
// the consent sheet until the user allows what it asks for.
func TestInstallingSomeoneElsesPluginAsksForConsent(t *testing.T) {
	src := filepath.Join(t.TempDir(), "hello")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"plugin.json", "index.js"} {
		b, _ := os.ReadFile(filepath.Join("examples/plugins/hello", f))
		os.WriteFile(filepath.Join(src, f), []byte(strings.Replace(string(b), `"permissions": {}`, `"capabilities": ["clipboard"], "permissions": {}`, 1)), 0o644)
	}
	theirs, err := pack.LoadKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := pack.Sign(src, theirs); err != nil {
		t.Fatal(err)
	}
	data, err := pack.Pack(src)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "hello"+pack.Extension)
	os.WriteFile(file, data, 0o644)

	h := pluginApp(t)
	h.a.router.Push("/settings/plugins")
	h.tt.Frame()
	h.a.pluginSource = file
	h.tt.Frame()
	h.click("Install")
	h.until("the consent sheet", func() bool {
		return h.tt.HasText("Hello is someone else's. It asks to:") && h.tt.HasText("• Copy text to the clipboard")
	})
	if len(h.a.plugins.Entries()) != 0 {
		t.Fatal("someone else's plugin ran before it was allowed")
	}
	h.click("Allow")
	h.tt.Frame()
	if len(h.a.plugins.Entries()) != 1 || !h.tt.HasText("Someone else's, allowed") {
		t.Fatalf("after allowing: %q", h.tt.Texts())
	}
}

// A text setting is a draft as it's typed, saved once on Enter: a
// shortcut half typed isn't refused as it goes.
func TestATextSettingSavesOnEnter(t *testing.T) {
	h := pluginApp(t)
	dst := filepath.Join(plugins.Dir(h.a.dir), "keys")
	os.MkdirAll(dst, 0o755)
	os.WriteFile(filepath.Join(dst, "plugin.json"), []byte(`{"id": "keys", "name": "Keys", "main": "index.js",
		"glitched": {"settings": [{"id": "key", "kind": "shortcut", "label": "Its key"}]}}`), 0o644)
	os.WriteFile(filepath.Join(dst, "index.js"), []byte(`export function view() {}`), 0o644)
	signAsOwn(t, h.a.dir, dst)
	h.a.plugins.Reload()
	h.a.router.Push("/settings/plugins")
	h.tt.Frame()
	h.openPlugin("Keys")
	field, ok := h.tt.AccessByRole(ui.RoleTextField, "Its key")
	if !ok {
		t.Fatalf("no field for the setting: %q", h.tt.Texts())
	}
	h.tt.ClickAt(field.Bounds.X+field.Bounds.W/2, field.Bounds.Y+field.Bounds.H/2)
	h.tt.Type("cmd+sh")
	h.tt.Frame()
	if v := h.a.plugins.SettingValues("keys")["key"]; v != "" {
		t.Fatalf("saved as typed: %q", v)
	}
	h.tt.Type("ift+k")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	if v := h.a.plugins.SettingValues("keys")["key"]; v != "cmd+shift+k" {
		t.Fatalf("after Enter: %q", v)
	}
}
