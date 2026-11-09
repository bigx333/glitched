package pack

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"testing"
)

func TestTheDeviceKeyIsMadeOnceAndKeptPrivate(t *testing.T) {
	state := t.TempDir()
	keys := make([]string, 8)
	var wg sync.WaitGroup
	for n := range keys {
		wg.Go(func() {
			k, err := LoadKey(state)
			if err != nil {
				t.Error(err)
				return
			}
			keys[n] = k.Public()
		})
	}
	wg.Wait()
	for _, k := range keys {
		if k != keys[0] {
			t.Fatalf("first uses at once made different keys: %q", keys)
		}
	}
	fi, err := os.Stat(filepath.Join(state, "plugin-key"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("plugin-key is %v", fi.Mode().Perm())
	}
	again, err := LoadKey(state)
	if err != nil || again.Public() != keys[0] {
		t.Fatalf("reloaded key = %v, %v", again, err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(again.ID()) || again.ID() != KeyID(keys[0]) {
		t.Fatalf("KeyID = %q", again.ID())
	}
	if leftovers, _ := filepath.Glob(filepath.Join(state, "plugin-key.*")); len(leftovers) > 0 {
		t.Fatalf("left behind %v", leftovers)
	}
}

func TestTrustedKeysCountAsTheUsersOwn(t *testing.T) {
	state := t.TempDir()
	other := newKey(t)
	keys, err := LoadKeys(state)
	if err != nil {
		t.Fatal(err)
	}
	if !keys.Trusts(keys.Own.Public()) || keys.Trusts(other.Public()) {
		t.Fatal("trusts the wrong keys before Trust")
	}
	if err := Trust(state, other.Public()); err != nil {
		t.Fatal(err)
	}
	if err := Trust(state, "not a key"); err == nil {
		t.Fatal("trusted a malformed key")
	}
	keys, err = LoadKeys(state)
	if err != nil || !keys.Trusts(other.Public()) || len(keys.Trusted) != 1 {
		t.Fatalf("after Trust: %+v, %v", keys, err)
	}
}

func TestSignThenVerify(t *testing.T) {
	key := newKey(t)
	dir := signedPlugin(t, key, "hello", "1.0.0", nil)
	id, err := Verify(dir)
	if err != nil || id != key.ID() {
		t.Fatalf("Verify = %q, %v; want %q", id, err, key.ID())
	}
	sig, err := readSignature(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(sig.Files) != 3 || sig.Files[ManifestFile] == "" || sig.Format != SignatureFormat {
		t.Fatalf("signature = %+v", sig)
	}
	if bytes.Contains(must(os.ReadFile(filepath.Join(dir, ManifestFile))), []byte("sig")) {
		t.Fatal("signing changed plugin.json")
	}

	// What signing leaves out can come and go.
	writePlugin(t, dir, map[string][]byte{
		".git/config":           []byte("x"),
		".DS_Store":             []byte("x"),
		"lib/.DS_Store":         []byte("x"),
		"node_modules/dep/a.js": []byte("x"),
		"lib/node_modules/b.js": []byte("x"),
	})
	if _, err := Verify(dir); err != nil {
		t.Fatalf("an ignored file broke it: %v", err)
	}
	if st := Check(dir); st.State != OK || st.KeyID != key.ID() {
		t.Fatalf("Check = %+v", st)
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func TestVerifyFindsEachKindOfTampering(t *testing.T) {
	key := newKey(t)
	for _, tc := range []struct {
		name   string
		tamper func(dir string)
		state  State
		file   string
		want   error
	}{
		{"a changed file", func(d string) { os.WriteFile(filepath.Join(d, "index.js"), []byte("evil()"), 0o644) }, Tampered, "index.js", &TamperError{"index.js", Changed}},
		{"a changed manifest", func(d string) {
			os.WriteFile(filepath.Join(d, ManifestFile), manifest("hello", "1.0.0", map[string]any{"capabilities": []string{"clipboard"}}), 0o644)
		}, Tampered, ManifestFile, &TamperError{ManifestFile, Changed}},
		{"an extra file", func(d string) { os.WriteFile(filepath.Join(d, "lib/evil.js"), []byte("x"), 0o644) }, Tampered, "lib/evil.js", &TamperError{"lib/evil.js", Extra}},
		{"a missing file", func(d string) { os.Remove(filepath.Join(d, "lib/util.js")) }, Tampered, "lib/util.js", &TamperError{"lib/util.js", Missing}},
		{"a missing manifest", func(d string) { os.Remove(filepath.Join(d, ManifestFile)) }, Tampered, ManifestFile, &TamperError{ManifestFile, Missing}},
		{"a link out", func(d string) { os.Symlink("/etc/hosts", filepath.Join(d, "lib/hosts")) }, Tampered, "", ErrLink},
		{"a linked folder", func(d string) { os.Symlink(t.TempDir(), filepath.Join(d, "more")) }, Tampered, "", ErrLink},
		{"a file over the cap", func(d string) {
			os.WriteFile(filepath.Join(d, "big.bin"), make([]byte, MaxFileBytes+1), 0o644)
		}, Tampered, "", ErrTooLarge},
		{"a forged files map", func(d string) {
			sig := must(os.ReadFile(filepath.Join(d, SignatureFile)))
			sig = regexp.MustCompile(`"index.js": "[0-9a-f]+"`).ReplaceAll(sig, []byte(`"index.js": "`+fmt.Sprintf("%064x", 1)+`"`))
			os.WriteFile(filepath.Join(d, SignatureFile), sig, 0o644)
		}, Invalid, "", ErrBadSignature},
		{"another key's signature", func(d string) { Sign(d, newKey(t)) }, OK, "", nil},
		{"no signature", func(d string) { os.Remove(filepath.Join(d, SignatureFile)) }, Unsigned, "", ErrUnsigned},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := signedPlugin(t, key, "hello", "1.0.0", nil)
			tc.tamper(dir)
			_, err := Verify(dir)
			var te *TamperError
			switch want := tc.want.(type) {
			case nil:
				if err != nil {
					t.Fatalf("Verify = %v", err)
				}
			case *TamperError:
				if !errors.As(err, &te) || *te != *want {
					t.Fatalf("Verify = %v, want %v", err, want)
				}
			default:
				if !errors.Is(err, want) {
					t.Fatalf("Verify = %v, want %v", err, want)
				}
			}
			st := Check(dir)
			if st.State != tc.state || st.File != tc.file || (tc.state != OK) != (st.Err != nil) {
				t.Fatalf("Check = %+v, want %s %q", st, tc.state, tc.file)
			}
		})
	}
}

func TestSigningRefusesWhatCantBeSigned(t *testing.T) {
	key := newKey(t)
	base := func() string {
		dir := t.TempDir()
		writePlugin(t, dir, map[string][]byte{ManifestFile: manifest("hello", "1", nil), "index.js": []byte("x")})
		return dir
	}

	dir := base()
	for n := range MaxFiles {
		writePlugin(t, dir, map[string][]byte{fmt.Sprintf("f/%d.txt", n): []byte("x")})
	}
	if err := Sign(dir, key); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("%d files: %v", MaxFiles+2, err)
	}

	dir = base()
	for n := range 5 {
		writePlugin(t, dir, map[string][]byte{fmt.Sprintf("blob%d", n): make([]byte, 7<<20)})
	}
	if err := Sign(dir, key); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("35 MiB: %v", err)
	}

	dir = base()
	writePlugin(t, dir, map[string][]byte{"a:b.js": []byte("x")})
	if err := Sign(dir, key); !errors.Is(err, ErrBadPath) {
		t.Fatalf("a colon: %v", err)
	}

	dir = base()
	link := filepath.Join(t.TempDir(), "link")
	os.Symlink(dir, link)
	if err := Sign(link, key); !errors.Is(err, ErrLink) {
		t.Fatalf("a linked folder: %v", err)
	}
	if _, err := Verify(link); !errors.Is(err, ErrLink) {
		t.Fatalf("Verify of a linked folder: %v", err)
	}

	dir = t.TempDir()
	writePlugin(t, dir, map[string][]byte{"index.js": []byte("x")})
	if err := Sign(dir, key); err == nil {
		t.Fatal("signed a folder without plugin.json")
	}
}

func TestThePayloadIsPinned(t *testing.T) {
	got := string(payload("KEY", map[string]string{"plugin.json": "aa", "a/b.js": "bb"}))
	want := "glitched-plugin-signature:v1\nkey KEY\nbb  a/b.js\naa  plugin.json\n"
	if got != want {
		t.Fatalf("payload = %q", got)
	}
}
