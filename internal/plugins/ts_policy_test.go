package plugins

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestRepoPluginsAreTypeScript(t *testing.T) {
	for _, v := range CheckRepoPlugins("../..") {
		t.Error(v)
	}
}

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func tsFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	writeFiles(t, dir, files)
	return dir
}

func TestCheckTypeScriptPluginAcceptsTypeScript(t *testing.T) {
	for name, manifest := range map[string]string{
		"main":         `{"main": "index.ts"}`,
		"main+service": `{"main": "index.ts", "glitched": {"service": "src/service.ts"}}`,
	} {
		dir := tsFixture(t, map[string]string{"plugin.json": manifest, "index.ts": "", "src/service.ts": "", "types.d.ts": ""})
		if got := CheckTypeScriptPlugin(dir); len(got) != 0 {
			t.Errorf("%s: %v", name, got)
		}
	}
}

func TestCheckTypeScriptPluginRejectsANonTypeScriptEntry(t *testing.T) {
	for name, manifest := range map[string]string{
		"js main":       `{"main": "index.js"}`,
		"no main":       `{"name": "x"}`,
		"d.ts main":     `{"main": "index.d.ts"}`,
		"upper TS main": `{"main": "index.TS"}`,
		"escaping main": `{"main": "../index.ts"}`,
		"missing main":  `{"main": "gone.ts"}`,
		"dir main":      `{"main": "src.ts"}`,
		"js service":    `{"main": "index.ts", "glitched": {"service": "service.js"}}`,
		"missing svc":   `{"main": "index.ts", "glitched": {"service": "gone.ts"}}`,
		"bad json":      `{"main": `,
	} {
		dir := tsFixture(t, map[string]string{"plugin.json": manifest, "index.ts": "", "index.d.ts": "", "index.TS": "", "src.ts/a.ts": ""})
		got := CheckTypeScriptPlugin(dir)
		if !slices.ContainsFunc(got, func(v TypeScriptViolation) bool { return v.Path == "plugin.json" }) {
			t.Errorf("%s: %v, want plugin.json flagged", name, got)
		}
	}
}

func TestCheckTypeScriptPluginRejectsJavaScriptFiles(t *testing.T) {
	for _, js := range []string{"a.js", "a.mjs", "a.cjs", "a.jsx", "A.JS", "lib/deep/a.js", "lib/a.Mjs"} {
		dir := tsFixture(t, map[string]string{"plugin.json": `{"main": "index.ts"}`, "index.ts": "", js: ""})
		want := []TypeScriptViolation{{js, jsReason}}
		if got := CheckTypeScriptPlugin(dir); !slices.Equal(got, want) {
			t.Errorf("%s: %v, want %v", js, got, want)
		}
	}
}

// tsRepo is a repository with a TypeScript plugin in each plugin folder, and
// JavaScript that is no plugin's.
func tsRepo(t *testing.T, extra map[string]string) string {
	t.Helper()
	root := tsFixture(t, map[string]string{
		"internal/plugins/bundled/a/plugin.json":    `{"main": "index.ts", "glitched": {"service": "service.ts"}}`,
		"internal/plugins/bundled/a/index.ts":       "",
		"internal/plugins/bundled/a/service.ts":     "",
		"internal/plugins/bundled/bundled.go":       "package bundled",
		"examples/plugins/b/plugin.json":            `{"main": "index.ts"}`,
		"examples/plugins/b/index.ts":               "",
		"examples/plugins/glitched.d.ts":            "",
		"internal/webtarget/snapshot.js":            "",
		".agents/skills/verify-glitched/ax.js":      "",
		"testdata/model-picker-before-keys.js":      "",
		"node_modules/dep/plugin.json":              `{"main": "index.js"}`,
		"node_modules/dep/index.js":                 "",
		".claude/plugins/x/plugin.json":             `{}`,
		"examples/plugins/b/node_modules/m/main.js": "",
	})
	writeFiles(t, root, extra)
	return root
}

func TestCheckRepoPluginsAcceptsTypeScriptPlugins(t *testing.T) {
	if got := CheckRepoPlugins(tsRepo(t, nil)); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestCheckRepoPluginsRejectsJavaScript(t *testing.T) {
	for name, tc := range map[string]struct {
		files map[string]string
		want  []TypeScriptViolation
	}{
		"stray example js": {
			map[string]string{"examples/plugins/stray.js": ""},
			[]TypeScriptViolation{{"examples/plugins/stray.js", jsReason}},
		},
		"stray built-in jsx": {
			map[string]string{"internal/plugins/bundled/stray.jsx": ""},
			[]TypeScriptViolation{{"internal/plugins/bundled/stray.jsx", jsReason}},
		},
		"js in a plugin": {
			map[string]string{"examples/plugins/b/lib/x.CJS": ""},
			[]TypeScriptViolation{{"examples/plugins/b/lib/x.CJS", jsReason}},
		},
		"folder without a manifest": {
			map[string]string{"examples/plugins/c/index.ts": "", "internal/plugins/bundled/d/README.md": ""},
			[]TypeScriptViolation{
				{"examples/plugins/c/plugin.json", "missing; every folder here is a plugin"},
				{"internal/plugins/bundled/d/plugin.json", "missing; every folder here is a plugin"},
			},
		},
		"js plugin elsewhere": {
			map[string]string{"plugins/rogue/plugin.json": `{"main": "index.js"}`, "plugins/rogue/index.js": ""},
			[]TypeScriptViolation{
				{"plugins/rogue/index.js", jsReason},
				{"plugins/rogue/plugin.json", `main "index.js" is not a TypeScript module (.ts)`},
			},
		},
		"plugin named build": {
			map[string]string{"examples/plugins/build/plugin.json": `{"name": "build"}`},
			[]TypeScriptViolation{
				{"examples/plugins/build/plugin.json", "main is not set, and mygo-plugin runs index.js without it"},
			},
		},
	} {
		if got := CheckRepoPlugins(tsRepo(t, tc.files)); !slices.Equal(got, tc.want) {
			t.Errorf("%s: %v, want %v", name, got, tc.want)
		}
	}
}

func TestCheckRepoPluginsNeedsThePluginFolders(t *testing.T) {
	root := tsRepo(t, nil)
	os.RemoveAll(filepath.Join(root, "examples"))
	got := CheckRepoPlugins(root)
	if len(got) != 1 || got[0].Path != "examples/plugins" {
		t.Fatal(got)
	}
}
