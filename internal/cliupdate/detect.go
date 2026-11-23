package cliupdate

import (
	"fmt"
	"path/filepath"
	"strings"

	"glitched/internal/jsonfile"
)

// Install is how one CLI is installed on this machine, as the path of its
// binary tells.
type Install struct {
	// Path is the binary, its symlinks resolved.
	Path string
	// Source is where the install's latest version is published: the
	// zero Source when Glitched can't tell.
	Source Source
	// Command updates the install, or, when Manual says why Glitched
	// won't run it, is what the user can.
	Command []string
	Manual  string
}

var names = map[string]string{"claude": "Claude Code", "codex": "Codex", "antigravity": "Antigravity"}

// Name is the provider's CLI as its user knows it.
func Name(provider string) string {
	if n, ok := names[provider]; ok {
		return n
	}
	return provider
}

// folders are the CLIs' own folders under home, where a launch that
// moves none leaves them.
var folders = map[string]string{"claude": ".claude", "codex": ".codex"}

const (
	claudePackage = "@anthropic-ai/claude-code"
	codexPackage  = "@openai/codex"
)

// Detect reads how provider's CLI at path was installed, for a user whose
// home is home on a goos and goarch machine: where its own updater looks
// for a newer version, and how it updates. folder is the CLI's own folder
// as its launch moves it, CLAUDE_CONFIG_DIR's or CODEX_HOME's; "" is its
// default under home.
func Detect(provider, path, home, folder, goos, goarch string) Install {
	name := Name(provider)
	if path == "" {
		return Install{Manual: fmt.Sprintf("Glitched can't tell how %s was installed, so it won't run its updater for you.", name)}
	}
	real, err := filepath.EvalSymlinks(path)
	switch {
	case err != nil:
		return Install{Path: path, Command: []string{path, "update"}, Manual: fmt.Sprintf("%s isn't at %s any more, so Glitched won't run its updater.", name, path)}
	case !executable(real):
		return Install{Path: real, Command: []string{path, "update"}, Manual: fmt.Sprintf("%s at %s isn't a program, so Glitched won't run its updater.", name, real)}
	}
	// A home behind a symlink, as macOS's /var is, still holds real.
	if h, err := filepath.EvalSymlinks(home); err == nil {
		home = h
	}
	if folder == "" {
		folder = filepath.Join(home, folders[provider])
	} else if f, err := filepath.EvalSymlinks(folder); err == nil {
		folder = f
	}
	in := Install{Path: real, Command: []string{path, "update"}}
	slash := filepath.ToSlash(real)
	under := func(dir string) bool { return strings.HasPrefix(slash, filepath.ToSlash(dir)+"/") }
	global := func(pkg string) bool { return strings.Contains(slash, "/lib/node_modules/"+pkg+"/") }

	if prefix, kind, token, ok := homebrew(slash); ok {
		brew := filepath.Join(filepath.FromSlash(prefix), "bin", "brew")
		in.Source = Source{URL: "https://formulae.brew.sh/api/formula/" + token + ".json", Field: "versions.stable"}
		in.Command = []string{brew, "upgrade", token}
		if kind == "Caskroom" {
			in.Source = Source{URL: "https://formulae.brew.sh/api/cask/" + token + ".json", Field: "version"}
			in.Command = []string{brew, "upgrade", "--cask", token}
		}
		kept := filepath.Join(filepath.FromSlash(prefix), kind)
		switch {
		case !executable(brew):
			in.Command[0] = "brew"
			in.Manual = fmt.Sprintf("Glitched can't find the Homebrew that installed %s, so it won't run the update for you.", name)
		case !writable(kept) || !writable(filepath.Join(kept, token)):
			// Homebrew refuses sudo: the user who owns its prefix updates it.
			in.Manual = fmt.Sprintf("%s's Homebrew is another user's to write, so its update is theirs to run.", name)
		}
		return in
	}

	// A global npm package is npm's wherever its prefix is, even inside a
	// native install's folder.
	switch {
	case provider == "claude" && global(claudePackage):
		return npmGlobal(in, slash, claudePackage, claudeChannel(folder), name)

	case provider == "claude" && (under(filepath.Join(home, ".local", "share", "claude")) || under(filepath.Join(folder, "local"))):
		in.Source = npm(claudePackage, claudeChannel(folder))
		return adminOnly(in, name, filepath.Dir(real))

	case provider == "claude" && strings.Contains(slash, "/node_modules/"+claudePackage+"/"):
		return npmGlobal(in, slash, claudePackage, claudeChannel(folder), name)

	case provider == "codex" && global(codexPackage):
		return npmGlobal(in, slash, codexPackage, "latest", name)

	case provider == "codex" && under(filepath.Join(folder, "packages", "standalone")):
		in.Source = Source{URL: "https://releases.openai.com/codex/channels/latest", Field: "tag_name", Trim: "rust-v"}
		return adminOnly(in, name, filepath.Dir(real))

	case provider == "codex" && strings.Contains(slash, "/node_modules/"+codexPackage+"/"):
		return npmGlobal(in, slash, codexPackage, "latest", name)

	case provider == "antigravity" && real == filepath.Join(home, ".local", "bin", "agy"):
		if (goos == "darwin" || goos == "linux") && (goarch == "arm64" || goarch == "amd64") {
			in.Source = Source{URL: "https://antigravity-cli-auto-updater-974169037036.us-central1.run.app/manifests/" + goos + "_" + goarch + ".json", Field: "version"}
		}
		return adminOnly(in, name, filepath.Dir(real))
	}
	in.Manual = fmt.Sprintf("Glitched can't tell how %s was installed, so it won't run its updater for you.", name)
	return in
}

// npm is the npm registry's version of pkg under the dist-tag tag.
func npm(pkg, tag string) Source {
	return Source{URL: "https://registry.npmjs.org/" + strings.Replace(pkg, "/", "%2f", 1) + "/" + tag, Field: "version"}
}

// claudeChannel is the release channel Claude Code updates from: the
// autoUpdatesChannel of the settings in its folder, "latest" unless it
// says "stable".
func claudeChannel(folder string) string {
	var settings struct {
		Channel string `json:"autoUpdatesChannel"`
	}
	jsonfile.Load(filepath.Join(folder, "settings.json"), &settings)
	if settings.Channel == "stable" {
		return "stable"
	}
	return "latest"
}

// homebrew reads a path inside Homebrew's prefix: the prefix, Caskroom or
// Cellar, and the cask's or formula's token.
func homebrew(slash string) (prefix, kind, token string, ok bool) {
	for _, k := range []string{"Caskroom", "Cellar"} {
		before, after, found := strings.Cut(slash, "/"+k+"/")
		if !found {
			continue
		}
		if t, _, ok := strings.Cut(after, "/"); ok && t != "" {
			return before, k, t, true
		}
	}
	return "", "", "", false
}

// npmGlobal is in for pkg as npm installed it in a global prefix, whose
// own npm installs tag over it. One in a project's node_modules is the
// project's to update.
func npmGlobal(in Install, slash, pkg, tag, name string) Install {
	in.Source = npm(pkg, tag)
	prefix, _, global := strings.Cut(slash, "/lib/node_modules/"+pkg+"/")
	if !global {
		in.Command = []string{"npm", "install", pkg + "@" + tag}
		in.Manual = fmt.Sprintf("%s is installed in a project's node_modules, so it's that project's to update.", name)
		return in
	}
	prefix = filepath.FromSlash(prefix)
	npmPath := filepath.Join(prefix, "bin", "npm")
	// The prefix is given, or npm's config, NPM_CONFIG_PREFIX in the
	// engine's environment included, could install it somewhere else.
	in.Command = []string{npmPath, "install", "-g", "--prefix", prefix, pkg + "@" + tag}
	if !executable(npmPath) {
		in.Command[0] = "npm"
		in.Manual = fmt.Sprintf("Glitched can't find the npm that installed %s, so it won't run the update for you.", name)
		return in
	}
	// npm replaces the package's folder, so it writes in the folder's
	// parent too.
	dir := filepath.Join(prefix, "lib", "node_modules", filepath.FromSlash(pkg))
	return adminOnly(in, name, dir, filepath.Dir(dir))
}

// adminOnly is in, unless one of dirs isn't the engine's user's to write:
// then an administrator runs its command, with sudo, which Glitched never
// does.
func adminOnly(in Install, name string, dirs ...string) Install {
	for _, dir := range dirs {
		if !writable(dir) {
			in.Command = append([]string{"sudo"}, in.Command...)
			in.Manual = fmt.Sprintf("%s is installed where only an administrator can write, so its update needs sudo.", name)
			return in
		}
	}
	return in
}
