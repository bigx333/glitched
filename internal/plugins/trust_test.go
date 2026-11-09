package plugins

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"glitched/internal/plugins/pack"
)

// author is someone else's key, as an author's device holds it.
func author(t *testing.T) *pack.Key {
	t.Helper()
	k, err := pack.LoadKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func writeFolder(t *testing.T, folder, manifest, js string) {
	t.Helper()
	os.MkdirAll(folder, 0o755)
	os.WriteFile(filepath.Join(folder, "plugin.json"), []byte(manifest), 0o644)
	os.WriteFile(filepath.Join(folder, "index.js"), []byte(js), 0o644)
}

func pluginNamed(h *Host, id string) Plugin {
	for _, p := range h.Installed() {
		if p.ID == id {
			return p
		}
	}
	return Plugin{}
}

func TestAnUnsignedPluginWaitsToBeTrustedAsYours(t *testing.T) {
	state := t.TempDir()
	folder := filepath.Join(Dir(state), "mine")
	writeFolder(t, folder, `{"id": "mine", "name": "Mine", "main": "index.js", "contributes": {"pages": [{"id": "home"}]}}`, `export function page() { return { title: "x", sections: [] } }`)
	h := Open(state, Options{Logf: t.Logf, SignOwn: true})
	defer h.Close()
	if p := pluginNamed(h, "mine"); p.Trust != TrustUnsigned || p.Err == nil || len(h.Entries()) != 0 {
		t.Fatalf("unsigned: %+v", p)
	}
	if err := h.TrustAsMine(folder); err != nil {
		t.Fatal(err)
	}
	if p := pluginNamed(h, "mine"); p.Trust != TrustOwn || p.Err != nil || len(h.Entries()) != 1 {
		t.Fatalf("trusted: %+v", p)
	}
	// Your own edits are signed again as it loads.
	os.WriteFile(filepath.Join(folder, "index.js"), []byte(`export function page() { return { title: "y", sections: [] } }`), 0o644)
	h.Reload()
	if p := pluginNamed(h, "mine"); p.Trust != TrustOwn || p.Err != nil {
		t.Fatalf("after an edit: %+v", p)
	}
	// It runs from its snapshot, which a later edit doesn't reach.
	if f := h.Folder("mine"); !strings.HasPrefix(f, pack.SnapshotsDir(state)) {
		t.Fatalf("runs from %s", f)
	}
}

func TestSomeoneElsesPluginWaitsForConsentAndAgainWhenItAsksForMore(t *testing.T) {
	state, key := t.TempDir(), author(t)
	folder := filepath.Join(Dir(state), "theirs")
	manifest := func(caps string) string {
		return `{"id": "theirs", "name": "Theirs", "version": "1.0.0", "main": "index.js", "capabilities": [` + caps + `], "contributes": {"pages": [{"id": "home"}]}}`
	}
	writeFolder(t, folder, manifest(`"clipboard"`), `export function page() { return { title: "x", sections: [] } }`)
	if err := pack.Sign(folder, key); err != nil {
		t.Fatal(err)
	}
	h := Open(state, Options{Logf: t.Logf, SignOwn: true})
	defer h.Close()
	p := pluginNamed(h, "theirs")
	if p.Trust != TrustWaiting || len(h.Entries()) != 0 || len(p.Asks) != 1 || p.Asks[0] != "Copy text to the clipboard" {
		t.Fatalf("before consent: %+v", p)
	}
	if err := h.Allow("theirs"); err != nil {
		t.Fatal(err)
	}
	if p := pluginNamed(h, "theirs"); p.Trust != TrustConsented || len(h.Entries()) != 1 {
		t.Fatalf("allowed: %+v", p)
	}
	// An update by the same author asking for less keeps the consent; one
	// asking for more waits again.
	writeFolder(t, folder, manifest(``), `export function page() { return { title: "x", sections: [] } }`)
	pack.Sign(folder, key)
	h.Reload()
	if p := pluginNamed(h, "theirs"); p.Trust != TrustConsented {
		t.Fatalf("asking for less: %+v", p)
	}
	writeFolder(t, folder, manifest(`"clipboard", "notify"`), `export function page() { return { title: "x", sections: [] } }`)
	pack.Sign(folder, key)
	h.Reload()
	if p := pluginNamed(h, "theirs"); p.Trust != TrustWaiting || len(h.Entries()) != 0 {
		t.Fatalf("asking for more: %+v", p)
	}
	// Changed after its author signed it: refused, and never signed as yours.
	h.Allow("theirs")
	os.WriteFile(filepath.Join(folder, "index.js"), []byte(`export function page() { return { title: "evil", sections: [] } }`), 0o644)
	h.Reload()
	if p := pluginNamed(h, "theirs"); p.Trust != TrustTampered || p.Err == nil || !strings.Contains(p.Err.Error(), "index.js") || len(h.Entries()) != 0 {
		t.Fatalf("tampered: %+v", p)
	}
}

func TestInstallingAPackageFileAndRemovingIt(t *testing.T) {
	state, key := t.TempDir(), author(t)
	src := filepath.Join(t.TempDir(), "words")
	writeFolder(t, src, `{"id": "words", "name": "Words", "version": "1.0.0", "main": "index.js", "capabilities": ["tools"],
		"glitched": {"tools": [{"name": "count", "description": "Counts."}]}}`, `export function tool() { return 1 }`)
	if err := pack.Sign(src, key); err != nil {
		t.Fatal(err)
	}
	data, err := pack.Pack(src)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "words"+pack.Extension)
	os.WriteFile(file, data, 0o644)
	h := Open(state, Options{Logf: t.Logf})
	defer h.Close()
	r, err := h.Install(context.Background(), pack.Source{Location: file})
	if err != nil || r.Decision != pack.NeedsConsent {
		t.Fatalf("%+v, %v", r, err)
	}
	h.Reload()
	if p := pluginNamed(h, "words"); p.Trust != TrustWaiting || !strings.Contains(strings.Join(p.Asks, "|"), "Offer the tool count to your agents") {
		t.Fatalf("installed: %+v", p)
	}
	h.Allow("words")
	if p := pluginNamed(h, "words"); p.Trust != TrustConsented || p.Err != nil {
		t.Fatalf("allowed: %+v", p)
	}
	if err := h.Remove("words"); err != nil {
		t.Fatal(err)
	}
	if len(h.Installed()) != 0 {
		t.Fatalf("after removing: %+v", h.Installed())
	}
	if _, err := os.Stat(filepath.Join(Dir(state), "words")); !os.IsNotExist(err) {
		t.Fatal("its folder stayed")
	}
}
