//go:build unix

package guard

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const testPath = "/usr/bin:/bin:/usr/local/bin:/opt/homebrew/bin"

func spec(program string, args ...string) Spec {
	return Spec{Program: program, Args: args, Cwd: os.TempDir(), Allowed: []string{program}}
}

func runT(t *testing.T, s Spec) (Result, error) {
	t.Helper()
	return run(context.Background(), s, testPath)
}

func mustRun(t *testing.T, s Spec) Result {
	t.Helper()
	res, err := runT(t, s)
	if err != nil {
		t.Fatalf("%s %q: %v", s.Program, s.Args, err)
	}
	return res
}

func need(t *testing.T, program string) {
	t.Helper()
	if _, ok := find(program, testPath); !ok {
		t.Skipf("%s isn't installed", program)
	}
}

func TestRunPassesArgsAsGivenWithoutAShell(t *testing.T) {
	res := mustRun(t, spec("echo", "hi", "$HOME", "a b", "`id`;ls"))
	if res.Status != 0 || string(res.Stdout) != "hi $HOME a b `id`;ls\n" {
		t.Fatalf("got %d %q", res.Status, res.Stdout)
	}
}

func TestRunRefusesWhatIsNotNamedOrNotAName(t *testing.T) {
	notAllowed := spec("ls")
	notAllowed.Allowed = []string{"echo"}
	cases := map[string]Spec{"not allowed": notAllowed}
	for _, bad := range []string{"/bin/echo", "./echo", "../echo", "bin/echo", "echo;ls", "", "-echo", strings.Repeat("e", 65)} {
		cases["program "+bad] = Spec{Program: bad, Cwd: os.TempDir(), Allowed: []string{bad}}
	}
	for _, cwd := range []string{"", "relative", "tmp/..", "/no/such/folder", "/etc/hostname"} {
		s := spec("pwd")
		s.Cwd = cwd
		cases["cwd "+cwd] = s
	}
	cases["NUL arg"] = spec("echo", "a\x00b")
	for name, s := range cases {
		if _, err := runT(t, s); !errors.Is(err, ErrRefused) {
			t.Errorf("%s: want a refusal, got %v", name, err)
		}
	}
}

func TestRunReportsAMissingProgramAsAFailureNotARefusal(t *testing.T) {
	_, err := runT(t, spec("no-such-program-glitched"))
	if err == nil || errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "isn't installed") {
		t.Fatalf("got %v", err)
	}
}

func TestRunTakesStdinCwdAndStatus(t *testing.T) {
	s := spec("cat")
	s.Stdin = []byte("piped in")
	if res := mustRun(t, s); string(res.Stdout) != "piped in" {
		t.Fatalf("stdin: %q", res.Stdout)
	}
	dir := t.TempDir()
	s = spec("pwd")
	s.Cwd = dir
	real, _ := filepath.EvalSymlinks(dir)
	if res := mustRun(t, s); strings.TrimSpace(string(res.Stdout)) != real {
		t.Fatalf("cwd: %q, want %q", res.Stdout, real)
	}
	res := mustRun(t, spec("ls", "/no/such/place"))
	if res.Status == 0 || len(res.Stderr) == 0 {
		t.Fatalf("failing program: %d %q", res.Status, res.Stderr)
	}
}

func TestRunSetsOnlyHarmlessVariables(t *testing.T) {
	s := spec("printenv", "GLITCHED_PLUGIN_WORD", "LC_ALL")
	s.Env = map[string]string{"GLITCHED_PLUGIN_WORD": "hello", "LC_ALL": "C"}
	if res := mustRun(t, s); string(res.Stdout) != "hello\nC\n" {
		t.Fatalf("got %q", res.Stdout)
	}
	for _, bad := range []map[string]string{
		{"PATH": "/tmp"},
		{"DYLD_INSERT_LIBRARIES": "/tmp/x.dylib"},
		{"LD_PRELOAD": "/tmp/x.so"},
		{"LD_LIBRARY_PATH": "/tmp"},
		{"GIT_SSH_COMMAND": "touch /tmp/x"},
		{"GIT_SSH": "/tmp/x"},
		{"GIT_ASKPASS": "/tmp/x"},
		{"GIT_CONFIG_COUNT": "1"},
		{"GIT_CONFIG_PARAMETERS": "'alias.x=!sh'"},
		{"GIT_CONFIG_GLOBAL": "/tmp/x"},
		{"GIT_EXEC_PATH": "/tmp"},
		{"GIT_EXTERNAL_DIFF": "/tmp/x"},
		{"GIT_DIR": "/tmp"},
		{"GIT_PROXY_COMMAND": "/tmp/x"},
		{"BASH_ENV": "/tmp/x"},
		{"ENV": "/tmp/x"},
		{"ZDOTDIR": "/tmp"},
		{"NODE_OPTIONS": "--require /tmp/x.js"},
		{"PERL5OPT": "-Mx"},
		{"PYTHONPATH": "/tmp"},
		{"PYTHONSTARTUP": "/tmp/x"},
		{"RUBYOPT": "-r/tmp/x"},
		{"GH_CONFIG_DIR": "/tmp"},
		{"GH_HOST": "evil.example"},
		{"GH_TOKEN": "x"},
		{"EDITOR": "touch /tmp/x"},
		{"PAGER": "touch /tmp/x"},
		{"HOME": "/tmp"},
		{"HTTPS_PROXY": "http://evil.example"},
		{"GLITCHED_PLUGIN_": "x"},
		{"GLITCHED_TOKEN": "x"},
		{"lower": "x"},
		{"1X": "x"},
		{"A=B": "x"},
		{"LANG": "a\x00b"},
		{"LANG": strings.Repeat("x", 4097)},
		{"GIT_INDEX_FILE": "/tmp/x.index"},
	} {
		s := spec("printenv")
		s.Env = bad
		if _, err := runT(t, s); !errors.Is(err, ErrRefused) {
			t.Errorf("%v: want a refusal, got %v", bad, err)
		}
	}
}

func TestRunStopsTheWholeGroupAtItsTimeout(t *testing.T) {
	s := spec("sh", "-c", "sleep 30 & echo $!; wait")
	s.Timeout = 300 * time.Millisecond
	start := time.Now()
	res := mustRun(t, s)
	if res.Status != -1 {
		t.Fatalf("status %d, want -1", res.Status)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("took %v", took)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(res.Stdout)))
	if err != nil {
		t.Fatalf("no pid in %q", res.Stdout)
	}
	deadline := time.Now().Add(3 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("the background sleep (%d) outlived the run", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestRunStopsWhenTheCallerCancels(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	s := spec("sleep", "30")
	s.Timeout = time.Hour
	start := time.Now()
	_, err := run(ctx, s, testPath)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 5*time.Second {
		t.Fatalf("a cancelled caller's run: %v after %v", err, time.Since(start))
	}
}

func TestRunKeepsSixteenMebibytesOfOutput(t *testing.T) {
	res := mustRun(t, spec("head", "-c", strconv.Itoa(MaxOutput+1<<20), "/dev/zero"))
	if res.Status != 0 || len(res.Stdout) != MaxOutput {
		t.Fatalf("status %d, kept %d bytes, want %d", res.Status, len(res.Stdout), MaxOutput)
	}
}

func TestRunRefusesGitShapesThatRunCode(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{
		{"-c", "alias.pwn=!touch x", "pwn"},
		{"-c", "core.sshCommand=touch x", "ls-remote", "ssh://example.invalid/x"},
		{"-c", "core.fsmonitor=touch x", "status"},
		{"-c", "core.hooksPath=/tmp/evil", "commit"},
		{"-c", "core.pager=less", "log"},
		{"-c", "pager.log=sh", "log"},
		{"-c", "include.path=/tmp/x", "status"},
		{"-c", "protocol.ext.allow=always", "fetch"},
		{"-c", "user.name=a\nb", "status"},
		{"-c"},
		{"--config-env=core.fsmonitor=HOME", "status"},
		{"--exec-path=/tmp", "status"},
		{"--exec-path", "status"},
		{"-p", "log"},
		{"--paginate", "log"},
		{"--version=x"},
		{"-C", "/etc", "status"},
		{"-C", "../other", "status"},
		{"-C"},
		{"--git-dir=/elsewhere/.git", "status"},
		{"--work-tree", "/", "status"},
		{"ls-remote", "--upload-pack=touch x", "."},
		{"ls-remote", "--upload-pa", "touch x", "."},
		{"ls-remote", "-u", "touch x", "."},
		{"fetch", "--upload-pack=touch x", "."},
		{"clone", "-c", "core.fsmonitor=touch x", ".", "y"},
		{"clone", "--config", "filter.x.smudge=sh", ".", "y"},
		{"clone", "-qc", "core.fsmonitor=sh", ".", "y"},
		{"clone", "-u", "sh", ".", "y"},
		{"clone", "--template=/tmp/t", ".", "y"},
		{"clone", "--separate-git-dir=/tmp/g", ".", "y"},
		{"log", "--output=/tmp/x"},
		{"log", "--outp", "/tmp/x"},
		{"format-patch", "-o", "/tmp", "-1"},
		{"push", "--receive-pack=sh", "origin"},
		{"push", "--exec=sh", "origin"},
		{"rebase", "--exec", "sh", "main"},
		{"rebase", "-x", "sh", "main"},
		{"grep", "-O", "x"},
		{"grep", "--open-files-in-pager", "x"},
		{"bisect", "run", "sh"},
		{"submodule", "foreach", "sh"},
		{"fast-export", "--export-marks=/tmp/m"},
		{"read-tree", "--index-output=/tmp/x.index", "HEAD"},
		{"config", "--global", "alias.x", "!sh"},
		{"config", "core.fsmonitor", "sh"},
		{"config", "set", "core.fsmonitor", "sh"},
		{"config", "--get", "x", "--unset", "y"},
		{"config", "--edit"},
		{"difftool", "--extcmd=sh"},
		{"credential", "fill"},
		{"help", "-w", "log"},
		{"maintenance", "start"},
		{"hook", "run", "pre-commit"},
		{"pwn"},
		{"lfs", "x"},
		{""},
	} {
		s := spec("git", args...)
		s.Cwd = dir
		if _, err := runT(t, s); !errors.Is(err, ErrRefused) {
			t.Errorf("git %q: want a refusal, got %v", args, err)
		}
	}
}

func TestGitAsPluginsUseItIsAllowed(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644)
	index := filepath.Join(dir, ".git", "glitched.index")
	for _, args := range [][]string{
		{"status", "--porcelain=v2", "--branch"},
		{"-C", filepath.Join(dir, "sub"), "log", "--oneline"},
		{"-C", "sub", "status"},
		{"-C", "sub/../sub", "status"},
		{"--git-dir", filepath.Join(dir, ".git"), "status"},
		{"-c", "core.quotepath=off", "-c", "color.ui=false", "-c", "core.pager=cat", "diff", "--no-ext-diff"},
		{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "commit", "-m", "x"},
		{"-c", "user.name=T", "-c", "user.email=t@example.invalid", "-c", "color.diff.meta=blue", "commit", "-m", "x"},
		{"switch", "-c", "feature"},
		{"log", "-c", "--format=%H"},
		{"log", "--oneline", "--exclude=x"},
		{"config", "--get", "user.email"},
		{"config", "get", "user.email"},
		{"config", "--list"},
		{"rev-parse", "--git-dir", "--show-toplevel"},
		{"fetch", "-u", "origin"},
		{"format-patch", "--to=a@example.com", "-1"},
		{"log", "--", "--output=not-an-option"},
		{"log", "--end-of-options", "-x"},
		{"read-tree", "--index-output=" + index, "-m", "HEAD"},
		{"read-tree", "--index-output", index, "HEAD"},
		{"bisect", "start"},
		{"submodule", "update", "--init"},
		{"--no-optional-locks", "status"},
		{"--no-pager", "log"},
		{"--version"},
		{},
	} {
		if err := gitArgs(args, dir); err != nil {
			t.Errorf("git %q: %v", args, err)
		}
	}
}

func TestGitIndexFileIsTheRepositorysOwn(t *testing.T) {
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	os.Mkdir(gitDir, 0o755)
	os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644)
	s := spec("printenv", "GIT_INDEX_FILE")
	s.Env = map[string]string{"GIT_INDEX_FILE": filepath.Join(gitDir, "glitched.index")}
	if res := mustRun(t, s); !strings.HasSuffix(string(res.Stdout), "glitched.index\n") {
		t.Fatalf("got %q", res.Stdout)
	}
	for _, bad := range []string{
		filepath.Join(dir, "glitched.index"),
		filepath.Join(gitDir, "config"),
		filepath.Join(gitDir, ".index"),
		"relative.index",
		gitDir + "/../.git/x.index",
	} {
		s.Env = map[string]string{"GIT_INDEX_FILE": bad}
		if _, err := runT(t, s); !errors.Is(err, ErrRefused) {
			t.Errorf("%s: want a refusal, got %v", bad, err)
		}
	}
}

// A repository's own fsmonitor and hooks never run for a plugin's git.
func TestAPlantedRepositoryRunsNothing(t *testing.T) {
	need(t, "git")
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	os.Mkdir(repo, 0o755)
	git := func(args ...string) {
		t.Helper()
		s := spec("git", args...)
		s.Cwd = repo
		if res := mustRun(t, s); res.Status != 0 {
			t.Fatalf("git %q: %d %s", args, res.Status, res.Stderr)
		}
	}
	git("init", "-q")
	fsmonitor := filepath.Join(dir, "fsmonitor-ran")
	hook := filepath.Join(dir, "hook-ran")
	f, _ := os.OpenFile(filepath.Join(repo, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("[core]\n\tfsmonitor = \"touch " + fsmonitor + "; false\"\n")
	f.Close()
	for _, h := range []string{"pre-commit", "post-commit", "commit-msg"} {
		os.WriteFile(filepath.Join(repo, ".git", "hooks", h), []byte("#!/bin/sh\ntouch "+hook+"\n"), 0o755)
	}
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("a"), 0o644)
	git("status", "--porcelain")
	git("add", "a.txt")
	git("-c", "user.name=T", "-c", "user.email=t@example.invalid", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "a")
	if _, err := os.Stat(fsmonitor); err == nil {
		t.Error("the repository's fsmonitor ran")
	}
	if _, err := os.Stat(hook); err == nil {
		t.Error("the repository's hook ran")
	}
}

// The rules follow the file that runs: a link named g to git is git.
func TestAGitByAnotherNameIsStillGit(t *testing.T) {
	need(t, "git")
	real, _ := find("git", testPath)
	bin := t.TempDir()
	if err := os.Symlink(real, filepath.Join(bin, "g")); err != nil {
		t.Fatal(err)
	}
	s := spec("g", "-c", "alias.x=!true", "x")
	if _, err := run(context.Background(), s, bin); !errors.Is(err, ErrRefused) {
		t.Fatalf("want a refusal, got %v", err)
	}
}

func TestGhNeverGivesATokenOrRunsAProgram(t *testing.T) {
	for _, args := range [][]string{
		{"auth", "token"},
		{"auth", "status", "--show-token"},
		{"auth", "status", "-t"},
		{"auth", "status", "-ht", "github.com"},
		{"auth", "login", "--with-token"},
		{"auth", "git-credential", "get"},
		{"auth", "setup-git"},
		{"auth"},
		{"alias", "set", "--shell", "pwn", "touch X"},
		{"alias", "list"},
		{"extension", "install", "evil/gh-evil"},
		{"extensions", "list"},
		{"ext", "install", "evil/gh-evil"},
		{"codespace", "ssh", "--", "-oProxyCommand=sh"},
		{"cs", "list"},
		{"config", "set", "browser", "sh"},
		{"config", "set", "pager", "sh"},
		{"config", "get", "oauth_token"},
		{"config", "clear-cache"},
		{"config"},
		{"repo", "clone", "a/b", "--", "--config", "core.fsmonitor=sh"},
		{"repo", "fork", "a/b", "--clone", "--", "--template=/tmp/t"},
		{"pwn"},
		{"-R", "a/b", "alias", "set", "x", "y"},
		{"--repo", "a/b", "pr", "list"},
	} {
		if _, err := runT(t, spec("gh", args...)); !errors.Is(err, ErrRefused) {
			t.Errorf("gh %q: want a refusal, got %v", args, err)
		}
	}
	for _, args := range [][]string{
		{"api", "graphql", "--input", "-"},
		{"api", "--method", "GET", "/user"},
		{"repo", "view", "--json", "nameWithOwner", "--jq", ".nameWithOwner"},
		{"repo", "clone", "a/b"},
		{"auth", "status"},
		{"auth", "status", "--hostname", "github.com"},
		{"config", "get", "git_protocol"},
		{"config", "list"},
		{"pr", "list", "-R", "a/b"},
		{"--version"},
		{"--help"},
		{},
	} {
		if err := ghArgs(args); err != nil {
			t.Errorf("gh %q: %v", args, err)
		}
	}
}

func TestGitFamilyRunsGetTheHardenedEnvironment(t *testing.T) {
	bin := t.TempDir()
	printenv, ok := find("printenv", testPath)
	if !ok {
		t.Skip("printenv isn't installed")
	}
	os.Symlink(printenv, filepath.Join(bin, "git-lfs"))
	res, err := run(context.Background(), Spec{Program: "git-lfs", Cwd: bin, Allowed: []string{"git-lfs"}}, bin)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=true", "PAGER=cat", "=core.fsmonitor\n", "=core.hooksPath\n", "=protocol.ext.allow\n"} {
		if !bytes.Contains(res.Stdout, []byte(want)) {
			t.Errorf("missing %q", want)
		}
	}
	res, _ = run(context.Background(), spec("printenv"), testPath)
	if bytes.Contains(res.Stdout, []byte("GIT_CONFIG_NOSYSTEM")) && os.Getenv("GIT_CONFIG_NOSYSTEM") == "" {
		t.Error("a plain program got git's environment")
	}
}

func TestRiskSaysWhatCanDoAnything(t *testing.T) {
	for _, p := range []string{"git", "gh", "sh", "bash", "zsh", "python3", "python3.12", "node", "osascript", "perl", "ruby", "GIT", "git.exe", "rg", "curl", "find", "env", "xargs", "some-tool"} {
		if got := Risk(p); got != RiskAnything {
			t.Errorf("Risk(%q) = %q", p, got)
		}
	}
	for _, p := range []string{"date", "uname", "jq", "cat", "echo", "sw_vers", "Date", "head.exe"} {
		if got := Risk(p); got != RiskHarmless {
			t.Errorf("Risk(%q) = %q", p, got)
		}
	}
}

func TestUserPathHasOnlyAbsoluteFolders(t *testing.T) {
	for _, d := range filepath.SplitList(userPath()) {
		if !filepath.IsAbs(d) {
			t.Errorf("relative folder %q on the PATH", d)
		}
	}
}
