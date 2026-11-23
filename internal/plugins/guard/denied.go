package guard

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// literal are programs that never open what their args name, so any arg
// is text to them.
var literal = []string{"basename", "dirname", "echo", "false", "printf", "true"}

// checkDenied refuses a run whose cwd is inside one of denied, or whose
// args name a path inside one: as written, after a flag's "=", or through
// links, and by the file it is, whatever the case or form of its name.
// It is the clear refusal for a run that asks outright; confine is what
// holds a program that finds its way in by itself.
func checkDenied(program string, denied, args []string, cwd string) error {
	if len(denied) == 0 {
		return nil
	}
	var folders []os.FileInfo
	for _, d := range denied {
		if fi, err := os.Stat(d); err == nil {
			folders = append(folders, fi)
		}
	}
	inside := func(p string) bool {
		if !filepath.IsAbs(p) {
			p = filepath.Join(cwd, p)
		}
		for _, q := range []string{filepath.Clean(p), resolved(filepath.Clean(p))} {
			for _, d := range denied {
				if rel, err := filepath.Rel(filepath.Clean(d), q); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
					return true
				}
			}
			for ; ; q = filepath.Dir(q) {
				if fi, err := os.Stat(q); err == nil && slices.ContainsFunc(folders, func(d os.FileInfo) bool { return os.SameFile(fi, d) }) {
					return true
				}
				if filepath.Dir(q) == q {
					break
				}
			}
		}
		return false
	}
	if inside(cwd) {
		return refuse("a plugin's run can't start in the folder that keeps the credentials agents were given")
	}
	if slices.Contains(literal, normalized(program)) {
		return nil
	}
	for _, a := range args {
		paths := []string{a}
		if _, v, ok := strings.Cut(a, "="); ok {
			paths = append(paths, v)
		}
		for _, p := range paths {
			if p != "" && inside(p) {
				return refuse("a plugin's run can't read the folder that keeps the credentials agents were given: %q", a)
			}
		}
	}
	return nil
}

// resolved is p with its links followed, as far as the path exists.
func resolved(p string) string {
	rest := ""
	for {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return p
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = parent
	}
}
