package plugins

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// The plugins in this repository are TypeScript: these checks keep a
// JavaScript one out of it. They are the repository's policy, not the
// host's: a user's own plugin may still be JavaScript.

// TypeScriptViolation is a file that breaks the policy, and why.
type TypeScriptViolation struct {
	Path   string
	Reason string
}

func (v TypeScriptViolation) String() string { return v.Path + ": " + v.Reason }

// pluginRoots are the folders whose every subfolder is a plugin.
var pluginRoots = []string{"internal/plugins/bundled", "examples/plugins"}

// jsExts are JavaScript's extensions, lower case.
var jsExts = map[string]bool{".js": true, ".mjs": true, ".cjs": true, ".jsx": true}

const jsReason = "JavaScript file; plugins in this repository are TypeScript"

func isJS(name string) bool { return jsExts[strings.ToLower(path.Ext(name))] }

// tsModule reports whether name is a TypeScript module mygo-plugin runs:
// a path inside the plugin, ending in .ts as it matches, and no .d.ts.
func tsModule(name string) bool {
	return name != "" && filepath.IsLocal(name) && !strings.ContainsAny(name, `\:`) &&
		strings.HasSuffix(name, ".ts") && !strings.HasSuffix(strings.ToLower(name), ".d.ts")
}

// CheckTypeScriptPlugin checks the plugin in dir: its main and service are
// TypeScript files in it, and it holds no JavaScript. Paths are relative
// to dir.
func CheckTypeScriptPlugin(dir string) []TypeScriptViolation {
	found := map[TypeScriptViolation]bool{}
	checkPlugin(os.DirFS(dir), ".", found)
	return sorted(found)
}

// CheckRepoPlugins checks every plugin in the repository at root, and that
// its plugin folders hold only plugins. Paths are relative to root.
func CheckRepoPlugins(root string) []TypeScriptViolation {
	fsys := os.DirFS(root)
	found := map[TypeScriptViolation]bool{}
	for _, dir := range pluginRoots {
		entries, err := fs.ReadDir(fsys, dir)
		if err != nil {
			found[TypeScriptViolation{dir, err.Error()}] = true
			continue
		}
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			manifest := path.Join(dir, e.Name(), "plugin.json")
			if _, err := fs.Stat(fsys, manifest); err != nil {
				found[TypeScriptViolation{manifest, "missing; every folder here is a plugin"}] = true
			}
		}
		findJS(fsys, dir, found)
	}
	skip := map[string]bool{".git": true, "node_modules": true, ".audit": true, ".claude": true}
	rootSkip := map[string]bool{"build": true, ".mygo": true, ".verify": true}
	fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			found[TypeScriptViolation{p, err.Error()}] = true
		case d.IsDir() && (skip[d.Name()] || (p == d.Name() && rootSkip[d.Name()])):
			return fs.SkipDir
		case !d.IsDir() && d.Name() == "plugin.json":
			checkPlugin(fsys, path.Dir(p), found)
		}
		return nil
	})
	return sorted(found)
}

func checkPlugin(fsys fs.FS, dir string, found map[TypeScriptViolation]bool) {
	manifest := path.Join(dir, "plugin.json")
	var m struct {
		Main     string `json:"main"`
		Glitched struct {
			Service string `json:"service"`
		} `json:"glitched"`
	}
	b, err := fs.ReadFile(fsys, manifest)
	if err == nil {
		err = json.Unmarshal(b, &m)
	}
	if err != nil {
		found[TypeScriptViolation{manifest, err.Error()}] = true
	} else {
		if m.Main == "" {
			found[TypeScriptViolation{manifest, "main is not set, and mygo-plugin runs index.js without it"}] = true
		} else {
			checkModule(fsys, dir, "main", m.Main, found)
		}
		if m.Glitched.Service != "" {
			checkModule(fsys, dir, "glitched.service", m.Glitched.Service, found)
		}
	}
	findJS(fsys, dir, found)
}

func checkModule(fsys fs.FS, dir, field, name string, found map[TypeScriptViolation]bool) {
	manifest := path.Join(dir, "plugin.json")
	if !tsModule(name) {
		found[TypeScriptViolation{manifest, fmt.Sprintf("%s %q is not a TypeScript module (.ts)", field, name)}] = true
		return
	}
	if fi, err := fs.Lstat(fsys, path.Join(dir, name)); err != nil || !fi.Mode().IsRegular() {
		found[TypeScriptViolation{manifest, fmt.Sprintf("%s %q is not a file in the plugin", field, name)}] = true
	}
}

func findJS(fsys fs.FS, dir string, found map[TypeScriptViolation]bool) {
	fs.WalkDir(fsys, dir, func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			found[TypeScriptViolation{p, err.Error()}] = true
		case d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules"):
			return fs.SkipDir
		case !d.IsDir() && isJS(d.Name()):
			found[TypeScriptViolation{p, jsReason}] = true
		}
		return nil
	})
}

func sorted(found map[TypeScriptViolation]bool) []TypeScriptViolation {
	out := make([]TypeScriptViolation, 0, len(found))
	for v := range found {
		out = append(out, v)
	}
	slices.SortFunc(out, func(a, b TypeScriptViolation) int {
		return cmp.Or(cmp.Compare(a.Path, b.Path), cmp.Compare(a.Reason, b.Reason))
	})
	return out
}
