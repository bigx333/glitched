package main

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"glitched/internal/daemon"
	"glitched/internal/plugins"
	"glitched/internal/plugins/bundled"
	"glitched/internal/plugins/pack"
	"glitched/internal/provider/fake"
)

// githubBranch is a checkout on fix-flaky whose origin is a GitHub
// repository, holding main as a bare repository here does.
func githubBranch(t *testing.T) string {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	os.WriteFile(cfg, []byte("[user]\n\tname = T\n\temail = t@example.invalid\n"), 0o644)
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	origin, work := filepath.Join(t.TempDir(), "origin.git"), filepath.Join(t.TempDir(), "work")
	git := func(dir string, args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	git(".", "init", "-q", "--bare", origin)
	git(".", "init", "-q", "-b", "main", work)
	os.WriteFile(filepath.Join(work, "a.txt"), []byte("one\n"), 0o644)
	git(work, "add", "a.txt")
	git(work, "commit", "-qm", "first")
	git(work, "remote", "add", "origin", "https://github.com/acme/widgets.git")
	git(work, "-c", "url."+origin+".pushInsteadOf=https://github.com/acme/widgets.git", "push", "-q", "origin", "main")
	git(work, "checkout", "-q", "-b", "fix-flaky")
	return work
}

// installOwn installs the built-in id as the user's own plugin in dir,
// signed with the device's key: the release key signs the built-in only
// once the maintainer runs cmd/sign-bundled.
func installOwn(t *testing.T, dir, id string) {
	t.Helper()
	sub, err := fs.Sub(bundled.FS, id)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(plugins.Dir(dir), id)
	err = fs.WalkDir(sub, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || p == pack.SignatureFile {
			return err
		}
		b, err := fs.ReadFile(sub, p)
		if err != nil {
			return err
		}
		os.MkdirAll(filepath.Join(dst, filepath.Dir(p)), 0o755)
		return os.WriteFile(filepath.Join(dst, p), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	key, err := pack.LoadKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := pack.Sign(dst, key); err != nil {
		t.Fatal(err)
	}
}

// playGh answers glitched.run's gh as GitHub would for a branch with an
// open pull request, #12, whose lint fails; any other program runs.
func playGh(t *testing.T) func() []string {
	var mu sync.Mutex
	var ran []string
	was := runPluginProgram
	runPluginProgram = func(ctx context.Context, id string, allowed []string, raw json.RawMessage) (any, error) {
		var a struct {
			Program string
			Args    []string
		}
		json.Unmarshal(raw, &a)
		if a.Program != "gh" {
			return was(ctx, id, allowed, raw)
		}
		mu.Lock()
		ran = append(ran, strings.Join(a.Args, " "))
		mu.Unlock()
		out := ""
		switch strings.Join(a.Args[:2], " ") {
		case "repo view":
			out = `{"defaultBranchRef":{"name":"main"}}`
		case "pr list":
			out = `[{"number":12,"title":"Fix the flaky test","url":"https://github.com/acme/widgets/pull/12","state":"OPEN","isDraft":false,"reviewDecision":"APPROVED","mergeable":"MERGEABLE","headRefName":"fix-flaky",
				"headRepository":{"name":"widgets"},"headRepositoryOwner":{"login":"acme"},"statusCheckRollup":[{"__typename":"CheckRun","name":"lint","status":"COMPLETED","conclusion":"FAILURE","detailsUrl":"https://github.com/acme/widgets/actions/runs/1"}]}]`
		}
		return map[string]any{"status": 0, "stdout": out, "stderr": ""}, nil
	}
	t.Cleanup(func() { runPluginProgram = was })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), ran...)
	}
}

// The header of a thread on a branch with a pull request shows it, as the
// built-in Pull requests draws it in the thread.header slot, and a click
// opens its popover.
func TestTheHeaderShowsTheBranchsPullRequest(t *testing.T) {
	work := githubBranch(t)
	ran := playGh(t)
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(nil))
	seed(t, d, work, "Fix the flaky test")
	installOwn(t, dir, "pull-requests")
	h := start(t, dir)
	for _, c := range []string{"threads:read", "programs"} {
		if err := h.a.plugins.SetGranted("pull-requests", c, true); err != nil {
			t.Fatal(err)
		}
	}
	h.a.select_(h.thread("Fix the flaky test"))
	h.until("the pull request's pill", func() bool { h.tt.Frame(); return h.tt.HasText("#12") })
	if h.tt.HasText("Open on GitHub") {
		t.Fatal("the popover shows before a click")
	}
	if err := h.tt.Click("#12"); err != nil {
		t.Fatal(err)
	}
	settleTester(h.tt)
	h.until("the popover", func() bool { h.tt.Frame(); return h.tt.HasText("Open on GitHub") })
	for _, want := range []string{"Approved", "lint · Failed", "Ask the agent to fix"} {
		if !h.tt.HasText(want) {
			t.Errorf("the popover doesn't say %q: %q", want, h.tt.Texts())
		}
	}
	if got := ran(); len(got) < 3 || got[0] != "auth status --active --hostname github.com" {
		t.Errorf("gh ran %q", got)
	}
}
