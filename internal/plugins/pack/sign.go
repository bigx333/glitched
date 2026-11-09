package pack

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	plugin "github.com/yxxbc/mygo-plugin"
)

// Signature is signature.json: who signed the plugin, and each file's
// sha256 as signed.
type Signature struct {
	Format string            `json:"format"`
	Key    string            `json:"key"`
	Files  map[string]string `json:"files"`
	Sig    string            `json:"sig"`
}

// fileLines is the files map as sha256sum prints it, sorted by path:
// "<hex>  <path>\n" each. Paths are plain (plainPath), so no newline can
// make two maps print alike.
func fileLines(files map[string]string) []byte {
	var b strings.Builder
	for _, rel := range slices.Sorted(maps.Keys(files)) {
		b.WriteString(files[rel] + "  " + rel + "\n")
	}
	return []byte(b.String())
}

// payload is what a signature covers: the format, the key, and the files.
func payload(key string, files map[string]string) []byte {
	return append([]byte(SignatureFormat+"\nkey "+key+"\n"), fileLines(files)...)
}

// check verifies s over its own files map, which says nothing yet about
// the files at hand.
func (s *Signature) check() error {
	if s.Format != SignatureFormat {
		return fmt.Errorf("%w: its format is %q", ErrBadSignature, s.Format)
	}
	key, err := base64.StdEncoding.DecodeString(s.Key)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return fmt.Errorf("%w: its key is malformed", ErrBadSignature)
	}
	sig, err := base64.StdEncoding.DecodeString(s.Sig)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("%w: it's malformed", ErrBadSignature)
	}
	if _, ok := s.Files[ManifestFile]; !ok {
		return fmt.Errorf("%w: it doesn't cover %s", ErrBadSignature, ManifestFile)
	}
	for rel, h := range s.Files {
		if !plainPath(rel) || len(h) != sha256.Size*2 || strings.ToLower(h) != h {
			return fmt.Errorf("%w: it names %q", ErrBadSignature, rel)
		}
	}
	if !ed25519.Verify(key, payload(s.Key, s.Files), sig) {
		return ErrBadSignature
	}
	return nil
}

// Bundle is a signed plugin in memory: every file it's made of, plugin.json
// among them, and its signature. Install and Snapshot write only bundles
// that verified, so what's written is the bytes that were checked.
type Bundle struct {
	Files     map[string][]byte
	Signature Signature
}

func hashes(files map[string][]byte) map[string]string {
	out := make(map[string]string, len(files))
	for rel, b := range files {
		sum := sha256.Sum256(b)
		out[rel] = hex.EncodeToString(sum[:])
	}
	return out
}

// verify checks the signature, then that the files are exactly the ones it
// covers.
func (b *Bundle) verify() error {
	if err := b.Signature.check(); err != nil {
		return err
	}
	have, signed := hashes(b.Files), b.Signature.Files
	for _, rel := range slices.Sorted(maps.Keys(signed)) {
		if h, ok := have[rel]; !ok {
			return &TamperError{rel, Missing}
		} else if h != signed[rel] {
			return &TamperError{rel, Changed}
		}
	}
	for _, rel := range slices.Sorted(maps.Keys(have)) {
		if _, ok := signed[rel]; !ok {
			return &TamperError{rel, Extra}
		}
	}
	return nil
}

// Key is the public key that signed b.
func (b *Bundle) Key() string { return b.Signature.Key }

// Digest names b's content, whoever signed it: the sha256 of its files
// map. A snapshot's folder is named by it.
func (b *Bundle) Digest() string {
	sum := sha256.Sum256(fileLines(b.Signature.Files))
	return hex.EncodeToString(sum[:])
}

// Manifest reads b's plugin.json as mygo-plugin does, and checks that its
// main module is one of the files.
func (b *Bundle) Manifest() (*plugin.Manifest, error) {
	m, err := plugin.ParseManifest(b.Files[ManifestFile])
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ManifestFile, err)
	}
	if _, ok := b.Files[m.Main]; !ok {
		return nil, fmt.Errorf("its main module %s is missing", m.Main)
	}
	return m, nil
}

// Sign signs the plugin in folder with key, writing its signature.json.
func Sign(folder string, key *Key) error {
	files, err := readTree(folder)
	if err != nil {
		return err
	}
	if _, ok := files[ManifestFile]; !ok {
		return fmt.Errorf("%s has no %s", folder, ManifestFile)
	}
	if _, err := plugin.ParseManifest(files[ManifestFile]); err != nil {
		return fmt.Errorf("%s: %w", ManifestFile, err)
	}
	h := hashes(files)
	sig := Signature{Format: SignatureFormat, Key: key.Public(), Files: h}
	sig.Sig = key.sign(payload(sig.Key, h))
	data, err := json.MarshalIndent(sig, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(folder, SignatureFile), append(data, '\n'))
}

// writeAtomic replaces path with data through a dot file beside it, which
// is never signed if a crash leaves it behind.
func writeAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	if err := errors.Join(err, f.Chmod(0o644), f.Close()); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// Verify checks the plugin in folder against its signature.json, and
// returns the KeyID of the key that signed it. A missing, extra or changed
// file is a *TamperError.
func Verify(folder string) (keyID string, err error) {
	b, err := readFolder(folder)
	if err != nil {
		return "", err
	}
	return KeyID(b.Key()), nil
}

// readFolder reads the plugin in folder as a bundle, verified.
func readFolder(folder string) (*Bundle, error) {
	files, err := readTree(folder)
	if err != nil {
		return nil, err
	}
	sig, err := readSignature(folder)
	if err != nil {
		return nil, err
	}
	b := &Bundle{Files: files, Signature: *sig}
	if err := b.verify(); err != nil {
		return nil, err
	}
	return b, nil
}

func readSignature(folder string) (*Signature, error) {
	fi, err := os.Lstat(filepath.Join(folder, SignatureFile))
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, ErrUnsigned
	case err != nil:
		return nil, err
	case fi.Mode()&fs.ModeSymlink != 0:
		return nil, fmt.Errorf("%w: %s", ErrLink, SignatureFile)
	case !fi.Mode().IsRegular() || fi.Size() > maxSignatureBytes:
		return nil, fmt.Errorf("%w: %s isn't one", ErrBadSignature, SignatureFile)
	}
	data, err := os.ReadFile(filepath.Join(folder, SignatureFile))
	if err != nil {
		return nil, err
	}
	var s Signature
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadSignature, err)
	}
	return &s, nil
}

// readTree reads every file signing covers under dir, within the limits.
// It reads through an os.Root, so a link that appears while it reads still
// can't take it out of dir.
func readTree(dir string) (map[string][]byte, error) {
	fi, err := os.Lstat(dir)
	switch {
	case err != nil:
		return nil, err
	case fi.Mode()&fs.ModeSymlink != 0:
		return nil, fmt.Errorf("%w: %s is one", ErrLink, dir)
	case !fi.IsDir():
		return nil, fmt.Errorf("%s isn't a folder", dir)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	files := map[string][]byte{}
	total := 0
	err = fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case p == ".":
			return nil
		case ignored(d.Name()) && d.IsDir():
			return fs.SkipDir
		case ignored(d.Name()) || p == SignatureFile:
			return nil
		case d.Type()&fs.ModeSymlink != 0:
			return fmt.Errorf("%w: %s", ErrLink, p)
		case d.IsDir():
			return nil
		case !d.Type().IsRegular():
			return fmt.Errorf("%s isn't a regular file", p)
		case !plainPath(p):
			return fmt.Errorf("%w: %q", ErrBadPath, p)
		case len(files) == MaxFiles:
			return fmt.Errorf("%w: it has more than %d files", ErrTooLarge, MaxFiles)
		}
		b, err := readCapped(root, p)
		if err != nil {
			return err
		}
		if total += len(b); total > MaxBytes {
			return fmt.Errorf("%w: it's over %d MiB", ErrTooLarge, MaxBytes>>20)
		}
		files[p] = b
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

func readCapped(root *os.Root, rel string) ([]byte, error) {
	f, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s isn't a regular file", rel)
	}
	b, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxFileBytes {
		return nil, fmt.Errorf("%w: %s is over %d MiB", ErrTooLarge, rel, MaxFileBytes>>20)
	}
	return b, nil
}
