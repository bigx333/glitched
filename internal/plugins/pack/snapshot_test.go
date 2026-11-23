package pack

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	plugin "github.com/yxxbc/mygo-plugin"
	"github.com/yxxbc/mygo-plugin/plugintest"
)

func TestTheHostRunsAReadOnlySnapshotNotTheFolder(t *testing.T) {
	key := newKey(t)
	folder := filepath.Join(t.TempDir(), "hello")
	writePlugin(t, folder, map[string][]byte{
		ManifestFile: must(os.ReadFile("../../../examples/plugins/hello/plugin.json")),
		"index.js":   must(os.ReadFile("../../../examples/plugins/hello/index.js")),
	})
	writePlugin(t, folder, map[string][]byte{".git/HEAD": []byte("x"), "node_modules/x.js": []byte("x")})
	if err := Sign(folder, key); err != nil {
		t.Fatal(err)
	}
	into := SnapshotsDir(t.TempDir())
	snap, err := Snapshot(folder, into)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(filepath.Dir(snap)) != into || filepath.Base(filepath.Dir(snap)) != "hello" {
		t.Fatalf("snapshot at %s", snap)
	}
	for _, rel := range []string{ManifestFile, "index.js", SignatureFile} {
		fi, err := os.Stat(filepath.Join(snap, rel))
		if err != nil || fi.Mode().Perm() != 0o444 {
			t.Fatalf("%s: %v, %v", rel, fi, err)
		}
	}
	for _, rel := range []string{".git", "node_modules"} {
		if _, err := os.Stat(filepath.Join(snap, rel)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s was copied", rel)
		}
	}
	// Anything running as the user can make a snapshot's file writable
	// again, and root writes it as it is: the next Snapshot makes a
	// snapshot so edited again.
	edited := filepath.Join(snap, "index.js")
	if err := os.Chmod(edited, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(edited, []byte("evil()"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Snapshot(folder, into); err != nil {
		t.Fatal(err)
	}
	if readFile(t, edited) == "evil()" {
		t.Fatal("an edit to the snapshot stayed")
	}

	p := plugintest.New(t, snap, plugin.Options{Contract: "glitched", ContractVersion: 1})
	page, err := p.Page("home")
	if err != nil || page.Title != "Hello" {
		t.Fatalf("page from the snapshot = %+v, %v", page, err)
	}

	again, err := Snapshot(folder, into)
	if err != nil || again != snap {
		t.Fatalf("second Snapshot = %s, %v", again, err)
	}

	// An edit to the folder is caught, and never reaches the snapshot.
	writePlugin(t, folder, map[string][]byte{"index.js": []byte("evil()")})
	if st := Check(folder); st.State != Tampered || st.File != "index.js" {
		t.Fatalf("Check = %+v", st)
	}
	var te *TamperError
	if _, err := Snapshot(folder, into); !errors.As(err, &te) {
		t.Fatalf("Snapshot of a tampered folder = %v", err)
	}
	if readFile(t, filepath.Join(snap, "index.js")) == "evil()" {
		t.Fatal("the edit reached the snapshot")
	}

	// A snapshot that changed is made again.
	writePlugin(t, folder, map[string][]byte{"index.js": must(os.ReadFile("../../../examples/plugins/hello/index.js"))})
	writePlugin(t, snap, map[string][]byte{"extra.js": []byte("x")})
	if _, err := Snapshot(folder, into); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(snap, "extra.js")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a snapshot with an extra file was kept")
	}
	if entries, _ := os.ReadDir(filepath.Dir(snap)); len(entries) != 1 {
		t.Fatalf("left beside the snapshot: %v", entries)
	}
}
