package guard

import (
	"path/filepath"
	"strings"
)

// checkDenied refuses a run whose cwd is inside one of denied, or whose
// args name a path inside one, before or after the links on the way. It
// sees only paths as the program gets them: a program that walks an
// ancestor of a folder, as grep -r does, still reaches inside it.
func checkDenied(denied, args []string, cwd string) error {
	if len(denied) == 0 {
		return nil
	}
	inside := func(p string) bool {
		if !filepath.IsAbs(p) {
			p = filepath.Join(cwd, p)
		}
		p = filepath.Clean(p)
		for _, q := range []string{p, resolved(p)} {
			for _, d := range denied {
				for _, d := range []string{filepath.Clean(d), resolved(filepath.Clean(d))} {
					if rel, err := filepath.Rel(d, q); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
						return true
					}
				}
			}
		}
		return false
	}
	if inside(cwd) {
		return refuse("a plugin's run can't start in the folder that keeps the credentials agents were given")
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
