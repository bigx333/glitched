package plugins

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"glitched/internal/plugins/pack"
)

const actionsModule = `
export function place({ items }) {
	return items.map(() => ({ type: "text", text: "drawn by " + host.dataDir.split("/").pop() }))
}`

func replacerManifest(id, at, version string) string {
	return `{"id": "` + id + `", "name": "` + id + `", "main": "index.js", "contract": "glitched", "contractVersion": ` + version + `,
	"capabilities": ["ui.place", "ui.replace"],
	"glitched": {"places": [{"id": "actions", "at": "` + at + `", "replaces": true}]}}`
}

func writeReplacer(t *testing.T, dst, manifest, js string) {
	t.Helper()
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"plugin.json": manifest, "index.js": js} {
		if err := os.WriteFile(filepath.Join(dst, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// builtIn is an app's built-in plugins: header, signed by a release key.
func builtIn(t *testing.T) (fs.FS, string, string) {
	t.Helper()
	root := t.TempDir()
	key, err := pack.LoadKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(root, "header")
	writeReplacer(t, dst, replacerManifest("header", "thread.header.actions", "2"), actionsModule)
	if err := pack.Sign(dst, key); err != nil {
		t.Fatal(err)
	}
	return os.DirFS(root), key.Public(), root
}

func openBuiltIn(t *testing.T, state string, bundled fs.FS, release string) *Host {
	t.Helper()
	u := newUIThread()
	h := Open(state, Options{Post: u.post, Logf: t.Logf, CallTimeout: 2 * time.Second, SignOwn: true, Bundled: bundled, ReleaseKeys: []string{release}})
	t.Cleanup(h.Close)
	return h
}

func replacers(h *Host) []string {
	var out []string
	for _, r := range h.Replacers("thread.header.actions") {
		out = append(out, r.Plugin)
	}
	return out
}

func installedAs(h *Host, folder string) Plugin {
	for _, p := range h.Installed() {
		if p.Folder == folder {
			return p
		}
	}
	return Plugin{}
}

func TestABuiltInDrawsItsComponentTillAUserPluginGrantedToTakesOver(t *testing.T) {
	state := t.TempDir()
	bundled, release, _ := builtIn(t)
	writeReplacer(t, filepath.Join(Dir(state), "mine"), replacerManifest("mine", "thread.header.actions", "2"), actionsModule)
	signAsOwn(t, state, filepath.Join(Dir(state), "mine"))
	h := openBuiltIn(t, state, bundled, release)

	b := installedAs(h, filepath.Join(BundledDir(state), "header"))
	if !b.Builtin || b.Trust != TrustBuiltin || b.Err != nil {
		t.Fatalf("built-in: %+v", b)
	}
	if got := replacers(h); !slices.Equal(got, []string{"header"}) {
		t.Fatalf("before the grant, %v draw the header", got)
	}
	if err := h.SetGranted("mine", "ui.replace", true); err != nil {
		t.Fatal(err)
	}
	if got := replacers(h); !slices.Equal(got, []string{"mine", "header"}) {
		t.Fatalf("with the grant, %v draw the header, best first", got)
	}
	if err := h.SetEnabled("mine", false); err != nil {
		t.Fatal(err)
	}
	if got := replacers(h); !slices.Equal(got, []string{"header"}) {
		t.Fatalf("with yours off, %v draw the header", got)
	}

	if err := h.SetGranted("header", "ui.replace", false); err != nil {
		t.Fatal(err)
	}
	if got := replacers(h); len(got) != 0 {
		t.Fatalf("a built-in's revoked grant still lets %v draw", got)
	}
	if err := h.SetGranted("header", "ui.replace", true); err != nil {
		t.Fatal(err)
	}
	if got := replacers(h); !slices.Equal(got, []string{"header"}) {
		t.Fatalf("given back, %v draw", got)
	}
}

func TestACopyOfABuiltInShadowsItAndRevertBringsItBack(t *testing.T) {
	state := t.TempDir()
	bundled, release, _ := builtIn(t)
	h := openBuiltIn(t, state, bundled, release)

	copied, err := h.Copy("header")
	if err != nil {
		t.Fatal(err)
	}
	mine := installedAs(h, copied)
	if mine.Trust != TrustOwn || !mine.Shadows || mine.Err != nil {
		t.Fatalf("the copy: %+v", mine)
	}
	if b := installedAs(h, filepath.Join(BundledDir(state), "header")); b.ShadowedBy != copied || b.Enabled {
		t.Fatalf("the built-in beside the copy: %+v", b)
	}
	if got := replacers(h); !slices.Equal(got, []string{"header", "header" + FallbackSuffix}) {
		t.Fatalf("%v draw the header; the copy, with the grants it took, should, and the built-in after it", got)
	}
	if in := h.running1("header"); in == nil || in.bundled {
		t.Fatal("the built-in runs in place of the copy")
	}

	if err := h.Revert("header"); err != nil {
		t.Fatal(err)
	}
	if in := h.running1("header"); in == nil || !in.bundled {
		t.Fatal("revert didn't bring the built-in back")
	}
	kept, _ := filepath.Glob(filepath.Join(revertedDir(state), "header-*", "index.js"))
	if len(kept) != 1 {
		t.Fatalf("the copy's files went: %v", kept)
	}
	if err := h.Revert("header"); err == nil {
		t.Fatal("reverted with no copy")
	}
}

func TestAFolderOfTheSameIDMadeByHandShadowsWithNoGrants(t *testing.T) {
	state := t.TempDir()
	bundled, release, _ := builtIn(t)
	dst := filepath.Join(Dir(state), "my-header")
	writeReplacer(t, dst, replacerManifest("header", "thread.header.actions", "2"), actionsModule)
	signAsOwn(t, state, dst)
	h := openBuiltIn(t, state, bundled, release)
	if in := h.running1("header"); in == nil || in.folder != dst {
		t.Fatal("your folder doesn't run in place of the built-in")
	}
	if got := replacers(h); !slices.Equal(got, []string{"header" + FallbackSuffix}) {
		t.Fatalf("%v draw; only the built-in should, your copy having no grants", got)
	}
	if len(h.Entries()) != 0 || h.Allowed("header", "ui.replace") {
		t.Fatal("the shadowed built-in shows as a plugin of its own, or lent your copy its grants")
	}

	posing := filepath.Join(Dir(state), "posing")
	writeReplacer(t, posing, replacerManifest("header"+FallbackSuffix, "thread.header.actions", "2"), actionsModule)
	h.Reload()
	if p := installedAs(h, posing); p.Err == nil {
		t.Fatal("a plugin of yours took the id the built-in falls back under")
	}
}

func TestABuiltInChangedSinceItsReleaseDoesntRun(t *testing.T) {
	state := t.TempDir()
	bundled, release, root := builtIn(t)
	if err := os.WriteFile(filepath.Join(root, "header", "index.js"), []byte(actionsModule+"\n// more"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := openBuiltIn(t, state, bundled, release)
	if b := installedAs(h, filepath.Join(BundledDir(state), "header")); b.Err == nil || h.running1("header") != nil {
		t.Fatalf("a tampered built-in: %+v", b)
	}
	other, _ := pack.LoadKey(t.TempDir())
	h2 := openBuiltIn(t, t.TempDir(), os.DirFS(t.TempDir()), other.Public())
	if len(h2.Installed()) != 0 {
		t.Fatal("an empty bundle installed something")
	}
}

// The sample replaces the header's buttons once granted, and draws them.
func TestTheQuietHeaderSampleReplacesTheBuiltIn(t *testing.T) {
	state, u := t.TempDir(), newUIThread()
	bundled, release, _ := builtIn(t)
	copyPlugin(t, state, "quiet-header", "quiet-header")
	h := Open(state, Options{Post: u.post, Logf: t.Logf, CallTimeout: 2 * time.Second, Bundled: bundled, ReleaseKeys: []string{release}})
	t.Cleanup(h.Close)
	if err := h.SetGranted("quiet-header", "ui.replace", true); err != nil {
		t.Fatal(err)
	}
	if got := replacers(h); !slices.Equal(got, []string{"quiet-header", "header"}) {
		t.Fatalf("%v draw the header", got)
	}
	s := h.App("quiet-header")
	ctx := map[string]any{"thread": map[string]string{"id": "t1"}, "local": true, "narrow": false, "changedFiles": 3,
		"panels": map[string]bool{"terminal": true, "diff": false, "preview": false}}
	s.SetItems("actions", []PlaceItem{{Key: "t1", Version: "1", Context: ctx}})
	u.until(t, "the quiet row", func() bool { return s.PlaceView("actions", "t1") != nil || s.Err != nil })
	if s.Err != nil {
		t.Fatal(s.Err)
	}
	if n := len(s.PlaceView("actions", "t1").Children); n != 4 {
		t.Fatalf("the row has %d buttons, want Open in and three toggles", n)
	}
}

func TestOffLimitsComponentsNeverLoad(t *testing.T) {
	state := t.TempDir()
	for name, manifest := range map[string]string{
		"approval": replacerManifest("approval", "approval.card", "2"),
		"send":     replacerManifest("send", "composer.send", "2"),
		"nowhere":  replacerManifest("nowhere", "no.such.thing", "2"),
		"old":      replacerManifest("old", "thread.header.actions", "1"),
		"added":    `{"id": "added", "name": "added", "main": "index.js", "contract": "glitched", "contractVersion": 2, "capabilities": ["ui.place"], "glitched": {"places": [{"id": "x", "at": "thread.header.actions"}]}}`,
		"v1":       `{"id": "v1", "name": "v1", "main": "index.js", "contract": "glitched", "contractVersion": 1, "capabilities": ["ui.place"], "glitched": {"places": [{"id": "x", "at": "thread.header"}]}}`,
	} {
		dst := filepath.Join(Dir(state), name)
		writeReplacer(t, dst, manifest, actionsModule)
		signAsOwn(t, state, dst)
	}
	h := openBuiltIn(t, state, nil, "")
	for _, id := range []string{"approval", "send"} {
		h.SetGranted(id, "ui.replace", true)
	}
	want := map[string]string{
		"approval": "approval.card, which is off limits to plugins",
		"send":     "composer.send, which is off limits to plugins",
		"nowhere":  "which is no component",
		"old":      "needs contractVersion 2",
		"added":    `takes over with "replaces": true`,
		"v1":       "",
	}
	for id, msg := range want {
		p := installedAs(h, filepath.Join(Dir(state), id))
		switch {
		case msg == "" && p.Err != nil:
			t.Errorf("%s: a contract 1 plugin didn't load: %v", id, p.Err)
		case msg != "" && (p.Err == nil || !strings.Contains(p.Err.Error(), msg)):
			t.Errorf("%s: got %v, want %q", id, p.Err, msg)
		}
	}
	if got := replacers(h); len(got) != 0 {
		t.Fatalf("%v draw", got)
	}
}
