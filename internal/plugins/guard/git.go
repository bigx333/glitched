package guard

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// family marks programs held to rules of their own. A run can be several
// at once: a program named gh that is really git gets both sets.
type family uint8

const (
	gitFamily     family = 1 << iota
	gitToolFamily        // git-lfs, scalar, git-upload-pack: they read git's config
	ghFamily
)

func familyOf(name string) family {
	switch n := normalized(name); {
	case n == "git":
		return gitFamily
	case n == "gh":
		return ghFamily
	case n == "scalar" || strings.HasPrefix(n, "git-"):
		return gitToolFamily
	}
	return 0
}

func checkArgs(fam family, args []string, cwd string) error {
	if fam&gitFamily != 0 {
		if err := gitArgs(args, cwd); err != nil {
			return err
		}
	}
	if fam&ghFamily != 0 {
		return ghArgs(args)
	}
	return nil
}

// gitEnv hardens every git-family run, including the git that gh starts.
// It goes in as GIT_CONFIG_COUNT entries after any the user already has,
// so it wins over the repository's own config (a planted fsmonitor or
// hook). Hooks are always off: Glitched has no notion yet of a checkout
// the user trusts, where Zeron keeps hooks for a commit or push.
func gitEnv(hooksDir string) []string {
	start, _ := strconv.Atoi(os.Getenv("GIT_CONFIG_COUNT"))
	start = max(start, 0)
	config := [][2]string{
		{"core.fsmonitor", "false"},
		{"protocol.ext.allow", "never"},
		{"core.pager", "cat"},
		{"core.hooksPath", hooksDir},
	}
	var out []string
	for i, kv := range config {
		out = append(out,
			fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", start+i, kv[0]),
			fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", start+i, kv[1]))
	}
	return append(out,
		fmt.Sprintf("GIT_CONFIG_COUNT=%d", start+len(config)),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_PAGER=cat",
		"PAGER=cat",
		"GIT_EDITOR=true",
		"GIT_SEQUENCE_EDITOR=true",
	)
}

// gitCommands are git's built-ins a plugin may run. A built-in name can't
// be an alias (git refuses an alias that hides one), and none runs a
// program its args name. Missing on purpose: archive, bundle, credential,
// daemon, difftool, fast-import, filter-branch, for-each-repo, help, hook,
// instaweb, maintenance, mergetool, send-email, upload-pack, and any
// "git <x>" that would run git-<x> from the PATH.
var gitCommands = []string{
	"add", "am", "annotate", "apply", "bisect", "blame", "branch", "cat-file", "check-attr", "check-ignore",
	"check-mailmap", "check-ref-format", "checkout", "cherry", "cherry-pick", "clean", "clone", "commit",
	"commit-graph", "commit-tree", "config", "count-objects", "describe", "diff", "diff-files", "diff-index",
	"diff-tree", "fetch", "for-each-ref", "format-patch", "fsck", "gc", "grep", "hash-object", "init",
	"interpret-trailers", "log", "ls-files", "ls-remote", "ls-tree", "merge", "merge-base", "merge-file",
	"merge-tree", "mktag", "mktree", "mv", "name-rev", "notes", "prune", "pull", "push", "range-diff",
	"read-tree", "rebase", "reflog", "remote", "repack", "replace", "rerere", "reset", "restore", "rev-list",
	"rev-parse", "revert", "rm", "shortlog", "show", "show-branch", "show-ref", "sparse-checkout", "stash",
	"status", "stripspace", "submodule", "switch", "symbolic-ref", "tag", "update-index", "update-ref", "var",
	"verify-commit", "verify-pack", "verify-tag", "version", "whatchanged", "worktree", "write-tree",
}

// gitRefusedLong are --options refused after the command, with any
// abbreviation (git takes a unique prefix). Each runs a program it names,
// writes a file anywhere, or reads config from an argument.
var gitRefusedLong = []string{
	"upload-pack", "receive-pack", "exec", "output", "output-directory", "config", "config-env", "template",
	"separate-git-dir", "extcmd", "open-files-in-pager", "exec-path", "export-marks", "import-marks",
}

// gitRefusedShort are the short forms of the same, per command, refused
// anywhere in a cluster ("-qc").
var gitRefusedShort = map[string]string{
	"clone":        "cu",
	"ls-remote":    "u",
	"format-patch": "o",
	"grep":         "O",
	"rebase":       "x",
}

// gitSafeConfig says whether -c key=value only changes how git prints or
// who it says you are. -c is read after GIT_CONFIG_COUNT, so the keys gitEnv
// hardens are accepted only with the value gitEnv already sets.
func gitSafeConfig(key, value string) bool {
	if strings.ContainsAny(value, "\n\x00") {
		return false
	}
	switch key = strings.ToLower(key); key {
	case "core.quotepath", "core.abbrev", "core.autocrlf", "core.safecrlf", "core.longpaths", "color.ui",
		"column.ui", "i18n.logoutputencoding", "i18n.commitencoding", "log.showsignature", "log.decorate",
		"diff.renames", "diff.noprefix", "diff.mnemonicprefix", "diff.relative", "status.showuntrackedfiles",
		"status.renames", "user.name", "user.email", "commit.gpgsign", "tag.gpgsign", "init.defaultbranch",
		"merge.conflictstyle", "pull.rebase", "pull.ff", "rebase.autostash", "fetch.prune", "gc.auto",
		"maintenance.auto", "advice.detachedhead":
		return true
	case "core.pager":
		return value == "cat"
	case "core.fsmonitor":
		return value == "false"
	case "core.hookspath":
		return value == "/dev/null"
	case "protocol.ext.allow":
		return value == "never"
	}
	return strings.HasPrefix(key, "color.") || strings.HasPrefix(key, "advice.")
}

// gitArgs refuses a git command line that could run a program, write
// outside the run's folder, or change git's config.
func gitArgs(args []string, cwd string) error {
	no := func(format string, a ...any) error {
		return refuse("Glitched doesn't run git that way: "+format, a...)
	}
	i := 0
	var command string
	for {
		if i >= len(args) {
			return nil // "git", "git --version": no command runs
		}
		arg := args[i]
		i++
		if !strings.HasPrefix(arg, "-") {
			command = arg
			break
		}
		flag, value, attached := arg, "", false
		if strings.HasPrefix(arg, "--") {
			flag, value, attached = strings.Cut(arg, "=")
		}
		switch flag {
		case "--version", "--no-pager", "-P", "--no-replace-objects", "--no-optional-locks", "--literal-pathspecs",
			"--glob-pathspecs", "--noglob-pathspecs", "--icase-pathspecs", "--no-advice", "--no-lazy-fetch", "--bare":
			if attached {
				return no("%s isn't an option a plugin may give git", arg)
			}
		case "-C", "--git-dir", "--work-tree":
			if !attached {
				if i >= len(args) {
					return no("%s needs a folder", flag)
				}
				value = args[i]
				i++
			}
			if !inside(cwd, value) {
				return no("%s %s is outside the run's folder (%s)", flag, value, cwd)
			}
		case "--namespace":
			if !attached {
				i++
			}
		case "-c":
			if i >= len(args) {
				return no("-c needs a key=value")
			}
			key, val, ok := strings.Cut(args[i], "=")
			i++
			if !ok {
				val = "true"
			}
			if !gitSafeConfig(key, val) {
				return no("-c %s could change what git runs; a plugin sets only how it prints", key)
			}
		default:
			return no("%s isn't an option a plugin may give git", arg)
		}
	}
	if !slices.Contains(gitCommands, command) {
		return no("%q isn't one of git's own commands a plugin may run", command)
	}
	rest := args[i:]
	var words []string
	for j := 0; j < len(rest); j++ {
		arg := rest[j]
		if arg == "--" || arg == "--end-of-options" {
			break
		}
		if long, ok := strings.CutPrefix(arg, "--"); ok {
			name, value, hasValue := strings.Cut(long, "=")
			for _, r := range gitRefusedLong {
				if name != "" && strings.HasPrefix(r, name) {
					return no("--%s runs a program or writes outside the repository", r)
				}
			}
			if len(name) >= 2 && strings.HasPrefix("index-output", name) {
				if !hasValue && j+1 < len(rest) {
					j++
					value = rest[j]
				}
				if !gitIndexPath(value) {
					return no("--index-output is a .index file in a repository's git folder")
				}
			}
			continue
		}
		if cluster, ok := strings.CutPrefix(arg, "-"); ok && cluster != "" {
			if k := strings.IndexAny(cluster, gitRefusedShort[command]); k >= 0 {
				return no("git %s -%c runs a program or writes outside the repository", command, cluster[k])
			}
			continue
		}
		words = append(words, arg)
	}
	first := ""
	if len(words) > 0 {
		first = words[0]
	}
	switch command {
	case "bisect":
		if first == "run" {
			return no("git bisect run runs a program")
		}
	case "submodule":
		if slices.Contains(words, "foreach") {
			return no("git submodule foreach runs a program")
		}
	case "config":
		reads := slices.ContainsFunc(rest, func(a string) bool {
			return slices.Contains([]string{"--get", "--get-all", "--get-regexp", "--get-urlmatch", "--get-color", "--get-colorbool", "-l", "--list"}, a)
		}) || first == "get" || first == "list"
		writes := slices.ContainsFunc(rest, func(a string) bool {
			return slices.Contains([]string{"--add", "--unset", "--unset-all", "--replace-all", "--rename-section", "--remove-section", "-e", "--edit"}, a)
		}) || slices.Contains([]string{"set", "unset", "rename-section", "remove-section", "edit"}, first)
		if !reads || writes {
			return no("a plugin reads git's config, never writes it")
		}
	}
	return nil
}

// inside says, lexically, whether path (from cwd when relative) stays in
// cwd. It doesn't follow links: a plugin's git can already write any file
// it can name, so this keeps honest mistakes in, not a determined plugin.
func inside(cwd, path string) bool {
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	rel, err := filepath.Rel(cwd, filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// gitIndexPath is an index file of the plugin's own: an absolute ….index
// path in a folder that holds HEAD. Writing it reaches only that repository.
func gitIndexPath(value string) bool {
	base := filepath.Base(value)
	return filepath.IsAbs(value) && filepath.Clean(value) == value &&
		strings.HasSuffix(base, ".index") && len(base) > len(".index") &&
		isFile(filepath.Join(filepath.Dir(value), "HEAD"))
}

func isFile(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.Mode().IsRegular()
}

// ghCommands are gh's own commands; anything else is an alias or an
// extension, which run programs.
var ghCommands = []string{
	"api", "attestation", "auth", "browse", "cache", "completion", "config", "gist", "gpg-key", "issue", "label",
	"org", "pr", "project", "release", "repo", "ruleset", "run", "search", "secret", "ssh-key", "status",
	"variable", "version", "workflow",
}

// ghArgs refuses a gh command line that could run a program, show the
// user's token, or change gh's settings.
func ghArgs(args []string) error {
	no := func(why string) error { return refuse("Glitched doesn't run gh that way: %s", why) }
	var words []string
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			words = append(words, a)
		}
	}
	if len(words) == 0 {
		return nil // "gh --version"
	}
	if a := args[0]; strings.HasPrefix(a, "-") && a != "--version" && a != "--help" {
		return no("gh's command comes first")
	}
	command := words[0]
	switch command {
	case "alias", "extension", "extensions", "ext":
		return no("aliases and extensions run programs")
	case "codespace", "cs":
		return no("codespace commands run programs here (ssh, cp)")
	}
	if !slices.Contains(ghCommands, command) {
		return no("that isn't one of gh's own commands")
	}
	sub := ""
	if len(words) > 1 {
		sub = words[1]
	}
	switch command {
	case "auth":
		if sub != "status" {
			return no("a plugin may ask gh auth status, nothing else of gh auth")
		}
		for _, a := range args {
			short := strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a, "t")
			if short || strings.HasPrefix(a, "--show-token") {
				return no("gh's token is never shown to a plugin")
			}
		}
	case "config":
		token := slices.ContainsFunc(words, func(w string) bool { return strings.Contains(strings.ToLower(w), "token") })
		if (sub != "get" && sub != "list") || token {
			return no("a plugin reads gh's settings (never a token), never changes them")
		}
	case "repo":
		if (sub == "clone" || sub == "fork") && slices.Contains(args, "--") {
			return no("git's own flags can't be passed through gh")
		}
	}
	return nil
}
