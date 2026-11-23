package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
)

// entry is one file of a skill's tree, as its hash reads it.
type entry struct {
	rel  string
	exec bool
	size int
	sum  string
}

// walk reads the tree of the skill in dir, giving each file to visit by
// its path in the tree, with slashes, whether it runs, and its bytes. It
// returns the tree's hash: of each file's path, mode, size and sha256, by
// path. A tree of anything but files and folders, or past the limits, is
// refused whole, and nothing in it leads out of dir.
func walk(dir string, visit func(rel string, exec bool, data []byte) error) (string, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", fmt.Errorf("Glitched couldn't open it: %v", err)
	}
	defer root.Close()
	w := walker{root: root, visit: visit}
	if err := w.dir(".", 0); err != nil {
		return "", err
	}
	slices.SortFunc(w.files, func(a, b entry) int { return strings.Compare(a.rel, b.rel) })
	h := sha256.New()
	for _, f := range w.files {
		mode := "644"
		if f.exec {
			mode = "755"
		}
		fmt.Fprintf(h, "%s\x00%s\x00%d\x00%s\n", f.rel, mode, f.size, f.sum)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

type walker struct {
	root  *os.Root
	visit func(rel string, exec bool, data []byte) error
	files []entry
	bytes int
}

func (w *walker) dir(rel string, depth int) error {
	if depth > maxDepth {
		return fmt.Errorf("It nests folders more than %d deep.", maxDepth)
	}
	d, err := w.root.Open(rel)
	if err != nil {
		return fmt.Errorf("Glitched couldn't read %s in it: %v", filepath.ToSlash(rel), err)
	}
	entries, err := d.ReadDir(-1)
	d.Close()
	if err != nil {
		return fmt.Errorf("Glitched couldn't read %s in it: %v", filepath.ToSlash(rel), err)
	}
	for _, e := range entries {
		p := filepath.Join(rel, e.Name())
		switch t := e.Type(); {
		case t.IsDir():
			err = w.dir(p, depth+1)
		case t.IsRegular():
			err = w.file(p)
		case t&fs.ModeSymlink != 0:
			err = fmt.Errorf("It holds a link, %s, and a skill may hold only files and folders.", filepath.ToSlash(p))
		default:
			err = fmt.Errorf("It holds %s, which is neither a file nor a folder.", filepath.ToSlash(p))
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (w *walker) file(p string) error {
	if len(w.files) == maxFiles {
		return fmt.Errorf("It holds more than %d files.", maxFiles)
	}
	rel := filepath.ToSlash(p)
	changed := fmt.Errorf("%s in it changed while Glitched read it.", rel)
	was, err := w.root.Lstat(p)
	if err != nil || !was.Mode().IsRegular() {
		return changed
	}
	// Not blocking on open: what was a file may be a pipe by now.
	f, err := w.root.OpenFile(p, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return fmt.Errorf("Glitched couldn't read %s in it: %v", rel, err)
	}
	defer f.Close()
	is, err := f.Stat()
	if err != nil || !os.SameFile(was, is) || !is.Mode().IsRegular() {
		return changed
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(maxBytes-w.bytes+1)))
	if err != nil {
		return fmt.Errorf("Glitched couldn't read %s in it: %v", rel, err)
	}
	if w.bytes += len(data); w.bytes > maxBytes {
		return fmt.Errorf("It holds more than %d MiB.", maxBytes>>20)
	}
	sum := sha256.Sum256(data)
	e := entry{rel: rel, exec: is.Mode().Perm()&0o100 != 0, size: len(data), sum: hex.EncodeToString(sum[:])}
	w.files = append(w.files, e)
	if err := w.visit(rel, e.exec, data); err != nil {
		return err
	}
	return nil
}

var errChanged = errors.New("skills: a skill changed while Glitched copied it")
