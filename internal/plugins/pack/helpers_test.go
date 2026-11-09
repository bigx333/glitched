package pack

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// manifest is a plugin.json for id at version, with extra fields merged in.
func manifest(id, version string, extra map[string]any) []byte {
	m := map[string]any{"id": id, "name": id, "version": version, "main": "index.js", "permissions": map[string]any{}}
	for k, v := range extra {
		m[k] = v
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	return b
}

// writePlugin writes files into dir, making folders as needed.
func writePlugin(t *testing.T, dir string, files map[string][]byte) {
	t.Helper()
	for rel, data := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// signedPlugin writes a plugin id at version into a new folder, signed by
// key, and returns the folder.
func signedPlugin(t *testing.T, key *Key, id, version string, extra map[string]any) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), id)
	writePlugin(t, dir, map[string][]byte{
		ManifestFile:  manifest(id, version, extra),
		"index.js":    []byte("export function page() { return { title: 'x' } }\n"),
		"lib/util.js": []byte("export const v = " + `"` + version + `"` + "\n"),
	})
	if err := Sign(dir, key); err != nil {
		t.Fatal(err)
	}
	return dir
}

func newKey(t *testing.T) *Key {
	t.Helper()
	k, err := LoadKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// rawPackage is a package of files signed by key over exactly them, built
// by hand so a test can put in it what Pack never would.
func rawPackage(key *Key, files map[string][]byte) []byte {
	h := hashes(files)
	sig := Signature{Format: SignatureFormat, Key: key.Public(), Files: h, Sig: key.sign(payload(key.Public(), h))}
	p := packageFile{Format: PackageFormat, Files: map[string]string{}, Signature: sig}
	for rel, data := range files {
		p.Files[rel] = base64.StdEncoding.EncodeToString(data)
	}
	b, _ := json.Marshal(p)
	return b
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
