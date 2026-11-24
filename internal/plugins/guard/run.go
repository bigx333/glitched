package guard

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// The limits a run is held to, Zeron's own, so a plugin behaves the same
// in both hosts.
const (
	MaxOutput      = 16 << 20
	DefaultTimeout = 60 * time.Second
	MaxTimeout     = 300 * time.Second
)

// Spec is one run a plugin asks for. Allowed is the list the user granted,
// carried with the run so no run can skip the check.
type Spec struct {
	Program string
	Args    []string
	Cwd     string
	Stdin   []byte
	Env     map[string]string
	Timeout time.Duration
	Allowed []string
	// Denied are folders no run reads or writes: none is its cwd, no arg
	// names a path inside one, and on macOS the kernel holds the program
	// and all it starts out of them.
	Denied []string
}

// Result is what the program did. Status is -1 when the run was stopped at
// its timeout or by a signal.
type Result struct {
	Status         int
	Stdout, Stderr []byte
}

// Run runs s.Program as the user if s.Allowed names it and the rules let
// it. Refusals match ErrRefused. A program that ran and failed is a Result
// with its status, not an error.
func Run(ctx context.Context, s Spec) (Result, error) {
	return run(ctx, s, UserPath())
}

func run(ctx context.Context, s Spec, path string) (Result, error) {
	if !validName(s.Program) || !slices.Contains(s.Allowed, s.Program) {
		return Result{}, refuse("this plugin doesn't run %q: it runs only the programs it names", s.Program)
	}
	if slices.ContainsFunc(s.Args, func(a string) bool { return strings.ContainsRune(a, 0) }) {
		return Result{}, refuse("a run's args can't hold a NUL")
	}
	if info, err := os.Stat(s.Cwd); !filepath.IsAbs(s.Cwd) || err != nil || !info.IsDir() {
		return Result{}, refuse("a run's cwd is an absolute path to a folder, not %q", s.Cwd)
	}
	if err := checkDenied(s.Program, s.Denied, s.Args, s.Cwd); err != nil {
		return Result{}, err
	}
	env, err := envOf(s.Env)
	if err != nil {
		return Result{}, err
	}
	// Checked by name before the lookup, so a refused shape is refused
	// whether or not the program is installed.
	fam := familyOf(s.Program)
	if err := checkArgs(fam, s.Args, s.Cwd); err != nil {
		return Result{}, err
	}
	found, ok := Find(s.Program, path)
	if !ok {
		return Result{}, fmt.Errorf("%q isn't installed on this device (or isn't on your PATH)", s.Program)
	}
	// A link named "g" to git is git: the rules follow the file that runs.
	if real, err := filepath.EvalSymlinks(found); err == nil {
		more := familyOf(filepath.Base(real)) &^ fam
		if err := checkArgs(more, s.Args, s.Cwd); err != nil {
			return Result{}, err
		}
		fam |= more
	}

	timeout := s.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	timeout = min(timeout, MaxTimeout)
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	program, args := confine(found, s.Args, s.Denied)
	cmd := exec.CommandContext(tctx, program, args...)
	cmd.Dir = s.Cwd
	cmd.Env = append(os.Environ(), "PATH="+path)
	cmd.Env = append(cmd.Env, env...)
	if fam != 0 {
		hooks, err := os.MkdirTemp("", "glitched-nohooks-")
		if err != nil {
			return Result{}, err
		}
		defer os.RemoveAll(hooks)
		cmd.Env = append(cmd.Env, GitEnv(hooks)...)
	}
	if s.Stdin != nil {
		cmd.Stdin = bytes.NewReader(s.Stdin)
	}
	var stdout, stderr capped
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	OwnGroup(cmd)
	// A grandchild that keeps a pipe open (in its own session, past the
	// group kill) must not hold the run past its end.
	cmd.WaitDelay = time.Second

	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("%q didn't start: %w", s.Program, err)
	}
	err = cmd.Wait()
	res := Result{Stdout: stdout.b, Stderr: stderr.b}
	var exit *exec.ExitError
	switch {
	case ctx.Err() != nil:
		res.Status = -1
		return res, ctx.Err()
	case err != nil && tctx.Err() != nil:
		res.Status = -1
	case err == nil || errors.Is(err, exec.ErrWaitDelay):
		res.Status = cmd.ProcessState.ExitCode()
	case errors.As(err, &exit):
		res.Status = exit.ExitCode()
	default:
		return res, err
	}
	return res, nil
}

// capped keeps the first MaxOutput bytes and drains the rest, so a chatty
// program finishes instead of blocking on a full pipe until its timeout.
type capped struct{ b []byte }

func (c *capped) Write(p []byte) (int, error) {
	if room := MaxOutput - len(c.b); room > 0 {
		c.b = append(c.b, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

// validName is a program's bare name: a path, even a relative one, would
// let a plugin run a file it wrote.
func validName(name string) bool {
	if len(name) == 0 || len(name) > 64 || !isAlnum(name[0]) {
		return false
	}
	for i := range len(name) {
		if c := name[i]; !isAlnum(c) && c != '.' && c != '_' && c != '+' && c != '-' {
			return false
		}
	}
	return true
}

func isAlnum(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// envAllowed are the variables a run may set: words and formatting, git's
// identity and prompts, gh's quiet switches. None names a program, a file
// to load, a config to read or a host.
var envAllowed = []string{
	"LANG", "LANGUAGE", "LC_ALL", "LC_COLLATE", "LC_CTYPE", "LC_MESSAGES", "LC_MONETARY", "LC_NUMERIC", "LC_TIME",
	"TZ", "TERM", "NO_COLOR", "FORCE_COLOR", "CLICOLOR", "CLICOLOR_FORCE", "COLUMNS", "LINES", "CI",
	"GIT_TERMINAL_PROMPT", "GIT_OPTIONAL_LOCKS", "GIT_LITERAL_PATHSPECS", "GIT_MERGE_AUTOEDIT",
	"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_AUTHOR_DATE",
	"GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "GIT_COMMITTER_DATE",
	"GIT_INDEX_FILE", // checked by gitIndexPath
	"GH_PROMPT_DISABLED", "GH_NO_UPDATE_NOTIFIER", "GH_SPINNER_DISABLED", "GH_REPO",
}

// envOwnPrefix marks a plugin's own variables: no program reads these.
const envOwnPrefix = "GLITCHED_PLUGIN_"

// envOf turns a run's env into KEY=value pairs set over the user's, or
// refuses it. Sorted so the first refusal is the same every time.
func envOf(env map[string]string) ([]string, error) {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		value := env[key]
		if !envName(key) {
			return nil, refuse("%q isn't an environment variable's name (capitals, digits and _)", key)
		}
		own := strings.HasPrefix(key, envOwnPrefix) && len(key) > len(envOwnPrefix)
		if !own && !slices.Contains(envAllowed, key) {
			return nil, refuse("a run can't set %s: a plugin sets only harmless variables (LANG, LC_…, TZ, NO_COLOR, git's identity, …) and its own (%s…)", key, envOwnPrefix)
		}
		if len(value) > 4096 || strings.ContainsRune(value, 0) {
			return nil, refuse("%s's value is too long, or holds a NUL", key)
		}
		if key == "GIT_INDEX_FILE" && !gitIndexPath(value) {
			return nil, refuse("GIT_INDEX_FILE is a .index file in a repository's git folder")
		}
		out = append(out, key+"="+value)
	}
	return out, nil
}

func envName(key string) bool {
	if key == "" || len(key) > 64 || key[0] >= '0' && key[0] <= '9' {
		return false
	}
	for i := range len(key) {
		if c := key[i]; !(c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}

// UserPath is where programs are looked up: the login shell's PATH (what
// the user's terminal finds), then the process's, then the usual install
// folders, which a GUI app launched from a dock often lacks. Relative
// entries are dropped: "." would run a file from the plugin's cwd.
var UserPath = sync.OnceValue(func() string {
	var dirs []string
	add := func(list string) {
		for _, d := range filepath.SplitList(list) {
			if filepath.IsAbs(d) && !slices.Contains(dirs, d) {
				dirs = append(dirs, d)
			}
		}
	}
	add(loginShellPath())
	add(os.Getenv("PATH"))
	if runtime.GOOS != "windows" {
		add(strings.Join([]string{"/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin"}, string(os.PathListSeparator)))
		if home, err := os.UserHomeDir(); err == nil {
			add(filepath.Join(home, ".local", "bin"))
		}
	}
	return strings.Join(dirs, string(os.PathListSeparator))
})

// Find is the first executable file called name in path's folders. On
// Windows only .exe counts: a .cmd or .bat runs through cmd.exe, a shell
// that would reparse the args.
func Find(name, path string) (string, bool) {
	if runtime.GOOS == "windows" && !strings.HasSuffix(strings.ToLower(name), ".exe") {
		name += ".exe"
	}
	for _, dir := range filepath.SplitList(path) {
		if !filepath.IsAbs(dir) {
			continue
		}
		p := filepath.Join(dir, name)
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() && (runtime.GOOS == "windows" || info.Mode().Perm()&0o111 != 0) {
			return p, true
		}
	}
	return "", false
}
