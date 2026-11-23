package provider

import (
	"path/filepath"
	"slices"
	"strings"
)

// Rel is path inside workspace, as a card and the steps show it, or as it
// is outside. An agent may name the workspace by the path its links
// resolve to, as macOS's /var is /private/var.
func Rel(workspace, path string) string {
	// Past a symlink, .. leaves the link's target, not its directory: such
	// a path is shown as the agent gave it.
	if slices.Contains(strings.Split(filepath.ToSlash(path), "/"), "..") {
		return path
	}
	resolved, _ := filepath.EvalSymlinks(workspace)
	for _, w := range []string{workspace, resolved} {
		if r, err := filepath.Rel(w, path); w != "" && err == nil && filepath.IsLocal(r) {
			return r
		}
	}
	return path
}
