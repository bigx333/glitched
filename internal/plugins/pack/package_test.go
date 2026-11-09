package pack

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestPackThenUnpack(t *testing.T) {
	key := newKey(t)
	dir := signedPlugin(t, key, "hello", "1.0.0", nil)
	writePlugin(t, dir, map[string][]byte{".git/HEAD": []byte("x")})
	data, err := Pack(dir)
	if err != nil {
		t.Fatal(err)
	}
	var p map[string]json.RawMessage
	if err := json.Unmarshal(data, &p); err != nil || string(p["format"]) != `"glitched-plugin:v1"` {
		t.Fatalf("package = %s", data)
	}
	b, err := Unpack(data)
	if err != nil {
		t.Fatal(err)
	}
	if b.Key() != key.Public() || len(b.Files) != 3 || !bytes.Equal(b.Files[ManifestFile], must(os.ReadFile(filepath.Join(dir, ManifestFile)))) {
		t.Fatalf("unpacked %d files by %s", len(b.Files), KeyID(b.Key()))
	}

	writePlugin(t, dir, map[string][]byte{"index.js": []byte("evil()")})
	var te *TamperError
	if _, err := Pack(dir); !errors.As(err, &te) {
		t.Fatalf("packed a tampered folder: %v", err)
	}
}

func TestUnpackRefuses(t *testing.T) {
	key := newKey(t)
	good := func() map[string][]byte {
		return map[string][]byte{ManifestFile: manifest("hello", "1", nil), "index.js": []byte("x")}
	}
	with := func(rel string, data []byte) map[string][]byte {
		f := good()
		f[rel] = data
		return f
	}

	for _, rel := range []string{"../evil", "/etc/evil", "a/../../evil", "a//b", "./a", `a\b`, ".git/config", "lib/.hidden",
		"C:evil", "D:../evil.cmd", "NUL", "lib/com1.js", "x.", "x ", "node_modules/a.js", SignatureFile, "a\nb"} {
		if _, err := Unpack(rawPackage(key, with(rel, []byte("x")))); !errors.Is(err, ErrBadPath) {
			t.Errorf("%q: %v", rel, err)
		}
	}

	if _, err := Unpack(rawPackage(key, with("big.bin", make([]byte, MaxFileBytes+1)))); !errors.Is(err, ErrTooLarge) {
		t.Errorf("a file over the cap: %v", err)
	}
	many := good()
	for n := range MaxFiles {
		many[fmt.Sprintf("f/%d", n)] = nil
	}
	if _, err := Unpack(rawPackage(key, many)); !errors.Is(err, ErrTooLarge) {
		t.Errorf("too many files: %v", err)
	}
	five := good()
	for n := range 5 {
		five[fmt.Sprintf("blob%d", n)] = bytes.Repeat([]byte{byte(n)}, 7<<20)
	}
	if _, err := Unpack(rawPackage(key, five)); !errors.Is(err, ErrTooLarge) {
		t.Errorf("35 MiB of files: %v", err)
	}
	if _, err := Unpack(make([]byte, MaxPackageBytes+1)); !errors.Is(err, ErrTooLarge) {
		t.Errorf("a package over the cap: %v", err)
	}

	var p packageFile
	json.Unmarshal(rawPackage(key, good()), &p)
	p.Files["index.js"] = "ZXZpbCgp"
	var te *TamperError
	if _, err := Unpack(must(json.Marshal(p))); !errors.As(err, &te) || te.File != "index.js" {
		t.Errorf("a changed file: %v", err)
	}
	p.Signature = Signature{}
	if _, err := Unpack(must(json.Marshal(p))); !errors.Is(err, ErrUnsigned) {
		t.Errorf("unsigned: %v", err)
	}
	for _, data := range []string{`{}`, `[]`, `{"format":"zeron-plugin:v1","files":{}}`, `not json`} {
		if _, err := Unpack([]byte(data)); !errors.Is(err, ErrNotPackage) {
			t.Errorf("%s: %v", data, err)
		}
	}
	if _, err := Unpack(rawPackage(key, map[string][]byte{ManifestFile: manifest("hello", "1", nil)})); err == nil {
		t.Error("unpacked a plugin without its main module")
	}
	if _, err := Unpack(rawPackage(key, map[string][]byte{ManifestFile: []byte(`{"id":"../x","main":"index.js"}`), "index.js": nil})); err == nil {
		t.Error("unpacked a plugin whose id isn't one")
	}
}
