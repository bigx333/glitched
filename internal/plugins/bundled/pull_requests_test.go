package bundled_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"glitched/internal/plugins"
	"glitched/internal/plugins/bundled"
	"glitched/internal/plugins/pack"
	"glitched/internal/plugins/widget"
	"glitched/internal/provider"
)

// github is GitHub as gh tells it, played by the test: the fake gh is this
// Go function, so no gh runs.
type github struct {
	mu        sync.Mutex
	calls     []ghCall
	missing   bool   // gh isn't installed
	auth      string // gh auth status's stderr when signed out, "" when signed in
	listErr   string // pr list's stderr, failing
	createErr string // pr create's stderr, failing
	prs       []map[string]any
}

type ghCall struct {
	Args  []string
	Stdin string
	Env   map[string]string
}

func (g *github) run(args []string, stdin string, env map[string]string) (any, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, ghCall{args, stdin, env})
	if g.missing {
		return nil, fmt.Errorf("%q isn't installed on this device (or isn't on your PATH)", "gh")
	}
	ok := func(out string) (any, error) { return map[string]any{"status": 0, "stdout": out, "stderr": ""}, nil }
	fail := func(err string) (any, error) { return map[string]any{"status": 1, "stdout": "", "stderr": err}, nil }
	switch strings.Join(args[:min(2, len(args))], " ") {
	case "auth status":
		if g.auth != "" {
			return fail(g.auth)
		}
		return ok("github.com\n  ✓ Logged in to github.com account octo\n")
	case "repo view":
		return ok(`{"defaultBranchRef":{"name":"main"}}`)
	case "pr list":
		if g.listErr != "" {
			return fail(g.listErr)
		}
		b, _ := json.Marshal(append([]map[string]any{}, g.prs...))
		return ok(string(b))
	case "pr create":
		if g.createErr != "" {
			return fail(g.createErr)
		}
		g.prs = append(g.prs, map[string]any{"number": 7, "title": args[slices.Index(args, "--title")+1], "url": "https://github.com/acme/widgets/pull/7",
			"state": "OPEN", "isDraft": slices.Contains(args, "--draft"), "headRefName": args[slices.Index(args, "--head")+1], "statusCheckRollup": []any{}})
		return ok("https://github.com/acme/widgets/pull/7\n")
	case "pr ready":
		for _, p := range g.prs {
			if fmt.Sprint(p["number"]) == args[2] {
				p["isDraft"] = false
			}
		}
		return ok("")
	}
	return fail("unknown command " + strings.Join(args, " "))
}

func (g *github) ran(prefix ...string) []ghCall {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []ghCall
	for _, c := range g.calls {
		if len(c.Args) >= len(prefix) && slices.Equal(c.Args[:len(prefix)], prefix) {
			out = append(out, c)
		}
	}
	return out
}

// pullRequests is the built-in Pull requests plugin on its own host, signed
// with a key of the test's: the release key signs it only once the
// maintainer runs cmd/sign-bundled.
type pullRequests struct {
	t      *testing.T
	h      *plugins.Host
	s      *plugins.Session
	q      chan func()
	gh     *github
	ctx    map[string]any
	mu     sync.Mutex
	toasts []string
	drafts []map[string]string
	copied []string
	asked  []plugins.AskRequest
	answer func(plugins.AskRequest) (plugins.AskAnswer, error)
}

func signedCopy(t *testing.T) (fs.FS, string) {
	t.Helper()
	root := t.TempDir()
	sub, err := fs.Sub(bundled.FS, "pull-requests")
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(root, "pull-requests")
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
	key, err := pack.LoadKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := pack.Sign(dst, key); err != nil {
		t.Fatal(err)
	}
	return os.DirFS(root), key.Public()
}

// openPullRequests shows the plugin a thread in work, as the header does.
func openPullRequests(t *testing.T, work string, gh *github) *pullRequests {
	t.Helper()
	p := &pullRequests{t: t, q: make(chan func(), 1024), gh: gh}
	fsys, release := signedCopy(t)
	p.h = plugins.Open(t.TempDir(), plugins.Options{
		Post:        func(fn func()) { p.q <- fn },
		Logf:        t.Logf,
		Bundled:     fsys,
		ReleaseKeys: []string{release},
		CallTimeout: 30 * time.Second,
		Do:          p.do,
		Run: func(ctx context.Context, id string, allowed []string, raw json.RawMessage) (any, error) {
			var a struct {
				Program string
				Args    []string
				Stdin   string
				Env     map[string]string
			}
			json.Unmarshal(raw, &a)
			switch {
			case !slices.Contains(allowed, a.Program):
				t.Errorf("ran %s, which the manifest doesn't name", a.Program)
			case a.Program == "gh":
				return gh.run(a.Args, a.Stdin, a.Env)
			case a.Program != "git":
				t.Errorf("ran %s %q", a.Program, a.Args)
				return nil, fmt.Errorf("not in this test")
			}
			return plugins.RunProgram(ctx, id, allowed, raw)
		},
		Ask: func(_ context.Context, _ string, q plugins.AskRequest) (plugins.AskAnswer, error) {
			p.mu.Lock()
			p.asked = append(p.asked, q)
			answer := p.answer
			p.mu.Unlock()
			if answer == nil {
				return plugins.AskAnswer{}, fmt.Errorf("%w: no ask in this test", provider.ErrUnavailable)
			}
			return answer(q)
		},
	})
	t.Cleanup(p.h.Close)
	p.s = p.h.App("pull-requests")
	if p.s == nil {
		t.Fatalf("Pull requests doesn't run: %+v", p.h.Installed())
	}
	p.ctx = map[string]any{"id": "t1", "title": "Fix the flaky test", "workspace": work, "local": true}
	p.s.SetItems("pr", []plugins.PlaceItem{{Key: "t1", Version: "1", Context: p.ctx}})
	p.settle()
	return p
}

func (p *pullRequests) do(_ string, e plugins.Effect) (any, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var m map[string]any
	json.Unmarshal(e.Args, &m)
	str := func(k string) string { s, _ := m[k].(string); return s }
	switch e.Type {
	case "toast":
		p.toasts = append(p.toasts, str("title")+": "+str("description"))
	case "composer.draft":
		p.drafts = append(p.drafts, map[string]string{"text": str("text"), "thread": str("thread")})
	case "clipboard.write":
		p.copied = append(p.copied, str("text"))
	case "models.list":
		return map[string]any{"models": []map[string]any{{"provider": "claude", "id": "claude-haiku-5-5", "name": "Haiku 5.5", "family": "Claude"}}}, nil
	}
	return nil, nil
}

// settle runs what the host posts till the plugin has nothing left to do.
func (p *pullRequests) settle() {
	p.t.Helper()
	deadline := time.After(30 * time.Second)
	for quiet := 0; quiet < 3; {
		select {
		case fn := <-p.q:
			fn()
			quiet = 0
		case <-time.After(20 * time.Millisecond):
			if p.h.Idle() && p.s.Answered("pr", "t1") != "" {
				quiet++
			}
		case <-deadline:
			p.t.Fatalf("timed out; told %q, failed %v", p.toasts, p.h.Failed("pull-requests"))
		}
	}
	if err := p.h.Failed("pull-requests"); err != nil {
		p.t.Fatalf("the plugin failed: %v", err)
	}
}

func (p *pullRequests) view() *widget.View {
	if n := p.s.PlaceView("pr", "t1"); n != nil {
		return &widget.View{Body: n}
	}
	return nil
}

// text is every text the header's copy holds, its popover and dialog too.
func (p *pullRequests) text() string {
	v := p.view()
	if v == nil {
		return ""
	}
	var b strings.Builder
	v.Body.Walk(func(n *widget.Node) {
		if s := n.Text + n.Label; s != "" {
			b.WriteString(s + "|")
		}
	})
	return b.String()
}

// val is what the dialog's field id holds.
func val(v *widget.View, id string) any {
	n := v.Find(id)
	if n == nil {
		return nil
	}
	var x any
	json.Unmarshal(n.Value, &x)
	return x
}

func (p *pullRequests) click(handler, node string, values map[string]any) {
	p.t.Helper()
	ref := &plugins.PlaceRef{ID: "pr", At: "thread.header", Key: "t1", Context: p.ctx}
	p.s.Dispatch(plugins.UIEvent{Type: "click", Handler: handler, Node: node, Place: ref}, values)
	p.settle()
}

// onGitHub makes work's origin a GitHub repository whose pushes land in
// the bare origin, and checks out branch.
func onGitHub(t *testing.T, work, origin, branch string) {
	t.Helper()
	git(t, work, "remote", "set-url", "origin", "https://github.com/acme/widgets.git")
	git(t, work, "config", "url."+origin+".pushInsteadOf", "https://github.com/acme/widgets.git")
	if branch != "main" {
		git(t, work, "checkout", "-q", "-b", branch)
	}
}

func openPR(number int, draft bool, checks ...map[string]any) map[string]any {
	return map[string]any{"number": number, "title": "Fix the flaky test", "url": fmt.Sprintf("https://github.com/acme/widgets/pull/%d", number),
		"state": "OPEN", "isDraft": draft, "reviewDecision": "REVIEW_REQUIRED", "mergeable": "CONFLICTING", "headRefName": "fix-flaky", "statusCheckRollup": checks}
}

var (
	lint  = map[string]any{"__typename": "CheckRun", "name": "lint", "status": "COMPLETED", "conclusion": "FAILURE", "detailsUrl": "https://github.com/acme/widgets/actions/runs/1"}
	build = map[string]any{"__typename": "CheckRun", "name": "build", "status": "IN_PROGRESS", "conclusion": "", "detailsUrl": "https://github.com/acme/widgets/actions/runs/2"}
	unit  = map[string]any{"__typename": "StatusContext", "context": "test", "state": "SUCCESS", "targetUrl": "https://ci.example/test"}
)

func TestTheHeaderShowsNothingWithNoPullRequestToShowOrMake(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(t *testing.T) (work string, local bool)
	}{
		{"not on this machine", func(t *testing.T) (string, bool) {
			work, origin := repo(t)
			onGitHub(t, work, origin, "fix-flaky")
			return work, false
		}},
		{"not a repository", func(t *testing.T) (string, bool) { repo(t); return t.TempDir(), true }},
		{"not on GitHub", func(t *testing.T) (string, bool) {
			work, _ := repo(t)
			git(t, work, "checkout", "-q", "-b", "fix-flaky")
			return work, true
		}},
		{"on the base branch", func(t *testing.T) (string, bool) {
			work, origin := repo(t)
			onGitHub(t, work, origin, "main")
			return work, true
		}},
		{"nothing ahead and clean", func(t *testing.T) (string, bool) {
			work, origin := repo(t)
			onGitHub(t, work, origin, "fix-flaky")
			git(t, work, "checkout", "-q", "--", ".")
			os.Remove(filepath.Join(work, "new.txt"))
			return work, true
		}},
		{"detached", func(t *testing.T) (string, bool) {
			work, origin := repo(t)
			onGitHub(t, work, origin, "main")
			git(t, work, "checkout", "-q", "--detach")
			return work, true
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			work, local := c.setup(t)
			gh := &github{}
			p := openPullRequests(t, work, gh)
			if !local {
				p.ctx["local"] = false
				p.s.SetItems("pr", []plugins.PlaceItem{{Key: "t1", Version: "2", Context: p.ctx}})
				p.settle()
			}
			if v := p.view(); v != nil || len(p.toasts) != 0 {
				t.Errorf("the header shows %q, told %q", p.text(), p.toasts)
			}
			if c.name == "nothing ahead and clean" && len(gh.ran("pr", "list")) != 1 {
				t.Errorf("gh ran %+v, want one pr list", gh.calls)
			}
		})
	}
}

func TestGhMissingOrSignedOutShowsAWarningPill(t *testing.T) {
	for _, c := range []struct {
		gh   *github
		want string
	}{
		{&github{missing: true}, "Install the GitHub CLI to see and open pull requests: `brew install gh`, then `gh auth login`."},
		{&github{auth: "You are not logged into any GitHub hosts. To log in, run: gh auth login"}, "Sign gh in to GitHub: run `gh auth login` in a terminal, then Refresh."},
	} {
		work, origin := repo(t)
		onGitHub(t, work, origin, "fix-flaky")
		p := openPullRequests(t, work, c.gh)
		v := p.view()
		if v == nil || v.Body.Style != "ghost" || v.Body.Popover == nil || !strings.Contains(p.text(), "PR|") {
			t.Fatalf("the header: %q", p.text())
		}
		if !strings.Contains(p.text(), c.want) || v.Find("refresh") == nil {
			t.Errorf("the popover: %q, want %q and Refresh", p.text(), c.want)
		}
		if len(c.gh.ran("pr")) != 0 {
			t.Errorf("gh ran %+v past auth status", c.gh.calls)
		}
	}
}

func TestABranchAheadOrDirtyOffersCreatePR(t *testing.T) {
	work, origin := repo(t)
	onGitHub(t, work, origin, "fix-flaky")
	p := openPullRequests(t, work, &github{})
	if b := p.view(); b == nil || b.Body.ID != "create" || b.Body.Style != "outline" || b.Body.Label != "Create PR" {
		t.Fatalf("dirty: the header is %q", p.text())
	}

	work, origin = repo(t)
	onGitHub(t, work, origin, "fix-flaky")
	git(t, work, "commit", "-qam", "Retry the flaky test")
	os.Remove(filepath.Join(work, "new.txt"))
	p = openPullRequests(t, work, &github{})
	if b := p.view(); b == nil || b.Body.ID != "create" {
		t.Fatalf("a commit ahead: the header is %q", p.text())
	}
	p.click("create.open", "create", nil)
	v := p.view()
	if val(v, "title") != "Retry the flaky test" || !strings.Contains(fmt.Sprint(val(v, "body")), "## Summary\n\n- Retry the flaky test") || val(v, "base") != "main" {
		t.Errorf("the dialog opens with %q", p.text())
	}
	if v.Find("commit") != nil {
		t.Error("a clean tree offers to commit")
	}
}

func TestAPullRequestShowsItsStateReviewAndChecks(t *testing.T) {
	work, origin := repo(t)
	onGitHub(t, work, origin, "fix-flaky")
	closed := map[string]any{"number": 3, "title": "Old", "url": "https://github.com/acme/widgets/pull/3", "state": "CLOSED", "headRefName": "fix-flaky"}
	gh := &github{prs: []map[string]any{closed, openPR(12, false, unit, lint, build)}}
	p := openPullRequests(t, work, gh)
	v := p.view()
	if v == nil || v.Body.Popover == nil || !strings.Contains(p.text(), "#12|") {
		t.Fatalf("the header: %q", p.text())
	}
	if icons := v.Body.Children; len(icons) != 3 || icons[0].Color != "$success" || icons[2].Name != "close" || icons[2].Color != "$danger" {
		t.Errorf("the pill: %+v", icons)
	}
	text := p.text()
	for _, want := range []string{"Fix the flaky test|", "Open|", "Awaiting review|", "Has conflicts|", "lint · Failed|", "build · Running|", "Show 1 passed|", "Open on GitHub|", "Copy link|", "Refresh|", "Ask the agent to fix|"} {
		if !strings.Contains(text, want) {
			t.Errorf("the popover doesn't say %q: %s", want, text)
		}
	}
	if strings.Index(text, "lint") > strings.Index(text, "build") || strings.Contains(text, "test · Passed") || v.Find("ready") != nil {
		t.Errorf("failed first, passed hidden, no Ready for review on an open one: %s", text)
	}
	if call := gh.ran("pr", "list"); len(call) != 1 || !slices.Equal(call[0].Args, []string{"pr", "list", "--repo", "acme/widgets", "--head", "fix-flaky", "--state", "all", "--limit", "10",
		"--json", "number,title,url,state,isDraft,reviewDecision,mergeable,statusCheckRollup,headRefName"}) {
		t.Errorf("gh pr list: %+v", call)
	}
	for _, c := range gh.calls {
		if c.Env["GH_PROMPT_DISABLED"] != "1" {
			t.Errorf("gh %q ran without GH_PROMPT_DISABLED=1", c.Args)
		}
	}
	p.click("popover.toggle", "pr", nil)
	p.click("passed.toggle", "passed", nil)
	if !strings.Contains(p.text(), "test · Passed|") || p.view().Find("passed") == nil {
		t.Errorf("Show 1 passed shows %q", p.text())
	}
	p.click("copy", "copy", nil)
	if len(p.copied) != 1 || p.copied[0] != "https://github.com/acme/widgets/pull/12" {
		t.Errorf("copied %q", p.copied)
	}
}

func TestReadyForReviewMarksADraftReady(t *testing.T) {
	work, origin := repo(t)
	onGitHub(t, work, origin, "fix-flaky")
	gh := &github{prs: []map[string]any{openPR(12, true)}}
	p := openPullRequests(t, work, gh)
	if !strings.Contains(p.text(), "Draft|") || p.view().Find("ready") == nil || p.view().Body.Children[0].Color != "$text.muted" {
		t.Fatalf("a draft: %q", p.text())
	}
	p.click("ready", "ready", nil)
	if call := gh.ran("pr", "ready"); len(call) != 1 || !slices.Equal(call[0].Args, []string{"pr", "ready", "12", "--repo", "acme/widgets"}) {
		t.Fatalf("gh pr ready: %+v", call)
	}
	if strings.Contains(p.text(), "Draft|") || !strings.Contains(p.text(), "Open|") || p.view().Find("ready") != nil {
		t.Errorf("after Ready for review: %q", p.text())
	}
}

func TestAskTheAgentToFixDraftsThePromptInTheThread(t *testing.T) {
	work, origin := repo(t)
	onGitHub(t, work, origin, "fix-flaky")
	p := openPullRequests(t, work, &github{prs: []map[string]any{openPR(12, false, lint, unit)}})
	p.click("popover.toggle", "pr", nil)
	p.click("fix", "fix", nil)
	if len(p.drafts) != 1 || p.drafts[0]["thread"] != "t1" {
		t.Fatalf("drafted %+v", p.drafts)
	}
	text := p.drafts[0]["text"]
	for _, want := range []string{"#12", "https://github.com/acme/widgets/pull/12", "- lint: https://github.com/acme/widgets/actions/runs/1", "`gh pr checks 12`"} {
		if !strings.Contains(text, want) {
			t.Errorf("the draft doesn't say %q: %s", want, text)
		}
	}
	if strings.Contains(text, "- test") {
		t.Errorf("the draft names a check that passed: %s", text)
	}
	if p.view().Body.Open {
		t.Error("the popover stays open")
	}
}

// A refresh GitHub fails keeps the pull request shown, saying why.
func TestARateLimitKeepsTheLastPullRequest(t *testing.T) {
	work, origin := repo(t)
	onGitHub(t, work, origin, "fix-flaky")
	gh := &github{prs: []map[string]any{openPR(12, false, unit)}}
	p := openPullRequests(t, work, gh)
	gh.mu.Lock()
	gh.listErr = "GraphQL: API rate limit exceeded for user ID 1."
	gh.mu.Unlock()
	p.click("popover.toggle", "pr", nil)
	p.click("refresh", "refresh", nil)
	if text := p.text(); !strings.Contains(text, "#12|") || !strings.Contains(text, "Couldn't refresh: GitHub's rate limit was reached; try again in a few minutes|") {
		t.Errorf("after a rate limit: %q", text)
	}
}

// createDialog opens Create PR on a branch with a.txt changed and new.txt
// new, and fills its fields.
func createDialog(t *testing.T, gh *github) (*pullRequests, string, string) {
	t.Helper()
	work, origin := repo(t)
	onGitHub(t, work, origin, "fix-flaky")
	p := openPullRequests(t, work, gh)
	p.click("create.open", "create", nil)
	v := p.view()
	if v == nil || v.Find("create-dialog") == nil || v.Find("commit") == nil || val(v, "commit") != true || val(v, "title") != "Fix the flaky test" {
		t.Fatalf("the dialog: %q", p.text())
	}
	if !strings.Contains(p.text(), "Commit 2 changed files first|") || !strings.Contains(p.text(), "Pushes fix-flaky to origin/fix-flaky, which it starts tracking.") {
		t.Errorf("the dialog says %q", p.text())
	}
	return p, work, origin
}

func TestCreatePRCommitsPushesAndOpensIt(t *testing.T) {
	gh := &github{}
	p, work, origin := createDialog(t, gh)
	body := "## Summary\n\n- Retry the flaky test once\n"
	p.click("create.go", "go", map[string]any{"title": "Retry the flaky test", "body": body, "base": "main", "draft": true, "commit": true})
	if git(t, work, "status", "--porcelain") != "" || git(t, work, "log", "-1", "--format=%s") != "Retry the flaky test" {
		t.Errorf("the commit: %s", git(t, work, "log", "-1", "--format=%s"))
	}
	if git(t, origin, "rev-parse", "refs/heads/fix-flaky") != git(t, work, "rev-parse", "HEAD") {
		t.Error("origin's fix-flaky isn't the commit")
	}
	if git(t, work, "config", "branch.fix-flaky.merge") != "refs/heads/fix-flaky" {
		t.Error("the branch doesn't track what it pushed to")
	}
	call := gh.ran("pr", "create")
	want := []string{"pr", "create", "--repo", "acme/widgets", "--head", "fix-flaky", "--base", "main", "--title", "Retry the flaky test", "--body-file", "-", "--draft"}
	if len(call) != 1 || !slices.Equal(call[0].Args, want) || call[0].Stdin != body || call[0].Env["GH_PROMPT_DISABLED"] != "1" {
		t.Fatalf("gh pr create: %+v, want %q with the body on stdin", call, want)
	}
	if text := p.text(); !strings.Contains(text, "#7|") || !strings.Contains(text, "Draft|") || p.view().Find("create-dialog") != nil {
		t.Errorf("after Create: %q", text)
	}
	if len(p.toasts) != 1 || p.toasts[0] != "Opened a pull request: https://github.com/acme/widgets/pull/7" {
		t.Errorf("told %q", p.toasts)
	}
}

func TestCreatePRWithoutCommittingPushesWhatIsCommitted(t *testing.T) {
	gh := &github{}
	p, work, origin := createDialog(t, gh)
	git(t, work, "commit", "-qam", "Only a.txt")
	p.click("create.go", "go", map[string]any{"title": "Only a.txt", "commit": false})
	if git(t, work, "status", "--porcelain") != "?? new.txt" || git(t, origin, "log", "-1", "--format=%s", "fix-flaky") != "Only a.txt" {
		t.Errorf("status %q, origin at %q", git(t, work, "status", "--porcelain"), git(t, origin, "log", "-1", "--format=%s", "fix-flaky"))
	}
	if call := gh.ran("pr", "create"); len(call) != 1 || slices.Contains(call[0].Args, "--draft") {
		t.Errorf("gh pr create: %+v", call)
	}
}

func TestCreatePRSaysWhatWasDoneBeforeAStepFailed(t *testing.T) {
	p, work, origin := createDialog(t, &github{})
	os.RemoveAll(origin)
	p.click("create.go", "go", nil)
	sha := git(t, work, "rev-parse", "--short", "HEAD")
	if e := p.view().Find("error"); e == nil || !strings.HasPrefix(e.Text, "Committed "+sha+"; push to origin/fix-flaky failed: ") {
		t.Errorf("a push that fails: %q", p.text())
	}

	gh := &github{createErr: "pull request create failed: GraphQL: Base ref must be a branch (createPullRequest)"}
	p, work, _ = createDialog(t, gh)
	p.click("create.go", "go", map[string]any{"base": "nope"})
	sha = git(t, work, "rev-parse", "--short", "HEAD")
	if e := p.view().Find("error"); e == nil || e.Text != "Committed "+sha+" and pushed; gh pr create failed: pull request create failed: GraphQL: Base ref must be a branch (createPullRequest)" {
		t.Errorf("gh pr create that fails: %q", p.text())
	}
	if call := gh.ran("pr", "create"); len(call) != 1 || call[0].Args[7] != "nope" {
		t.Errorf("gh pr create: %+v", call)
	}
	p.click("create.go", "go", nil)
	if e := p.view().Find("error"); e == nil || !strings.HasPrefix(e.Text, "Pushed; gh pr create failed: ") {
		t.Errorf("again, with nothing to commit: %q", p.text())
	}
}

// A pull request gh says exists already is shown, never made twice.
func TestCreatePRShowsOneThatExists(t *testing.T) {
	gh := &github{createErr: "a pull request for branch \"fix-flaky\" into branch \"main\" already exists:\nhttps://github.com/acme/widgets/pull/12"}
	p, _, _ := createDialog(t, gh)
	gh.mu.Lock()
	gh.prs = []map[string]any{openPR(12, false)}
	gh.mu.Unlock()
	p.click("create.go", "go", nil)
	if text := p.text(); !strings.Contains(text, "#12|") || p.view().Find("create-dialog") != nil || len(p.toasts) != 0 {
		t.Errorf("after an existing one: %q, told %q", text, p.toasts)
	}
}

func TestCreatePRWithGhMissingSaysSo(t *testing.T) {
	gh := &github{}
	p, _, _ := createDialog(t, gh)
	gh.mu.Lock()
	gh.missing = true
	gh.mu.Unlock()
	p.click("create.go", "go", nil)
	if e := p.view().Find("error"); e == nil || !strings.HasSuffix(e.Text, "and pushed; gh pr create failed: \"gh\" isn't installed on this device (or isn't on your PATH)") {
		t.Errorf("gh missing at create: %q", p.text())
	}
}

func TestWriteWithAIFillsTheTitleAndDescription(t *testing.T) {
	p, _, _ := createDialog(t, &github{})
	p.answer = func(q plugins.AskRequest) (plugins.AskAnswer, error) {
		return plugins.AskAnswer{Text: "```json\n{\"title\": \"Retry the flaky test once\", \"body\": \"## Summary\\n\\n- Retries it\\n\\n## Testing\\n\\nNot stated.\"}\n```", Provider: "claude", Model: "claude-haiku-5-5"}, nil
	}
	p.click("create.write", "write", map[string]any{"title": "Mine"})
	v := p.view()
	if val(v, "title") != "Retry the flaky test once" || val(v, "body") != "## Summary\n\n- Retries it\n\n## Testing\n\nNot stated." {
		t.Errorf("the fields: %q, %q", val(v, "title"), val(v, "body"))
	}
	if by := v.Find("by"); by == nil || by.Text != "Written by Claude Haiku 5.5" {
		t.Errorf("says %q", p.text())
	}
	if len(p.asked) != 1 {
		t.Fatalf("asked %d times", len(p.asked))
	}
	q := p.asked[0]
	for _, want := range []string{"from fix-flaky into main", "titled: Fix the flaky test", "a.txt", "-one\n+two", "?? new.txt"} {
		if !strings.Contains(q.Prompt, want) {
			t.Errorf("the prompt doesn't hold %q:\n%s", want, q.Prompt)
		}
	}
	if !strings.Contains(q.System, "## Summary") || q.Provider != "" || q.Model != "" {
		t.Errorf("asked %+v; want the default provider and model", q)
	}
}

func TestWriteWithAISaysWhyItCant(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{fmt.Errorf("%w: acp can't answer a prompt on its own", provider.ErrUnsupported), "The provider of your default model can't write this on its own yet."},
		{fmt.Errorf("%w: Claude Code is not installed", provider.ErrUnavailable), "No model can write this here: Claude Code isn't installed."},
	} {
		p, _, _ := createDialog(t, &github{})
		p.answer = func(plugins.AskRequest) (plugins.AskAnswer, error) { return plugins.AskAnswer{}, c.err }
		p.click("create.write", "write", nil)
		if e := p.view().Find("error"); e == nil || !strings.HasPrefix(e.Text, c.want) || val(p.view(), "title") != "Fix the flaky test" {
			t.Errorf("%v: %q", c.err, p.text())
		}
	}
}
