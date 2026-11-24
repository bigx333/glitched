//go:build unix

package providerlog

import (
	"bytes"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"glitched/internal/api"
)

// The log's folder and files are its user's alone, whatever they were,
// rotations too, and it writes through no link and to no file but a
// regular one: a FIFO at a thread's path keeps no other thread waiting.
func TestTheLogKeepsToPrivateRegularFiles(t *testing.T) {
	state := state(t)
	dir := Under(state)
	thread, linked, fifo, other := api.NewID(), api.NewID(), api.NewID(), api.NewID()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.Chmod(dir, 0o755)
	for _, file := range []string{name(thread, 0), name(thread, 1)} {
		os.WriteFile(filepath.Join(dir, file), stamped(time.Now(), "old"), 0o644)
		os.Chmod(filepath.Join(dir, file), 0o644)
	}
	target := filepath.Join(t.TempDir(), "elsewhere")
	os.WriteFile(target, nil, 0o600)
	if err := os.Symlink(target, File(dir, linked)); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(File(dir, fifo), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := Open(state)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	l.SetOn(true)
	for _, th := range []api.ID{thread, linked, fifo, other} {
		diag(l, th, "diagnostic")
	}
	l.Flush()
	for path, want := range map[string]os.FileMode{dir: 0o700, File(dir, thread): 0o600, filepath.Join(dir, name(thread, 1)): 0o600, File(dir, other): 0o600} {
		if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != want {
			t.Errorf("%s: %v, want mode %04o", filepath.Base(path), err, want)
		}
	}
	if b, _ := os.ReadFile(target); len(b) != 0 {
		t.Errorf("the log wrote %q through a link", b)
	}
	if rs := records(t, File(dir, other)); len(rs) != 1 {
		t.Errorf("the other thread's file holds %d records, want 1", len(rs))
	}
}

// A thread's file that is another name of another file, a link to a file
// in the folder or a second name of one outside it, is no file of the
// log's: the log writes neither through it nor to it.
func TestTheLogWritesNoOtherNameOfAFile(t *testing.T) {
	for _, c := range []struct {
		name  string
		alias func(target, path string) error
	}{
		{"a link within the folder", func(target, path string) error { return os.Symlink(filepath.Base(target), path) }},
		{"a second name", os.Link},
	} {
		t.Run(c.name, func(t *testing.T) {
			state := state(t)
			dir := Under(state)
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			owner, thread := api.NewID(), api.NewID()
			target := File(dir, owner)
			if c.name == "a second name" {
				target = filepath.Join(t.TempDir(), "outside.ndjson")
			}
			original, mode := stamped(time.Now(), "outside data"), os.FileMode(0o644)
			if c.name == "a link within the folder" {
				mode = 0o600
			}
			if err := os.WriteFile(target, original, mode); err != nil {
				t.Fatal(err)
			}
			os.Chmod(target, mode)
			if err := c.alias(target, File(dir, thread)); err != nil {
				t.Fatal(err)
			}
			l, err := Open(state)
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			l.SetOn(true)
			diag(l, thread, "inside data")
			l.Flush()
			fi, err := os.Stat(target)
			if err != nil {
				t.Fatal(err)
			}
			if b, _ := os.ReadFile(target); fi.Mode().Perm() != mode || !bytes.Equal(b, original) {
				t.Errorf("the log changed the file of the other name: mode %#o, %d bytes, want %d", fi.Mode().Perm(), len(b), len(original))
			}
		})
	}
}
