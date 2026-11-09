package pack

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
)

// SnapshotsDir is where Snapshot keeps the copies the host loads.
func SnapshotsDir(stateDir string) string { return filepath.Join(stateDir, "plugin-snapshots") }

// Snapshot verifies the plugin in folder and returns a copy of exactly its
// signed files at <into>/<id>/<digest>, made once per version, its files
// read-only. The host loads the plugin from there and never from folder,
// which the user (or anything running as them) can edit: an edit then
// fails Check and the next Snapshot instead of running unsigned.
//
// A snapshot already there is used only if it still holds exactly those
// files; otherwise it's made again.
func Snapshot(folder, into string) (string, error) {
	b, err := readFolder(folder)
	if err != nil {
		return "", err
	}
	m, err := b.Manifest()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(into, m.ID, b.Digest())
	if b.holds(dir) {
		return dir, nil
	}
	removeTree(dir)
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", err
	}
	sweepStale(filepath.Dir(dir), ".tmp-")
	tmp, err := os.MkdirTemp(filepath.Dir(dir), ".tmp-")
	if err != nil {
		return "", err
	}
	err = b.write(tmp, 0o444)
	if err == nil {
		err = os.Rename(tmp, dir)
	}
	if err != nil {
		removeTree(tmp)
		if b.holds(dir) { // another process made it first
			return dir, nil
		}
		return "", err
	}
	return dir, nil
}

// write writes b's files and signature.json into dir, which exists, each
// with perm. It writes through an os.Root, so no path leaves dir.
func (b *Bundle) write(dir string, perm fs.FileMode) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	for rel, data := range b.Files {
		if d := path.Dir(rel); d != "." {
			if err := root.MkdirAll(d, 0o755); err != nil {
				return err
			}
		}
		if err := root.WriteFile(rel, data, perm); err != nil {
			return err
		}
	}
	sig, err := json.MarshalIndent(b.Signature, "", "  ")
	if err != nil {
		return err
	}
	return root.WriteFile(SignatureFile, append(sig, '\n'), perm)
}

// holds reports whether dir holds b's files and signature and nothing
// else: unlike a plugin's folder, a snapshot has no entry signing ignores.
func (b *Bundle) holds(dir string) bool {
	if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() {
		return false
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return false
	}
	defer root.Close()
	seen := 0
	err = fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir():
			return nil
		case !d.Type().IsRegular():
			return errors.New("not a file")
		case p == SignatureFile:
			s, err := readSignature(dir)
			if err != nil || s.Key != b.Signature.Key || s.Sig != b.Signature.Sig {
				return errors.New("another signature")
			}
			return nil
		}
		want, ok := b.Files[p]
		if !ok {
			return errors.New("an extra file")
		}
		got, err := readCapped(root, p)
		if err != nil || !bytes.Equal(got, want) {
			return errors.New("a changed file")
		}
		seen++
		return nil
	})
	return err == nil && seen == len(b.Files)
}

// removeTree removes dir, read-only files and all.
func removeTree(dir string) {
	if runtime.GOOS == "windows" { // which won't remove a read-only file
		filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err == nil && d.Type().IsRegular() {
				os.Chmod(p, 0o644)
			}
			return nil
		})
	}
	os.RemoveAll(dir)
}

// State is what Check found.
type State string

const (
	// OK: the folder is what was signed.
	OK State = "ok"
	// Tampered: its signature checks out, but its files aren't the ones it
	// covers.
	Tampered State = "tampered"
	// Unsigned: it has no signature.json.
	Unsigned State = "unsigned"
	// Invalid: its signature.json is malformed or doesn't verify.
	Invalid State = "invalid"
)

// Status is whether a plugin folder still matches its signature.
type Status struct {
	State State
	// KeyID is the signer's, when the signature itself verifies.
	KeyID string
	// File is the file that differs, when Tampered names one.
	File string
	// Err is why it isn't OK.
	Err error
}

// Check says whether the plugin in folder still matches its signature.
func Check(folder string) Status {
	sig, err := readSignature(folder)
	if errors.Is(err, ErrUnsigned) {
		return Status{State: Unsigned, Err: err}
	}
	if err == nil {
		err = sig.check()
	}
	if err != nil {
		return Status{State: Invalid, Err: err}
	}
	st := Status{State: Tampered, KeyID: KeyID(sig.Key)}
	files, err := readTree(folder)
	if err == nil {
		err = (&Bundle{Files: files, Signature: *sig}).verify()
	}
	var te *TamperError
	switch {
	case err == nil:
		st.State = OK
	case errors.As(err, &te):
		st.File, st.Err = te.File, err
	default:
		st.Err = fmt.Errorf("its files can't be checked: %w", err)
	}
	return st
}
