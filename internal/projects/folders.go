package projects

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"glitched/internal/api"
)

const (
	// maxFolders is as many folders as one listing holds.
	maxFolders = 1000
	// listTimeout bounds one listing, as a folder on a hung network mount
	// would otherwise hold it forever.
	listTimeout = 10 * time.Second
)

// List lists the folders in path on this machine, a symlink among them
// when it points at a folder, sorted by name whatever its case, at most
// maxFolders of them. A folder hide names is never listed or entered.
func List(ctx context.Context, path string, hide []string) (api.FolderList, error) {
	dir, err := Expand(path)
	if err != nil {
		return api.FolderList{}, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return api.FolderList{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()
	type listed struct {
		list api.FolderList
		err  error
	}
	// A read stuck in the kernel can't be stopped, only left: it ends on
	// its own and its answer goes nowhere.
	done := make(chan listed, 1)
	go func() {
		list, err := list(ctx, dir, resolveHidden(hide))
		done <- listed{list, err}
	}()
	select {
	case l := <-done:
		l.list.Home = home
		return l.list, l.err
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return api.FolderList{}, fmt.Errorf("projects: listing %s took longer than %s", dir, listTimeout)
		}
		return api.FolderList{}, ctx.Err()
	}
}

func list(ctx context.Context, dir string, hidden []hiddenDir) (api.FolderList, error) {
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return api.FolderList{}, err
	}
	if covers(hidden, real) {
		return api.FolderList{}, fmt.Errorf("projects: %s: %w", dir, errHidden)
	}
	entries, err := os.ReadDir(real)
	if err != nil {
		return api.FolderList{}, err
	}
	list := api.FolderList{Path: dir, Folders: []api.Folder{}}
	if parent := filepath.Dir(dir); parent != dir {
		list.Parent = parent
	}
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return api.FolderList{}, err
		}
		at := filepath.Join(real, e.Name())
		if e.Type()&fs.ModeSymlink != 0 {
			if info, err := os.Stat(at); err != nil || !info.IsDir() {
				continue
			}
			if at, err = filepath.EvalSymlinks(at); err != nil {
				continue
			}
		} else if !e.IsDir() {
			continue
		}
		if !covers(hidden, at) {
			list.Folders = append(list.Folders, api.Folder{Name: e.Name()})
		}
	}
	slices.SortFunc(list.Folders, func(a, b api.Folder) int {
		if c := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	if len(list.Folders) > maxFolders {
		list.Folders, list.Truncated = list.Folders[:maxFolders], true
	}
	for i, f := range list.Folders {
		_, err := os.Lstat(filepath.Join(real, f.Name, ".git"))
		list.Folders[i].Repo = err == nil
	}
	return list, nil
}

// Expand is p as an absolute, clean path: "" and "~" are home, and "~/x"
// is x in it. A relative path is refused.
func Expand(p string) (string, error) {
	if p == "" || p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/")), nil
	}
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("projects: %q isn't an absolute path", p)
	}
	return filepath.Clean(p), nil
}

var errHidden = errors.New("it holds the engine's secrets, so it isn't shown")

// hiddenDir is a folder no listing shows or enters: its path with every
// symlink resolved, and what it is on disk, nil while it isn't there.
type hiddenDir struct {
	path string
	info fs.FileInfo
}

func resolveHidden(paths []string) []hiddenDir {
	var hidden []hiddenDir
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			continue
		}
		if real, err := resolve(abs); err == nil {
			abs = real
		}
		info, _ := os.Stat(abs)
		hidden = append(hidden, hiddenDir{abs, info})
	}
	return hidden
}

// refuseHidden refuses p, an absolute path that may not be there yet,
// when it resolves to a folder hide names or one in it.
func refuseHidden(p string, hide []string) error {
	real, err := resolve(p)
	if err != nil {
		return err
	}
	if covers(resolveHidden(hide), real) {
		return fmt.Errorf("projects: %s: %w", p, errHidden)
	}
	return nil
}

// resolve is p, absolute, with every symlink in the part of it that is
// there resolved; the rest, which isn't there yet, is kept as it is.
func resolve(p string) (string, error) {
	var rest []string
	for {
		real, err := filepath.EvalSymlinks(p)
		if err == nil {
			return filepath.Join(append([]string{real}, rest...)...), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(p)
		if parent == p {
			return "", err
		}
		rest = append([]string{filepath.Base(p)}, rest...)
		p = parent
	}
}

// covers reports whether p, resolved, is a hidden folder or in one: by
// its path, whatever its case, as a case-insensitive disk matches it, and
// by what each folder it is in is on disk, which no other name for it
// gets around.
func covers(hidden []hiddenDir, p string) bool {
	for _, h := range hidden {
		if within(p, h.path) {
			return true
		}
	}
	for q := p; ; q = filepath.Dir(q) {
		if info, err := os.Stat(q); err == nil {
			for _, h := range hidden {
				if h.info != nil && os.SameFile(info, h.info) {
					return true
				}
			}
		}
		if filepath.Dir(q) == q {
			return false
		}
	}
}

// within reports whether p is dir or in it, whatever the case of either.
func within(p, dir string) bool {
	if len(p) < len(dir) || !strings.EqualFold(p[:len(dir)], dir) {
		return false
	}
	return len(p) == len(dir) || p[len(dir)] == filepath.Separator || strings.HasSuffix(dir, string(filepath.Separator))
}
