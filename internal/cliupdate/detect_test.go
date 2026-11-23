package cliupdate

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// file writes an executable at path, under its folders.
func file(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// tempHome is a home with its symlinks resolved, as macOS's /var is one,
// so that the paths Detect resolves compare equal.
func tempHome(t *testing.T) string {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return home
}

func link(t *testing.T, target, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDetect(t *testing.T) {
	cantTell := "Glitched can't tell how"
	for _, c := range []struct {
		name     string
		provider string
		layout   func(t *testing.T, home string) string // makes the install, returns the path the provider runs
		settings string                                 // ~/.claude/settings.json
		goos     string
		source   Source
		command  func(home, path string) []string
		manual   string
	}{
		{
			name: "claude native", provider: "claude",
			layout: func(t *testing.T, home string) string {
				return link(t, file(t, filepath.Join(home, ".local/share/claude/versions/2.1.323")), filepath.Join(home, ".local/bin/claude"))
			},
			source:  Source{URL: "https://registry.npmjs.org/@anthropic-ai%2fclaude-code/latest", Field: "version"},
			command: func(home, path string) []string { return []string{path, "update"} },
		},
		{
			name: "claude native on the stable channel", provider: "claude", settings: `{"autoUpdatesChannel":"stable"}`,
			layout: func(t *testing.T, home string) string {
				return link(t, file(t, filepath.Join(home, ".local/share/claude/versions/2.1.323")), filepath.Join(home, ".local/bin/claude"))
			},
			source:  Source{URL: "https://registry.npmjs.org/@anthropic-ai%2fclaude-code/stable", Field: "version"},
			command: func(home, path string) []string { return []string{path, "update"} },
		},
		{
			name: "claude's old local install", provider: "claude", settings: `{"autoUpdatesChannel":"nightly"}`,
			layout:  func(t *testing.T, home string) string { return file(t, filepath.Join(home, ".claude/local/claude")) },
			source:  Source{URL: "https://registry.npmjs.org/@anthropic-ai%2fclaude-code/latest", Field: "version"},
			command: func(home, path string) []string { return []string{path, "update"} },
		},
		{
			name: "claude from npm", provider: "claude", settings: `{"autoUpdatesChannel":"stable"}`,
			layout: func(t *testing.T, home string) string {
				file(t, filepath.Join(home, "node/bin/npm"))
				return link(t, file(t, filepath.Join(home, "node/lib/node_modules/@anthropic-ai/claude-code/cli.js")), filepath.Join(home, "node/bin/claude"))
			},
			source: Source{URL: "https://registry.npmjs.org/@anthropic-ai%2fclaude-code/stable", Field: "version"},
			command: func(home, path string) []string {
				return []string{filepath.Join(home, "node/bin/npm"), "install", "-g", "--prefix", filepath.Join(home, "node"), "@anthropic-ai/claude-code@stable"}
			},
		},
		{
			name: "codex in a project's node_modules", provider: "codex",
			layout: func(t *testing.T, home string) string {
				return file(t, filepath.Join(home, "project/node_modules/@openai/codex/bin/codex.js"))
			},
			source:  Source{URL: "https://registry.npmjs.org/@openai%2fcodex/latest", Field: "version"},
			command: func(home, path string) []string { return []string{"npm", "install", "@openai/codex@latest"} },
			manual:  "Codex is installed in a project's node_modules",
		},
		{
			name: "a link to an install that's gone", provider: "antigravity",
			layout: func(t *testing.T, home string) string {
				return link(t, filepath.Join(home, "missing"), filepath.Join(home, ".local/bin/agy"))
			},
			command: func(home, path string) []string { return []string{path, "update"} },
			manual:  "Antigravity isn't at",
		},
		{
			name: "codex standalone", provider: "codex",
			layout: func(t *testing.T, home string) string {
				return link(t, file(t, filepath.Join(home, ".codex/packages/standalone/0.170.0/codex")), filepath.Join(home, ".local/bin/codex"))
			},
			source:  Source{URL: "https://releases.openai.com/codex/channels/latest", Field: "tag_name", Trim: "rust-v"},
			command: func(home, path string) []string { return []string{path, "update"} },
		},
		{
			name: "codex from npm", provider: "codex",
			layout: func(t *testing.T, home string) string {
				file(t, filepath.Join(home, "node/bin/npm"))
				return link(t, file(t, filepath.Join(home, "node/lib/node_modules/@openai/codex/bin/codex.js")), filepath.Join(home, "node/bin/codex"))
			},
			source: Source{URL: "https://registry.npmjs.org/@openai%2fcodex/latest", Field: "version"},
			command: func(home, path string) []string {
				return []string{filepath.Join(home, "node/bin/npm"), "install", "-g", "--prefix", filepath.Join(home, "node"), "@openai/codex@latest"}
			},
		},
		{
			name: "codex from an npm that's gone", provider: "codex",
			layout: func(t *testing.T, home string) string {
				return link(t, file(t, filepath.Join(home, "node/lib/node_modules/@openai/codex/bin/codex.js")), filepath.Join(home, "node/bin/codex"))
			},
			source: Source{URL: "https://registry.npmjs.org/@openai%2fcodex/latest", Field: "version"},
			command: func(home, path string) []string {
				return []string{"npm", "install", "-g", "--prefix", filepath.Join(home, "node"), "@openai/codex@latest"}
			},
			manual: "Glitched can't find the npm that installed Codex",
		},
		{
			name: "agy from install.sh", provider: "antigravity",
			layout:  func(t *testing.T, home string) string { return file(t, filepath.Join(home, ".local/bin/agy")) },
			source:  Source{URL: "https://antigravity-cli-auto-updater-974169037036.us-central1.run.app/manifests/darwin_arm64.json", Field: "version"},
			command: func(home, path string) []string { return []string{path, "update"} },
		},
		{
			name: "agy on an OS without a manifest", provider: "antigravity", goos: "windows",
			layout:  func(t *testing.T, home string) string { return file(t, filepath.Join(home, ".local/bin/agy")) },
			command: func(home, path string) []string { return []string{path, "update"} },
		},
		{
			name: "a Homebrew cask", provider: "claude",
			layout: func(t *testing.T, home string) string {
				file(t, filepath.Join(home, "homebrew/bin/brew"))
				return link(t, file(t, filepath.Join(home, "homebrew/Caskroom/claude-code/2.1.323/claude")), filepath.Join(home, "homebrew/bin/claude"))
			},
			source: Source{URL: "https://formulae.brew.sh/api/cask/claude-code.json", Field: "version"},
			command: func(home, path string) []string {
				return []string{filepath.Join(home, "homebrew/bin/brew"), "upgrade", "--cask", "claude-code"}
			},
		},
		{
			name: "a Homebrew formula", provider: "codex",
			layout: func(t *testing.T, home string) string {
				file(t, filepath.Join(home, "homebrew/bin/brew"))
				return link(t, file(t, filepath.Join(home, "homebrew/Cellar/codex/0.170.0/bin/codex")), filepath.Join(home, "homebrew/bin/codex"))
			},
			source: Source{URL: "https://formulae.brew.sh/api/formula/codex.json", Field: "versions.stable"},
			command: func(home, path string) []string {
				return []string{filepath.Join(home, "homebrew/bin/brew"), "upgrade", "codex"}
			},
		},
		{
			name: "an npm that isn't a program", provider: "codex",
			layout: func(t *testing.T, home string) string {
				os.MkdirAll(filepath.Join(home, "node/bin/npm"), 0o755)
				return file(t, filepath.Join(home, "node/lib/node_modules/@openai/codex/bin/codex.js"))
			},
			source: Source{URL: "https://registry.npmjs.org/@openai%2fcodex/latest", Field: "version"},
			command: func(home, path string) []string {
				return []string{"npm", "install", "-g", "--prefix", filepath.Join(home, "node"), "@openai/codex@latest"}
			},
			manual: "Glitched can't find the npm that installed Codex",
		},
		{
			name: "a brew that isn't a program", provider: "claude",
			layout: func(t *testing.T, home string) string {
				os.MkdirAll(filepath.Join(home, "homebrew/bin/brew"), 0o755)
				return file(t, filepath.Join(home, "homebrew/Caskroom/claude-code/2.1.323/claude"))
			},
			source:  Source{URL: "https://formulae.brew.sh/api/cask/claude-code.json", Field: "version"},
			command: func(home, path string) []string { return []string{"brew", "upgrade", "--cask", "claude-code"} },
			manual:  "Glitched can't find the Homebrew that installed Claude Code",
		},
		{
			name: "a Homebrew whose brew is gone", provider: "codex",
			layout: func(t *testing.T, home string) string {
				return file(t, filepath.Join(home, "homebrew/Cellar/codex/0.170.0/bin/codex"))
			},
			source:  Source{URL: "https://formulae.brew.sh/api/formula/codex.json", Field: "versions.stable"},
			command: func(home, path string) []string { return []string{"brew", "upgrade", "codex"} },
			manual:  "Glitched can't find the Homebrew that installed Codex",
		},
		{
			name: "a path Glitched doesn't know", provider: "codex",
			layout:  func(t *testing.T, home string) string { return file(t, filepath.Join(home, "opt/codex")) },
			command: func(home, path string) []string { return []string{path, "update"} },
			manual:  cantTell + " Codex was installed",
		},
		{
			name: "agy somewhere else", provider: "antigravity",
			layout:  func(t *testing.T, home string) string { return file(t, filepath.Join(home, "bin/agy")) },
			command: func(home, path string) []string { return []string{path, "update"} },
			manual:  cantTell + " Antigravity was installed",
		},
		{
			name: "not installed", provider: "claude",
			layout:  func(t *testing.T, home string) string { return "" },
			command: func(home, path string) []string { return nil },
			manual:  cantTell + " Claude Code was installed",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := tempHome(t)
			path := c.layout(t, home)
			if c.settings != "" {
				file(t, filepath.Join(home, ".claude/settings.json"))
				os.WriteFile(filepath.Join(home, ".claude/settings.json"), []byte(c.settings), 0o600)
			}
			goos := c.goos
			if goos == "" {
				goos = "darwin"
			}
			in := Detect(c.provider, path, home, goos, "arm64")
			if in.Source != c.source {
				t.Errorf("source %+v, want %+v", in.Source, c.source)
			}
			if want := c.command(home, path); !slices.Equal(in.Command, want) {
				t.Errorf("command %q, want %q", in.Command, want)
			}
			if c.manual == "" && in.Manual != "" || !strings.Contains(in.Manual, c.manual) {
				t.Errorf("manual %q, want %q", in.Manual, c.manual)
			}
		})
	}
}

// An install the engine's user can't write is the administrator's to
// update: Glitched shows the sudo command and runs nothing.
func TestDetectAnInstallOnlyAnAdministratorCanWrite(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes everywhere")
	}
	home := tempHome(t)
	npm := file(t, filepath.Join(home, "node/bin/npm"))
	pkg := filepath.Join(home, "node/lib/node_modules/@openai/codex")
	path := link(t, file(t, filepath.Join(pkg, "bin/codex.js")), filepath.Join(home, "node/bin/codex"))
	if err := os.Chmod(pkg, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(pkg, 0o755) })

	in := Detect("codex", path, home, "linux", "amd64")
	if want := []string{"sudo", npm, "install", "-g", "--prefix", filepath.Join(home, "node"), "@openai/codex@latest"}; !slices.Equal(in.Command, want) {
		t.Errorf("command %q, want %q", in.Command, want)
	}
	if !strings.Contains(in.Manual, "administrator") || !strings.Contains(in.Manual, "sudo") {
		t.Errorf("manual %q, want it to say an administrator updates it with sudo", in.Manual)
	}
	if in.Source.URL == "" {
		t.Error("an install that needs sudo still has a source to check")
	}
}

// npm replaces a package's folder in its parent, so a parent the engine's
// user can't write makes the update an administrator's too.
func TestDetectAPackageInAParentOnlyAnAdministratorCanWrite(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes everywhere")
	}
	home := tempHome(t)
	npm := file(t, filepath.Join(home, "node/bin/npm"))
	parent := filepath.Join(home, "node/lib/node_modules/@openai")
	path := file(t, filepath.Join(parent, "codex/bin/codex.js"))
	if err := os.Chmod(parent, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(parent, 0o755) })

	in := Detect("codex", path, home, "darwin", "arm64")
	if want := []string{"sudo", npm, "install", "-g", "--prefix", filepath.Join(home, "node"), "@openai/codex@latest"}; !slices.Equal(in.Command, want) || !strings.Contains(in.Manual, "administrator") {
		t.Errorf("%q, %q; want the sudo command, for an administrator", in.Command, in.Manual)
	}
}

// A Homebrew prefix another user owns is theirs to update: Homebrew
// refuses sudo, so the command stays as it is.
func TestDetectAHomebrewAnotherUserOwns(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes everywhere")
	}
	home := tempHome(t)
	brew := file(t, filepath.Join(home, "homebrew/bin/brew"))
	cellar := filepath.Join(home, "homebrew/Cellar")
	path := file(t, filepath.Join(cellar, "codex/0.170.0/bin/codex"))
	if err := os.Chmod(cellar, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(cellar, 0o755) })

	in := Detect("codex", path, home, "darwin", "arm64")
	if want := []string{brew, "upgrade", "codex"}; !slices.Equal(in.Command, want) || !strings.Contains(in.Manual, "another user's") {
		t.Errorf("%q, %q; want the brew command, for the user who owns it", in.Command, in.Manual)
	}
}

// The npm command names its prefix, so npm's own config, as the engine's
// environment gives it, can't install the update somewhere else.
func TestDetectNamesTheNpmPrefix(t *testing.T) {
	home := tempHome(t)
	prefix := filepath.Join(home, "custom-npm")
	npm := file(t, filepath.Join(prefix, "bin/npm"))
	path := file(t, filepath.Join(prefix, "lib/node_modules/@openai/codex/bin/codex.js"))
	t.Setenv("NPM_CONFIG_PREFIX", filepath.Join(home, "other-npm"))
	in := Detect("codex", path, home, "darwin", "arm64")
	if want := []string{npm, "install", "-g", "--prefix", prefix, "@openai/codex@latest"}; !slices.Equal(in.Command, want) || in.Manual != "" {
		t.Errorf("%q, %q; want %q", in.Command, in.Manual, want)
	}
}
