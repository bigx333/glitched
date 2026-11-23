package bundled_test

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
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
	inactive  string // gh auth status's stderr for a signed-out account that isn't the active one
	listErr   string // pr list's stderr, failing
	createErr string // pr create's stderr, failing
	createLag bool   // pr create opens the pull request but answers after its timeout
	createEOF bool   // pr create opens the pull request but its answer is lost
	createTo  string // the base pr create opens it into, whatever it's asked
	base      string // the repository's default branch, "" for main
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
		if g.inactive != "" && !slices.Contains(args, "--active") {
			return fail(g.inactive)
		}
		return ok("github.com\n  ✓ Logged in to github.com account octo\n")
	case "repo view":
		return ok(`{"defaultBranchRef":{"name":"` + cmp.Or(g.base, "main") + `"}}`)
	case "pr list":
		if g.listErr != "" {
			return fail(g.listErr)
		}
		var prs []map[string]any
		for _, p := range g.prs {
			if args[slices.Index(args, "--state")+1] != "open" || p["state"] == "OPEN" {
				prs = append(prs, p)
			}
		}
		if i := slices.Index(args, "--limit"); i >= 0 {
			n, _ := strconv.Atoi(args[i+1])
			prs = prs[:min(n, len(prs))]
		}
		b, _ := json.Marshal(append([]map[string]any{}, prs...))
		return ok(string(b))
	case "pr create":
		if g.createErr != "" {
			return fail(g.createErr)
		}
		g.prs = append(g.prs, map[string]any{"number": 7, "title": args[slices.Index(args, "--title")+1], "url": "https://github.com/acme/widgets/pull/7",
			"state": "OPEN", "isDraft": slices.Contains(args, "--draft"), "headRefName": args[slices.Index(args, "--head")+1], "statusCheckRollup": []any{},
			"headRepository": map[string]any{"name": "widgets"}, "headRepositoryOwner": map[string]any{"login": "acme"},
			"baseRefName": cmp.Or(g.createTo, args[slices.Index(args, "--base")+1])})
		if g.createLag {
			return map[string]any{"status": -1, "stdout": "", "stderr": ""}, nil
		}
		if g.createEOF {
			return fail("Post \"https://api.github.com/graphql\": EOF")
		}
		return ok("https://github.com/acme/widgets/pull/7\n")
	case "pr view":
		for _, p := range g.prs {
			if fmt.Sprint(p["number"]) == args[2] {
				b, _ := json.Marshal(p)
				return ok(string(b))
			}
		}
		return fail("GraphQL: Could not resolve to a PullRequest with the number of " + args[2] + ". (repository.pullRequest)")
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
	runs   []run
	asks   []time.Time // each ask's deadline
	// before runs ahead of each program the plugin runs; a result it
	// gives is the program's instead.
	before    func(args []string) any
	effects   []string      // the app calls the plugin made, by type
	toastLag  time.Duration // how long the app takes to show a toast
	modelsLag time.Duration // how long the app takes to list the models
}

// run is a program the plugin ran: when, where, and the seconds it gave it.
type run struct {
	At      time.Time
	Args    []string
	Cwd     string
	Timeout float64
}

// callTimeout is how long the host waits for an update, the app's 30 s
// but where a test waits less.
var callTimeout = 30 * time.Second

func signedCopy(t *testing.T, id string) (fs.FS, string) {
	t.Helper()
	root := t.TempDir()
	sub, err := fs.Sub(bundled.FS, id)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(root, id)
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
	fsys, release := signedCopy(t, "pull-requests")
	p.h = plugins.Open(t.TempDir(), plugins.Options{
		Post:        func(fn func()) { p.q <- fn },
		Logf:        t.Logf,
		Bundled:     fsys,
		ReleaseKeys: []string{release},
		CallTimeout: callTimeout,
		Do:          p.do,
		Run: func(ctx context.Context, id string, allowed, denied []string, raw json.RawMessage) (any, error) {
			var a struct {
				Program string
				Args    []string
				Cwd     string
				Stdin   string
				Timeout float64
				Env     map[string]string
			}
			json.Unmarshal(raw, &a)
			p.mu.Lock()
			p.runs = append(p.runs, run{time.Now(), append([]string{a.Program}, a.Args...), a.Cwd, a.Timeout})
			before := p.before
			p.mu.Unlock()
			if before != nil {
				if r := before(append([]string{a.Program}, a.Args...)); r != nil {
					return r, nil
				}
			}
			switch {
			case !slices.Contains(allowed, a.Program):
				t.Errorf("ran %s, which the manifest doesn't name", a.Program)
			case a.Program == "gh":
				return gh.run(a.Args, a.Stdin, a.Env)
			case a.Program == "git" && a.Args[0] == "push":
				return pushToOrigin(ctx, a.Cwd, a.Args, a.Timeout)
			case a.Program != "git":
				t.Errorf("ran %s %q", a.Program, a.Args)
				return nil, fmt.Errorf("not in this test")
			}
			return plugins.RunProgram(ctx, allowed, denied, raw)
		},
		Ask: func(ctx context.Context, _ string, q plugins.AskRequest) (plugins.AskAnswer, error) {
			at, _ := ctx.Deadline()
			p.mu.Lock()
			p.asked = append(p.asked, q)
			p.asks = append(p.asks, at)
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
	p.effects = append(p.effects, e.Type)
	switch e.Type {
	case "toast":
		time.Sleep(p.toastLag)
		p.toasts = append(p.toasts, str("title")+": "+str("description"))
	case "composer.draft":
		p.drafts = append(p.drafts, map[string]string{"text": str("text"), "thread": str("thread")})
	case "clipboard.write":
		p.copied = append(p.copied, str("text"))
	case "models.list":
		time.Sleep(p.modelsLag)
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

// drain runs what the host posts till it has been idle a while, for when
// no copy is shown to be answered.
func (p *pullRequests) drain() {
	p.t.Helper()
	deadline := time.After(10 * time.Second)
	for quiet := 0; quiet < 5; {
		select {
		case fn := <-p.q:
			fn()
			quiet = 0
		case <-time.After(20 * time.Millisecond):
			if p.h.Idle() {
				quiet++
			}
		case <-deadline:
			p.t.Fatal("the host didn't settle")
		}
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
	git(t, work, "config", "test.origin", origin)
	if branch != "main" {
		git(t, work, "checkout", "-q", "-b", branch)
	}
}

// pushToOrigin is the network for a push the plugin runs: it lands in the
// bare origin onGitHub named, while every other git the plugin runs sees
// github.com, as a push URL that git rewrites would show it.
func pushToOrigin(ctx context.Context, cwd string, args []string, timeout float64) (any, error) {
	origin, err := exec.Command("git", "-C", cwd, "config", "test.origin").Output()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout*float64(time.Second)))
	defer cancel()
	rewrite := "url." + strings.TrimSpace(string(origin)) + ".pushInsteadOf=https://github.com/acme/widgets.git"
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", cwd, "-c", rewrite}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_ALLOW_PROTOCOL=file")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	status := 0
	if ctx.Err() != nil {
		status = -1
	} else if e, ok := err.(*exec.ExitError); ok {
		status = e.ExitCode()
	} else if err != nil {
		return nil, err
	}
	return map[string]any{"status": status, "stdout": string(out), "stderr": stderr.String()}, nil
}

func openPR(number int, draft bool, checks ...map[string]any) map[string]any {
	return map[string]any{"number": number, "title": "Fix the flaky test", "url": fmt.Sprintf("https://github.com/acme/widgets/pull/%d", number),
		"state": "OPEN", "isDraft": draft, "reviewDecision": "REVIEW_REQUIRED", "mergeable": "CONFLICTING", "headRefName": "fix-flaky", "statusCheckRollup": checks,
		"headRepository": map[string]any{"name": "widgets"}, "headRepositoryOwner": map[string]any{"login": "acme"}, "baseRefName": "main"}
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
		{"not a folder", func(t *testing.T) (string, bool) { return filepath.Join(t.TempDir(), "gone"), true }},
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
			if lists := gh.ran("pr", "list"); c.name == "nothing ahead and clean" && (len(lists) != 2 || !slices.Contains(lists[0].Args, "open") || !slices.Contains(lists[1].Args, "all")) {
				t.Errorf("gh ran %+v, want a pr list of the open ones, then of all", gh.calls)
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
	if pill := v.Body.Children; len(pill) != 2 || pill[0].Name != "pull-request" || pill[0].Color != "$danger" || pill[1].Text != "#12" {
		t.Errorf("the pill: %+v, want the pull request icon in the failing checks' red, and #12", pill)
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
	if call := gh.ran("pr", "list"); len(call) != 1 || !slices.Equal(call[0].Args, []string{"pr", "list", "--repo", "acme/widgets", "--head", "fix-flaky", "--state", "open", "--limit", "10",
		"--json", "number,title,url,state,isDraft,reviewDecision,mergeable,statusCheckRollup,headRefName,headRepository,headRepositoryOwner,baseRefName"}) {
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

// Create commits on the dialog's branch or nowhere: a workspace that moved
// to another branch while the dialog was open is told, and left as it is.
func TestCreatePRCommitsOnlyOnTheDialogsBranch(t *testing.T) {
	gh := &github{}
	p, work, origin := createDialog(t, gh)
	git(t, work, "checkout", "-q", "-b", "other")
	p.click("create.go", "go", nil)
	if e := p.view().Find("error"); e == nil || e.Text != "The workspace is on other now, not fix-flaky" {
		t.Errorf("after the branch moved: %q", p.text())
	}
	if got := git(t, work, "log", "-1", "--format=%s"); got != "first" || git(t, work, "status", "--porcelain") == "" {
		t.Errorf("other's last commit is %q; want the changes left uncommitted", got)
	}
	if out, err := exec.Command("git", "-C", origin, "rev-parse", "--verify", "-q", "refs/heads/fix-flaky").Output(); err == nil {
		t.Errorf("pushed fix-flaky at %s", out)
	}
	if len(gh.ran("pr", "create")) != 0 {
		t.Error("opened a pull request")
	}
}

// Each update answers within the app's 30 s: every program Create, Write
// with AI and Ready for review run, and each ask, ends by 27 s after the
// click.
func TestEveryStepEndsWithinTheUpdatesBudget(t *testing.T) {
	gh := &github{prs: []map[string]any{openPR(12, true)}}
	work, origin := repo(t)
	onGitHub(t, work, origin, "fix-flaky")
	p := openPullRequests(t, work, gh)
	ready := time.Now()
	p.click("ready", "ready", nil)
	gh.mu.Lock()
	gh.prs = nil
	gh.mu.Unlock()
	p.click("refresh", "refresh", nil)
	p.click("create.open", "create", nil)
	p.answer = func(plugins.AskRequest) (plugins.AskAnswer, error) {
		return plugins.AskAnswer{Text: `{"title": "Retry it", "body": "Retries it."}`, Provider: "claude", Model: "claude-haiku-5-5"}, nil
	}
	write := time.Now()
	p.click("create.write", "write", nil)
	create := time.Now()
	p.click("create.go", "go", nil)
	if len(gh.ran("pr", "ready")) != 1 || len(gh.ran("pr", "create")) != 1 || len(p.asks) != 1 {
		t.Fatalf("ready %d, create %d, asks %d", len(gh.ran("pr", "ready")), len(gh.ran("pr", "create")), len(p.asks))
	}
	budget := 27*time.Second + 100*time.Millisecond
	for _, r := range p.runs {
		from := ready
		if r.At.After(create) {
			from = create
		} else if r.At.After(write) {
			from = write
		}
		if end := r.At.Add(time.Duration(r.Timeout * float64(time.Second))); end.Sub(from) > budget {
			t.Errorf("%q may run till %s after its click", r.Args, end.Sub(from).Round(time.Millisecond))
		}
	}
	if d := p.asks[0].Sub(write); d > budget {
		t.Errorf("the ask may run till %s after Write with AI", d.Round(time.Millisecond))
	}
}

// A gh pr create that runs out of time may still have opened the pull
// request: Create looks again, and shows the one it finds.
func TestCreatePRThatTimesOutShowsThePullRequestItOpened(t *testing.T) {
	gh := &github{createLag: true}
	p, _, _ := createDialog(t, gh)
	p.click("create.go", "go", nil)
	if text := p.text(); !strings.Contains(text, "#7|") || p.view().Find("create-dialog") != nil {
		t.Errorf("after a create that timed out: %q", text)
	}
}

// Create pushes to the one GitHub repository its dialog names, and to no
// other: a remote with more push URLs, or one now elsewhere, is refused
// before anything is committed, and a branch whose remote pushes to more
// than one place shows nothing.
func TestCreatePRPushesOnlyToTheRepositoryItNames(t *testing.T) {
	for _, c := range []struct {
		name string
		urls func(origin string) []string
		want string
	}{
		{"two push URLs", func(origin string) []string {
			return []string{origin, filepath.Join(filepath.Dir(origin), "elsewhere.git")}
		}, "Can't push: origin pushes to 2 places, and Create pushes to one"},
		{"another repository", func(string) []string { return []string{"https://github.com/acme/other.git"} }, "Can't push: origin pushes to acme/other now, not acme/widgets"},
	} {
		gh := &github{}
		p, work, origin := createDialog(t, gh)
		git(t, ".", "init", "-q", "--bare", filepath.Join(filepath.Dir(origin), "elsewhere.git"))
		for _, u := range c.urls(origin) {
			git(t, work, "remote", "set-url", "--add", "--push", "origin", u)
		}
		p.click("create.go", "go", nil)
		if e := p.view().Find("error"); e == nil || e.Text != c.want {
			t.Errorf("%s: %q", c.name, p.text())
		}
		if git(t, work, "log", "-1", "--format=%s") != "first" || len(gh.ran("pr", "create")) != 0 {
			t.Errorf("%s: committed or opened a pull request", c.name)
		}
		for _, r := range p.runs {
			if r.Args[1] == "push" {
				t.Errorf("%s: ran %q", c.name, r.Args)
			}
		}
	}

	work, origin := repo(t)
	onGitHub(t, work, origin, "fix-flaky")
	git(t, work, "remote", "set-url", "--add", "--push", "origin", "https://github.com/acme/widgets.git")
	git(t, work, "remote", "set-url", "--add", "--push", "origin", "https://github.com/acme/first.git")
	if p := openPullRequests(t, work, &github{}); p.view() != nil {
		t.Errorf("a branch that pushes to two places: %q", p.text())
	}
}

// A refresh that fails on another branch shows nothing of the last one's
// pull request, and Ready for review and Ask the agent to fix check the
// branch is still the pull request's before they act.
func TestAPullRequestIsOnlyShownAndActedOnForItsBranch(t *testing.T) {
	work, origin := repo(t)
	onGitHub(t, work, origin, "fix-flaky")
	gh := &github{prs: []map[string]any{openPR(12, true, lint)}}
	p := openPullRequests(t, work, gh)
	git(t, work, "checkout", "-q", "-b", "other")
	p.click("popover.toggle", "pr", nil)
	p.click("ready", "ready", nil)
	if len(gh.ran("pr", "ready")) != 0 {
		t.Errorf("marked #12 ready from other: %+v", gh.ran("pr", "ready"))
	}

	git(t, work, "checkout", "-q", "fix-flaky")
	p.click("refresh", "refresh", nil)
	git(t, work, "checkout", "-q", "other")
	p.click("fix", "fix", nil)
	if len(p.drafts) != 0 {
		t.Errorf("asked to fix #12 from other: %q", p.drafts)
	}

	git(t, work, "checkout", "-q", "fix-flaky")
	p.click("refresh", "refresh", nil)
	git(t, work, "checkout", "-q", "other")
	gh.mu.Lock()
	gh.listErr = "GraphQL: API rate limit exceeded for user ID 1."
	gh.mu.Unlock()
	p.click("refresh", "refresh", nil)
	if strings.Contains(p.text(), "#12") {
		t.Errorf("a failed refresh on other: %q", p.text())
	}
}

// A fork's pull request from a branch of the same name isn't the branch's.
func TestAForksPullRequestOfTheSameBranchIsNotShown(t *testing.T) {
	work, origin := repo(t)
	onGitHub(t, work, origin, "fix-flaky")
	fork := openPR(88, true)
	fork["headRepositoryOwner"] = map[string]any{"login": "another-fork"}
	p := openPullRequests(t, work, &github{prs: []map[string]any{openPR(11, false), fork}})
	if text := p.text(); !strings.Contains(text, "#11|") {
		t.Errorf("the header: %q, want #11, not the fork's #88", text)
	}
}

// Only SUCCESS passes a check: STALE, a conclusion GitHub adds later and
// none on a finished run wait; SKIPPED and NEUTRAL are skipped.
func TestOnlySuccessPassesACheck(t *testing.T) {
	run := func(name, conclusion string) map[string]any {
		return map[string]any{"__typename": "CheckRun", "name": name, "status": "COMPLETED", "conclusion": conclusion}
	}
	for _, c := range []struct {
		check map[string]any
		mark  string
		row   string
	}{
		{run("old", "STALE"), "checks running", "old · Waiting|"},
		{run("new", "SOMETHING_NEW"), "checks running", "new · Waiting|"},
		{run("blank", ""), "checks running", "blank · Waiting|"},
		{run("neutral", "NEUTRAL"), "checks passed", ""},
	} {
		work, origin := repo(t)
		onGitHub(t, work, origin, "fix-flaky")
		p := openPullRequests(t, work, &github{prs: []map[string]any{openPR(12, false, unit, c.check)}})
		if tip := p.view().Body.Tooltip; !strings.HasSuffix(tip, c.mark) {
			t.Errorf("%v: the pill says %q, want %s", c.check["conclusion"], tip, c.mark)
		}
		p.click("popover.toggle", "pr", nil)
		if !strings.Contains(p.text(), c.row) {
			t.Errorf("%v: the popover: %q", c.check["conclusion"], p.text())
		}
	}
}

// gh is signed out only as its active account is, or as GitHub says when
// asked for the repository.
func TestGhIsSignedOutOnlyAsItsActiveAccountIs(t *testing.T) {
	work, origin := repo(t)
	onGitHub(t, work, origin, "fix-flaky")
	gh := &github{prs: []map[string]any{openPR(12, false)}, inactive: "github.com\n  X Failed to log in to github.com account old (keyring)\n  - Active account: false\n  - The token in keyring is invalid. To re-authenticate, run: gh auth login -h github.com"}
	if p := openPullRequests(t, work, gh); !strings.Contains(p.text(), "#12|") {
		t.Errorf("with an inactive account signed out: %q", p.text())
	}

	work, origin = repo(t)
	onGitHub(t, work, origin, "fix-flaky")
	p := openPullRequests(t, work, &github{listErr: "HTTP 401: Bad credentials (https://api.github.com/graphql)\nTry authenticating with:  gh auth login"})
	p.click("popover.toggle", "pr", nil)
	if !strings.Contains(p.text(), "GitHub CLI|") || !strings.Contains(p.text(), "Sign gh in to GitHub") {
		t.Errorf("with GitHub refusing the token: %q", p.text())
	}
}

// The header follows what its copy shows: a thread whose workspace changes
// is read there, and one no longer shown isn't read at all.
func TestTheHeaderFollowsItsCopy(t *testing.T) {
	work, origin := repo(t)
	onGitHub(t, work, origin, "fix-flaky")
	other, otherOrigin := repo(t)
	onGitHub(t, other, otherOrigin, "fix-flaky")
	gh := &github{prs: []map[string]any{openPR(12, false)}}
	p := openPullRequests(t, work, gh)
	p.ctx = map[string]any{"id": "t1", "title": "Fix the flaky test", "workspace": other, "local": true}
	p.s.SetItems("pr", []plugins.PlaceItem{{Key: "t1", Version: "2", Context: p.ctx}})
	p.settle()
	if p.runs[len(p.runs)-1].Cwd != other || !strings.Contains(p.text(), "#12|") {
		t.Errorf("after the workspace changed, read %s: %q", p.runs[len(p.runs)-1].Cwd, p.text())
	}
	p.s.SetItems("pr", nil)
	p.drain()
	n := len(p.runs)
	p.s.Notify(plugins.AppEvent{Kind: "app", Type: "window.focused", Data: map[string]bool{"focused": true}})
	p.drain()
	if len(p.runs) != n {
		t.Errorf("read %+v for a header no longer shown", p.runs[n:])
	}
}

// Write with AI keeps your title and description over a model's too long
// for GitHub, and says so.
func TestWriteWithAIKeepsTheDraftOverAnAnswerTooLong(t *testing.T) {
	for _, c := range []struct {
		title, body, want string
	}{
		{strings.Repeat("é", 129), "Fine.", "The model's title is over 256 bytes, so yours are kept."},
		{"Fine", strings.Repeat("x", 30001), "The model's description is over 30000 bytes, so yours are kept."},
	} {
		p, _, _ := createDialog(t, &github{})
		p.answer = func(plugins.AskRequest) (plugins.AskAnswer, error) {
			b, _ := json.Marshal(map[string]string{"title": c.title, "body": c.body})
			return plugins.AskAnswer{Text: string(b), Provider: "claude", Model: "claude-haiku-5-5"}, nil
		}
		p.click("create.write", "write", map[string]any{"title": "Mine", "body": "My words"})
		v := p.view()
		if e := v.Find("error"); e == nil || !strings.HasPrefix(e.Text, c.want) || val(v, "title") != "Mine" || val(v, "body") != "My words" {
			t.Errorf("%d-byte title, %d-byte body: %q", len(c.title), len(c.body), p.text())
		}
	}
}

// A branch ahead of a base that isn't fetched here still offers Create PR,
// whose dialog says the commits it adds aren't known.
func TestABaseNotFetchedStillOffersCreatePR(t *testing.T) {
	work, origin := repo(t)
	onGitHub(t, work, origin, "fix-flaky")
	git(t, work, "commit", "-qam", "Two")
	git(t, work, "add", "new.txt")
	git(t, work, "commit", "-qm", "New")
	git(t, work, "update-ref", "-d", "refs/remotes/origin/main")
	git(t, work, "branch", "-q", "-D", "main")
	p := openPullRequests(t, work, &github{})
	if !strings.Contains(p.text(), "Create PR|") {
		t.Fatalf("the header: %q", p.text())
	}
	p.click("create.open", "create", nil)
	if !strings.Contains(p.text(), "origin/main isn't fetched here, so the commits it adds aren't known.") {
		t.Errorf("the dialog: %q", p.text())
	}
}

// The repository's default branch shows nothing, gh or no gh, and asks gh
// nothing.
func TestTheDefaultBranchShowsNothingWithoutAskingGh(t *testing.T) {
	work, origin := repo(t)
	onGitHub(t, work, origin, "main")
	git(t, work, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	git(t, work, "checkout", "-q", "--", ".")
	os.Remove(filepath.Join(work, "new.txt"))
	gh := &github{missing: true}
	if p := openPullRequests(t, work, gh); p.view() != nil || len(gh.calls) != 0 {
		t.Errorf("a clean default branch: %q, gh ran %+v", p.text(), gh.calls)
	}
}

// gitIn runs git in dir for a test's hook, which can't fail the test.
func gitIn(dir string, args ...string) string {
	out, _ := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	return strings.TrimSpace(string(out))
}

// Create pushes the one commit it made, and to where its dialog said or
// nowhere: an upstream that moves after the commit, or a branch that moves
// past it, stops the push. Submodules are never pushed with it.
func TestCreatePRPushesTheCommitItMadeWhereTheDialogSaid(t *testing.T) {
	gh := &github{}
	p, work, origin := createDialog(t, gh)
	p.mu.Lock()
	p.before = func(args []string) any {
		if args[1] == "commit" {
			gitIn(work, "config", "branch.fix-flaky.remote", "origin")
			gitIn(work, "config", "branch.fix-flaky.merge", "refs/heads/release")
		}
		return nil
	}
	p.mu.Unlock()
	p.click("create.go", "go", nil)
	sha := gitIn(work, "rev-parse", "--short=7", "HEAD")
	if e := p.view().Find("error"); e == nil || e.Text != "Committed "+sha+"; can't push: fix-flaky pushes to origin/release now, not origin/fix-flaky" {
		t.Errorf("an upstream that moved after the commit: %q", p.text())
	}
	if refs := gitIn(origin, "for-each-ref", "refs/heads/fix-flaky", "refs/heads/release"); refs != "" {
		t.Errorf("pushed %s", refs)
	}

	p, work, origin = createDialog(t, gh)
	p.mu.Lock()
	p.before = func(args []string) any {
		if slices.Equal(args, []string{"git", "rev-parse", "--verify", "-q", "refs/heads/fix-flaky"}) {
			gitIn(work, "commit", "-q", "--allow-empty", "-m", "Someone else's")
		}
		return nil
	}
	p.mu.Unlock()
	p.click("create.go", "go", nil)
	if e := p.view().Find("error"); e == nil || !strings.Contains(e.Text, "; fix-flaky moved to "+gitIn(work, "rev-parse", "--short=7", "HEAD")+" meanwhile, so nothing was pushed") {
		t.Errorf("a branch that moved past the commit: %q", p.text())
	}
	if refs := gitIn(origin, "for-each-ref", "refs/heads/fix-flaky"); refs != "" {
		t.Errorf("pushed %s", refs)
	}

	p, work, origin = createDialog(t, gh)
	p.click("create.go", "go", nil)
	var push []string
	for _, r := range p.runs {
		if r.Args[1] == "push" {
			push = r.Args
		}
	}
	head := gitIn(work, "rev-parse", "HEAD")
	if !slices.Contains(push, "--no-recurse-submodules") || push[len(push)-1] != head+":refs/heads/fix-flaky" || gitIn(origin, "rev-parse", "fix-flaky") != head {
		t.Errorf("pushed with %q; want the commit %s, and no submodules", push, head)
	}
}

// A gh pr create whose answer is lost may still have opened the pull
// request: Create looks for one from its head into its base, and shows the
// one it finds at once, closing the dialog only then.
func TestACreateGitHubMayHaveOpenedShowsThePullRequestItFinds(t *testing.T) {
	gh := &github{createEOF: true}
	p, _, _ := createDialog(t, gh)
	lists := len(gh.ran("pr", "list"))
	p.click("create.go", "go", nil)
	if text := p.text(); !strings.Contains(text, "#7|") || p.view().Find("create-dialog") != nil {
		t.Errorf("after an answer lost: %q", text)
	}
	if n := len(gh.ran("pr", "list")) - lists; n != 1 {
		t.Errorf("listed pull requests %d times after Create, want once, to find it", n)
	}

	gh = &github{createEOF: true, createTo: "release"}
	p, _, _ = createDialog(t, gh)
	p.click("create.go", "go", nil)
	if e := p.view().Find("error"); e == nil || !strings.Contains(e.Text, "GitHub didn't say whether it opened the pull request") || strings.Contains(p.text(), "#7|") {
		t.Errorf("with only one into another base found: %q", p.text())
	}
}

// Create answers before the app shows its toast: a slow toast doesn't
// lose the pull request it opened.
func TestCreatePRAnswersBeforeItsToast(t *testing.T) {
	was := callTimeout
	callTimeout = 2 * time.Second
	t.Cleanup(func() { callTimeout = was })
	p, _, _ := createDialog(t, &github{})
	p.toastLag = 3 * time.Second
	p.click("create.go", "go", nil)
	if text := p.text(); !strings.Contains(text, "#7|") || p.s.Err != nil {
		t.Errorf("with a slow toast: %q, failed %v", text, p.s.Err)
	}
}

// A pull request is its head's: the branch, the ref it pushes to and the
// repository. One whose branch now pushes elsewhere isn't marked ready,
// nor kept when a refresh fails.
func TestAPullRequestIsActedOnOnlyForItsHead(t *testing.T) {
	work, origin := repo(t)
	onGitHub(t, work, origin, "fix-flaky")
	gh := &github{prs: []map[string]any{openPR(12, true)}}
	p := openPullRequests(t, work, gh)
	git(t, work, "config", "branch.fix-flaky.remote", "origin")
	git(t, work, "config", "branch.fix-flaky.merge", "refs/heads/release")
	p.click("popover.toggle", "pr", nil)
	p.click("ready", "ready", nil)
	if len(gh.ran("pr", "ready")) != 0 {
		t.Errorf("marked #12 ready for a branch that pushes to release: %+v", gh.ran("pr", "ready"))
	}

	git(t, work, "config", "--unset", "branch.fix-flaky.merge")
	git(t, work, "config", "--unset", "branch.fix-flaky.remote")
	p.click("refresh", "refresh", nil)
	git(t, work, "config", "branch.fix-flaky.remote", "origin")
	git(t, work, "config", "branch.fix-flaky.merge", "refs/heads/release")
	gh.mu.Lock()
	gh.listErr = "GraphQL: API rate limit exceeded for user ID 1."
	gh.mu.Unlock()
	p.click("refresh", "refresh", nil)
	if strings.Contains(p.text(), "#12") {
		t.Errorf("a failed refresh after the upstream moved: %q", p.text())
	}
}

// A branch the push leaves untracked, as a fetch config that maps only
// main, says so.
func TestCreatePRSaysWhenTheBranchIsntTracking(t *testing.T) {
	p, work, _ := createDialog(t, &github{})
	git(t, work, "config", "remote.origin.fetch", "+refs/heads/main:refs/remotes/origin/main")
	p.click("create.go", "go", nil)
	if len(p.toasts) != 1 || !strings.Contains(p.toasts[0], "Fix-flaky doesn't track origin/fix-flaky yet") || !strings.Contains(p.text(), "#7|") {
		t.Errorf("told %q, showing %q", p.toasts, p.text())
	}
}

// The header reads enough to be right: the remote's default branch hides
// a branch only when the remote fetches from where it pushes, the owned
// pull request is found past a page of forks', and a git status or a git
// that fails is unknown, not clean or hidden.
func TestTheHeaderReadsEnoughToBeRight(t *testing.T) {
	work, origin := repo(t)
	onGitHub(t, work, origin, "main")
	git(t, work, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	git(t, work, "remote", "set-url", "--push", "origin", "https://github.com/acme/fork.git")
	if p := openPullRequests(t, work, &github{base: "develop"}); !strings.Contains(p.text(), "Create PR|") {
		t.Errorf("main, pushed to a fork whose default is develop: %q", p.text())
	}

	work, origin = repo(t)
	onGitHub(t, work, origin, "fix-flaky")
	var prs []map[string]any
	for i := range 10 {
		fork := openPR(100+i, false)
		fork["headRepositoryOwner"] = map[string]any{"login": fmt.Sprintf("fork%d", i)}
		prs = append(prs, fork)
	}
	if p := openPullRequests(t, work, &github{prs: append(prs, openPR(11, false))}); !strings.Contains(p.text(), "#11|") {
		t.Errorf("past ten forks' pull requests: %q", p.text())
	}

	work, origin = repo(t)
	onGitHub(t, work, origin, "fix-flaky")
	git(t, work, "checkout", "-q", "--", ".")
	os.Remove(filepath.Join(work, "new.txt"))
	p := openPullRequests(t, work, &github{})
	p.mu.Lock()
	p.before = func(args []string) any {
		if args[1] == "status" {
			return map[string]any{"status": 128, "stdout": "", "stderr": "fatal: index file corrupt"}
		}
		return nil
	}
	p.mu.Unlock()
	p.click("refresh", "refresh", nil)
	p.click("create.open", "create", nil)
	if !strings.Contains(p.text(), "git couldn't say which files changed, so nothing is committed.") {
		t.Errorf("a git status that fails: %q", p.text())
	}

	work, origin = repo(t)
	onGitHub(t, work, origin, "fix-flaky")
	p = openPullRequests(t, work, &github{prs: []map[string]any{openPR(12, false)}})
	p.mu.Lock()
	p.before = func(args []string) any {
		if args[1] == "symbolic-ref" {
			return map[string]any{"status": -1, "stdout": "", "stderr": ""}
		}
		return nil
	}
	p.mu.Unlock()
	p.click("refresh", "refresh", nil)
	if !strings.Contains(p.text(), "#12|") {
		t.Errorf("after a git that timed out: %q", p.text())
	}
	p.mu.Lock()
	p.before = nil
	n := len(p.runs)
	p.mu.Unlock()
	p.s.Notify(plugins.AppEvent{Kind: "timer", ID: "refresh"})
	p.settle()
	if len(p.runs) == n {
		t.Error("the next timer didn't read again after a git that timed out")
	}
}

// The pill is the pull request's icon, tinted by its checks while it's
// open and by its state after, and its number; its popover is padded, and
// opens only on a click, never as the header comes back.
func TestThePullRequestPillIsCompactAndItsPopoverWaitsForAClick(t *testing.T) {
	work, origin := repo(t)
	onGitHub(t, work, origin, "fix-flaky")
	merged := openPR(12, false, lint)
	merged["state"] = "MERGED"
	gh := &github{prs: []map[string]any{merged}}
	p := openPullRequests(t, work, gh)
	v := p.view()
	if pill := v.Body.Children; len(pill) != 2 || pill[0].Color != "$accent" {
		t.Errorf("a merged pull request's pill: %+v, want its icon in merged's color", pill)
	}
	if len(v.Body.Popover.Padding) == 0 {
		t.Error("the popover's column isn't padded")
	}
	p.click("popover.toggle", "pr", nil)
	gh.mu.Lock()
	gh.prs = nil
	gh.mu.Unlock()
	p.click("refresh", "refresh", nil)
	gh.mu.Lock()
	gh.prs = []map[string]any{openPR(12, false)}
	gh.mu.Unlock()
	p.s.Notify(plugins.AppEvent{Kind: "app", Type: "window.focused", Data: map[string]bool{"focused": true}})
	p.settle()
	if v := p.view(); v == nil || v.Body.Open {
		t.Errorf("the pull request came back with its popover open: %q", p.text())
	}
}

// Write with AI asks the app nothing, names a model the picker doesn't
// list by its provider and id, and an error clears who wrote the last.
func TestWriteWithAINamesTheModelAndAsksTheAppNothing(t *testing.T) {
	p, _, _ := createDialog(t, &github{})
	p.mu.Lock()
	p.effects = nil
	p.answer = func(plugins.AskRequest) (plugins.AskAnswer, error) {
		return plugins.AskAnswer{Text: `{"title": "Retry it", "body": "Retries it."}`, Provider: "claude", Model: "claude-haiku-4-5-20251001"}, nil
	}
	p.mu.Unlock()
	p.click("create.write", "write", nil)
	if by := p.view().Find("by"); by == nil || by.Text != "Written by Claude (claude-haiku-4-5-20251001)" {
		t.Errorf("says %q", p.text())
	}
	if slices.Contains(p.effects, "models.list") {
		t.Errorf("Write with AI asked the app %q", p.effects)
	}
	p.mu.Lock()
	p.answer = func(plugins.AskRequest) (plugins.AskAnswer, error) {
		return plugins.AskAnswer{}, fmt.Errorf("Not logged in")
	}
	p.mu.Unlock()
	p.click("create.write", "write", nil)
	if v := p.view(); v.Find("by") != nil || v.Find("error") == nil {
		t.Errorf("after an error: %q", p.text())
	}
}

// The dialog draws nothing where it has nothing to show: no gap is left
// where the commit toggle, who wrote it or an error would be.
func TestTheDialogLeavesNoGap(t *testing.T) {
	p, _, origin := createDialog(t, &github{})
	os.RemoveAll(origin)
	p.click("create.go", "go", nil)
	v := p.view()
	if v.Find("commit") != nil || v.Find("error") == nil {
		t.Fatalf("after the commit: %q", p.text())
	}
	v.Find("create-dialog").Walk(func(n *widget.Node) {
		if n.Type == "spacer" {
			t.Errorf("a spacer %q holds a gap in the dialog", n.ID)
		}
	})
}

// Create pushes by the remote's name, so git resolves the push URL exactly
// as the dialog's git remote get-url --push did, a remote's own exceptions
// to pushInsteadOf included; a literal URL would lose them. Git then shows
// the remote's ref at the commit, the branch tracking it.
func TestCreatePRPushesByTheRemoteItShowed(t *testing.T) {
	p, work, origin := createDialog(t, &github{})
	p.click("create.go", "go", nil)
	var push []string
	for _, r := range p.runs {
		if r.Args[1] == "push" {
			push = r.Args
		}
	}
	head := gitIn(work, "rev-parse", "HEAD")
	if len(push) < 2 || push[len(push)-2] != "origin" || gitIn(origin, "rev-parse", "fix-flaky") != head {
		t.Errorf("pushed with %q; want %s pushed by the remote the dialog showed", push, head)
	}
	if gitIn(work, "rev-parse", "refs/remotes/origin/fix-flaky") != head || gitIn(work, "rev-parse", "--abbrev-ref", "fix-flaky@{upstream}") != "origin/fix-flaky" || !strings.Contains(p.text(), "#7|") {
		t.Errorf("after the push: %q", p.text())
	}
}

// Create PR opens its dialog without asking the app anything: the models'
// names, for Write with AI to say which wrote it, were read with the
// header, so a slow app doesn't hold the dialog up.
func TestCreatePROpensWithoutAskingTheApp(t *testing.T) {
	was := callTimeout
	callTimeout = 2 * time.Second
	t.Cleanup(func() { callTimeout = was })
	work, origin := repo(t)
	onGitHub(t, work, origin, "fix-flaky")
	p := openPullRequests(t, work, &github{})
	p.mu.Lock()
	p.effects = nil
	p.modelsLag = 3 * time.Second
	p.mu.Unlock()
	p.click("create.open", "create", nil)
	if v := p.view(); v == nil || v.Find("go") == nil || p.s.Err != nil || slices.Contains(p.effects, "models.list") {
		t.Errorf("Create PR with a slow app: %q, failed %v, asked %q", p.text(), p.s.Err, p.effects)
	}
}

// A commit or a push is told as done once git says so: a git after it that
// runs out of time doesn't hide it, nor offer the commit again.
func TestCreatePRTellsOfWhatItDidBeforeAGitRanOutOfTime(t *testing.T) {
	late := map[string]any{"status": -1, "stdout": "", "stderr": ""}
	p, work, origin := createDialog(t, &github{})
	p.mu.Lock()
	p.before = func(args []string) any {
		if slices.Equal(args, []string{"git", "rev-parse", "HEAD"}) {
			return late
		}
		return nil
	}
	p.mu.Unlock()
	p.click("create.go", "go", nil)
	v := p.view()
	if e := v.Find("error"); e == nil || !strings.HasPrefix(e.Text, "Committed; ") || v.Find("commit") != nil || gitIn(work, "log", "-1", "--format=%s") != "Fix the flaky test" {
		t.Errorf("after a rev-parse that ran out of time: %q", p.text())
	}
	if refs := gitIn(origin, "for-each-ref", "refs/heads/fix-flaky"); refs != "" {
		t.Errorf("pushed %s", refs)
	}

	p, work, origin = createDialog(t, &github{})
	p.mu.Lock()
	p.before = func(args []string) any {
		if args[1] == "branch" {
			return late
		}
		return nil
	}
	p.mu.Unlock()
	p.click("create.go", "go", nil)
	if len(p.toasts) != 1 || !strings.Contains(p.toasts[0], "Fix-flaky doesn't track origin/fix-flaky yet: git branch didn't finish in time") || !strings.Contains(p.text(), "#7|") || gitIn(origin, "rev-parse", "fix-flaky") != gitIn(work, "rev-parse", "HEAD") {
		t.Errorf("after a git branch that ran out of time: told %q, showing %q", p.toasts, p.text())
	}
}

// A pull request Create opened, or a list showed, stays till a read of it
// by its number says otherwise: a list that leaves it out doesn't bring
// Create PR back.
func TestAPullRequestStaysTillItsReadSaysOtherwise(t *testing.T) {
	gh := &github{}
	p, _, _ := createDialog(t, gh)
	p.click("create.go", "go", nil)
	p.mu.Lock()
	p.before = func(args []string) any {
		if slices.Equal(args[:3], []string{"gh", "pr", "list"}) {
			return map[string]any{"status": 0, "stdout": "[]", "stderr": ""}
		}
		return nil
	}
	p.mu.Unlock()
	p.s.Notify(plugins.AppEvent{Kind: "app", Type: "window.focused", Data: map[string]bool{"focused": true}})
	p.settle()
	if text := p.text(); !strings.Contains(text, "#7|") || len(gh.ran("pr", "view", "7")) != 1 {
		t.Errorf("after a list that left #7 out: %q", text)
	}
	gh.mu.Lock()
	gh.prs = nil
	gh.mu.Unlock()
	p.click("refresh", "refresh", nil)
	if text := p.text(); !strings.Contains(text, "Create PR|") {
		t.Errorf("after GitHub said #7 isn't there: %q", text)
	}
}

// The branch's open pull request is found before its old ones: the open
// ones are listed first, and the rest only when none is open.
func TestAnOpenPullRequestIsFoundBeforeOldOnes(t *testing.T) {
	work, origin := repo(t)
	onGitHub(t, work, origin, "fix-flaky")
	closed := openPR(119, false)
	closed["state"] = "CLOSED"
	prs := []map[string]any{closed}
	for i := range 9 {
		fork := openPR(110+i, false)
		fork["headRepositoryOwner"] = map[string]any{"login": fmt.Sprintf("fork%d", i)}
		prs = append(prs, fork)
	}
	if p := openPullRequests(t, work, &github{prs: append(prs, openPR(11, false))}); !strings.Contains(p.text(), "#11|") {
		t.Errorf("past an old one of the branch's and nine forks': %q", p.text())
	}

	work, origin = repo(t)
	onGitHub(t, work, origin, "fix-flaky")
	gh := &github{prs: []map[string]any{closed}}
	if p := openPullRequests(t, work, gh); !strings.Contains(p.text(), "#119|") || len(gh.ran("pr", "list")) != 2 {
		t.Errorf("with only an old one: %q after %+v", p.text(), gh.ran("pr", "list"))
	}
}

// A narrow header asks for the copy's smallest drawing: the pill keeps to
// its icon, named by its label, and its popover; Create PR to its icon.
func TestACompactHeaderKeepsThePillToItsIcon(t *testing.T) {
	compact := func(p *pullRequests) {
		p.s.SetItems("pr", []plugins.PlaceItem{{Key: "t1", Version: "1 compact", Context: p.ctx, Compact: true}})
		p.settle()
	}
	work, origin := repo(t)
	onGitHub(t, work, origin, "fix-flaky")
	p := openPullRequests(t, work, &github{prs: []map[string]any{openPR(12, false, unit)}})
	compact(p)
	v := p.view()
	if v == nil || v.Body.Label != "Pull request #12" || v.Body.Popover == nil || len(v.Body.Children) != 1 || v.Body.Children[0].Name != "pull-request" {
		t.Errorf("the compact pill: %q", p.text())
	}

	work, origin = repo(t)
	onGitHub(t, work, origin, "fix-flaky")
	git(t, work, "commit", "-qam", "Two")
	p = openPullRequests(t, work, &github{})
	compact(p)
	if b := p.view(); b == nil || b.Body.Label != "Create PR" || !b.Body.Compact {
		t.Errorf("the compact Create PR: %q", p.text())
	}
}
