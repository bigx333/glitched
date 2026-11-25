package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/plugins"
	"glitched/internal/plugins/pack"
	"glitched/internal/plugins/widget"
)

// writeOwnPlugin writes a plugin of the user's into the app's plugins
// folder, signed as theirs, and reloads.
func writeOwnPlugin(t *testing.T, a *app, folder, manifest, js string) {
	t.Helper()
	dst := filepath.Join(plugins.Dir(a.dir), folder)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Main string `json:"main"`
	}
	json.Unmarshal([]byte(manifest), &parsed)
	entry := parsed.Main
	if entry == "" {
		entry = "index.js"
	}
	for name, body := range map[string]string{"plugin.json": manifest, entry: js} {
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
	caps := `"ui.place", "ui.replace"`
	if need := plugins.ComponentNamed(at).Needs; need != "" {
		caps += `, "` + need + `"`
	}
	return `{"id": "` + id + `", "name": "` + id + `", "main": "index.js", "contract": "glitched", "contractVersion": 2,
	"capabilities": [` + caps + `], "glitched": {"places": [{"id": "row", "at": "` + at + `", "replaces": true}]}}`
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
	if note := a.partState(p, "thread.header.actions").why; !strings.Contains(note, "failed, so the built-in draws it") || !strings.Contains(note, "broken on purpose") {
		t.Errorf("Settings says %q of the broken one", note)
	}
	if err := a.plugins.SetEnabled("mine", false); err != nil {
		t.Fatal(err)
	}

	copied, err := a.plugins.Copy("header-actions")
	if err != nil {
		t.Fatal(err)
	}
	js := filepath.Join(copied, "main.ts")
	b, _ := os.ReadFile(js)
	os.WriteFile(js, []byte(strings.ReplaceAll(string(b), `"Commit & push"`, `"Ship it"`)), 0o644)
	a.plugins.Reload()
	a.pluginsChanged(nil)
	if !shows("Ship it") || shows("Commit & push") {
		t.Fatalf("your edited copy doesn't run in the built-in's place: %q", tt.Texts())
	}
	os.WriteFile(js, []byte(brokenModule), 0o644)
	a.plugins.Reload()
	a.pluginsChanged(nil)
	if !shows("Commit & push") {
		t.Fatalf("a broken copy left no built-in: %q", tt.Texts())
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

// A frame waits once for a component's copies no plugin has drawn yet: the
// copies a later call of the frame brings show their native body till
// their drawings come.
func TestAFrameWaitsOnceForAComponentsNewCopies(t *testing.T) {
	a := offline(t)
	writeOwnPlugin(t, a, "rows", replacing("rows", "thread.empty"), `export function place({ items }) {
	return items.map(({ key }) => ({ type: "text", text: "plugin " + key }))
}`)
	if err := a.plugins.SetGranted("rows", "ui.replace", true); err != nil {
		t.Fatal(err)
	}
	keys := []string{"one", "two", "three"}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Children(func() {
			for _, k := range keys {
				a.pluginComponent(c, "thread.empty", nil, componentCopy{
					key:     k,
					box:     func() ui.Element { return ui.Column(c).Key(k) },
					context: func(plugins.ThreadRecord) any { return k },
					native:  func(_ *widget.Node) { ui.Text(c, "native "+k) },
				})
			}
		})
		a.syncPlaces()
	}, 400, 300)
	for _, want := range []string{"plugin one", "native two", "native three"} {
		if !tt.HasText(want) {
			t.Fatalf("the first frame doesn't show %q: %q", want, tt.Texts())
		}
	}
	v, _ := offlinePosts.Load(a)
	settlePlugins(t, a, tt, v.(*posts))
	for _, k := range keys {
		if !tt.HasText("plugin "+k) || tt.HasText("native "+k) {
			t.Fatalf("once drawn, %s shows %q", k, tt.Texts())
		}
	}
}

// A copy the plugin answered null for shows its native body without the
// frame waiting for it again, wherever it moves.
func TestACopyAnsweredNullIsNotWaitedForAgain(t *testing.T) {
	a := offline(t)
	// The built-in is the next replacer otherwise; the copy's null falling
	// through to the native body is what this tests.
	if err := a.plugins.SetEnabled("composer-send", false); err != nil {
		t.Fatal(err)
	}
	writeOwnPlugin(t, a, "my-send", sendManifest, `export function place({ items }) {
	return items.map(() => null)
}`)
	for _, c := range []string{"ui.replace", "threads:write"} {
		if err := a.plugins.SetGranted("my-send", c, true); err != nil {
			t.Fatal(err)
		}
	}
	keys := []string{"b", "a"}
	var took time.Duration
	tt := ui.NewTester(func(c *ui.Context) {
		start := time.Now()
		ui.Column(c).Children(func() {
			for _, k := range keys {
				a.pluginComponent(c, "composer.send", nil, componentCopy{
					key:     k,
					box:     func() ui.Element { return ui.Column(c).Key(k) },
					context: func(plugins.ThreadRecord) any { return k },
					native:  func(_ *widget.Node) { ui.Text(c, "native "+k) },
				})
			}
		})
		took = time.Since(start)
		a.syncPlaces()
	}, 400, 300)
	v, _ := offlinePosts.Load(a)
	settlePlugins(t, a, tt, v.(*posts))
	keys = []string{"a", "b"}
	tt.Frame()
	if !tt.HasText("native a") || !tt.HasText("native b") {
		t.Fatalf("copies answered null show %q; want their native bodies", tt.Texts())
	}
	if took >= firstDrawWait {
		t.Fatalf("a frame waited %v for copies already answered null", took)
	}
}

// Your plugin loaded again draws what it now draws, though its last load
// drew as many views of something else: its controls are its own, not
// the last load's, so a list or an input it now draws shows.
func TestAPluginLoadedAgainDrawsItsNewControls(t *testing.T) {
	h := start(t, inboxDir(t))
	h.thread("SIP re-INVITE handling")
	const manifest = `{"id": "mine", "name": "mine", "main": "index.js", "contract": "glitched", "contractVersion": 2,
	"capabilities": ["ui.place", "ui.replace"], "glitched": {"places": [{"id": "a", "at": "thread.header.actions", "replaces": true}]}}`
	writeOwnPlugin(t, h.a, "mine", manifest, mineModule)
	h.a.plugins.SetGranted("mine", "ui.replace", true)
	// What this tries is a new load's view of the last one's number. A
	// load's first view, drawn before any frame gives it the header's
	// copies, is apart from the one that draws them only when the start
	// is quick, so each load's first is drawn before the window asks.
	started := func() {
		t.Helper()
		app := h.a.plugins.App("mine")
		deadline := time.After(20 * time.Second)
		for app.Rev < 1 {
			if h.p.run() {
				continue
			}
			select {
			case <-h.p.wake:
			case <-deadline:
				t.Fatal("the plugin's first view never came")
			}
		}
	}
	started()
	settlePlugins(t, h.a, h.tt, h.p)
	if !h.tt.HasText("Mine") {
		t.Fatalf("yours doesn't draw the header's actions: %q", h.tt.Texts())
	}
	rev := h.a.plugins.App("mine").Rev
	writeOwnPlugin(t, h.a, "mine", manifest, `export function place({ items }) {
	return items.map(() => ({ type: "list", id: "l", label: "Mine", height: 40, items: [{ key: "a", label: "Row a" }] }))
}
export function rows({ keys }) { return keys.map(() => ({ type: "text", text: "Row a drawn" })) }`)
	started()
	settlePlugins(t, h.a, h.tt, h.p)
	if got := h.a.plugins.App("mine").Rev; got != rev {
		t.Fatalf("the new load drew view %d, the last %d: this no longer tries what it means to", got, rev)
	}
	h.tt.Advance(time.Second)
	settlePlugins(t, h.a, h.tt, h.p)
	if !h.tt.HasText("Row a drawn") {
		t.Fatalf("yours loaded again draws no list: %q", h.tt.Texts())
	}
}
