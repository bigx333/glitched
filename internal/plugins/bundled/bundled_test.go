package bundled_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"glitched/internal/plugins"
	"glitched/internal/plugins/bundled"
	"glitched/internal/plugins/widget"
)

// app is the built-in plugins on a state directory, with the UI thread a
// test's: what the host posts runs as the test waits.
type app struct {
	t      *testing.T
	h      *plugins.Host
	q      chan func()
	toasts []string
}

func open(t *testing.T) *app {
	t.Helper()
	a := &app{t: t, q: make(chan func(), 1024)}
	a.h = plugins.Open(t.TempDir(), plugins.Options{
		Post:        func(fn func()) { a.q <- fn },
		Logf:        t.Logf,
		Bundled:     bundled.FS,
		ReleaseKeys: []string{bundled.ReleaseKey},
		CallTimeout: 30 * time.Second,
		Do: func(_ string, e plugins.Effect) (any, error) {
			if e.Type == "toast" {
				var m struct{ Title, Description string }
				json.Unmarshal(e.Args, &m)
				a.toasts = append(a.toasts, m.Title+": "+m.Description)
			}
			return nil, nil
		},
	})
	t.Cleanup(a.h.Close)
	return a
}

func (a *app) until(what string, ok func() bool) {
	a.t.Helper()
	deadline := time.After(30 * time.Second)
	for !ok() {
		select {
		case fn := <-a.q:
			fn()
		case <-deadline:
			a.t.Fatalf("timed out waiting for %s; told %q, failed %v", what, a.toasts, a.h.Failed("header-actions"))
		}
	}
}

// Every built-in plugin is signed by the release key compiled in: one
// changed without signing it again doesn't run.
func TestTheBuiltInPluginsAreSignedByTheReleaseKey(t *testing.T) {
	a := open(t)
	for _, p := range a.h.Installed() {
		if p.Trust != plugins.TrustBuiltin || p.Err != nil {
			t.Errorf("%s: %s, %v; after changing a built-in plugin, sign it: GLITCHED_RELEASE_KEY=<key file> go run ./cmd/sign-bundled", p.ID, p.Trust, p.Err)
		}
	}
	if got := a.h.Replacers("thread.header.actions"); len(got) != 1 || got[0].Plugin != "header-actions" {
		t.Fatalf("the header's actions are drawn by %v", got)
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// repo is a checkout with a change and a new file, cloned from a bare
// origin, and the origin.
func repo(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[user]\n\tname = T\n\temail = t@example.invalid\n[init]\n\tdefaultBranch = main\n"), 0o644)
	origin := filepath.Join(t.TempDir(), "origin.git")
	git(t, ".", "init", "-q", "--bare", origin)
	work := filepath.Join(t.TempDir(), "work")
	git(t, ".", "clone", "-q", origin, work)
	os.WriteFile(filepath.Join(work, "a.txt"), []byte("one\n"), 0o644)
	git(t, work, "add", "a.txt")
	git(t, work, "commit", "-qm", "first")
	git(t, work, "push", "-q", "-u", "origin", "main")
	os.WriteFile(filepath.Join(work, "a.txt"), []byte("two\n"), 0o644)
	os.WriteFile(filepath.Join(work, "new.txt"), []byte("new\n"), 0o644)
	return work, origin
}

// header draws the built-in header actions for a thread in work, and
// clicks through Commit & push with the message.
func header(t *testing.T, work, message string) (*app, *plugins.Session) {
	a := open(t)
	s := a.h.App("header-actions")
	ctx := map[string]any{
		"thread": map[string]any{"id": "t1", "title": "Fix the flaky test", "workspace": work},
		"local":  true, "narrow": false, "changedFiles": 2,
		"panels": map[string]bool{"terminal": false, "diff": true, "preview": false},
	}
	s.SetItems("actions", []plugins.PlaceItem{{Key: "t1", Version: "1", Context: ctx}})
	view := func() *widget.View {
		if n := s.PlaceView("actions", "t1"); n != nil {
			return &widget.View{Body: n}
		}
		return nil
	}
	a.until("the row", func() bool { v := view(); return v != nil && v.Find("commit") != nil })
	if d := view().Find("diff"); !d.Active || d.Badge != "2" {
		t.Fatalf("the diff toggle: active %v, badge %q", d.Active, d.Badge)
	}
	ref := &plugins.PlaceRef{ID: "actions", At: "thread.header.actions", Key: "t1", Context: ctx}
	s.Dispatch(plugins.UIEvent{Type: "click", Handler: "commit.open", Node: "commit", Place: ref}, nil)
	a.until("the commit dialog", func() bool { v := view(); return v != nil && v.Find("message") != nil })
	text := ""
	view().Body.Walk(func(n *widget.Node) { text += n.Text + "|" })
	for _, want := range []string{"M a.txt", "?? new.txt", "on main", "pushes to origin/main"} {
		if !strings.Contains(text, want) {
			t.Errorf("the dialog doesn't say %q: %s", want, text)
		}
	}
	s.Dispatch(plugins.UIEvent{Type: "click", Handler: "commit.go", Node: "go", Place: ref}, map[string]any{"message": message})
	a.until("the dialog to close", func() bool { v := view(); return v != nil && v.Find("message") == nil })
	return a, s
}

func TestCommitAndPushCommitsEveryChangeAndPushesIt(t *testing.T) {
	work, origin := repo(t)
	a, _ := header(t, work, "Fix the flaky test")
	if git(t, work, "status", "--porcelain") != "" {
		t.Error("changes left uncommitted")
	}
	if got := git(t, origin, "log", "-1", "--format=%s", "main"); got != "Fix the flaky test" {
		t.Errorf("origin's main is at %q", got)
	}
	if len(a.toasts) != 1 || !strings.HasPrefix(a.toasts[0], "Pushed ") || !strings.Contains(a.toasts[0], "2 files to origin/main") {
		t.Errorf("told %q", a.toasts)
	}
}

func TestAPushThatFailsKeepsTheCommitAndSaysSo(t *testing.T) {
	work, origin := repo(t)
	os.RemoveAll(origin)
	a, _ := header(t, work, "Keep it local")
	if got := git(t, work, "log", "-1", "--format=%s"); got != "Keep it local" {
		t.Errorf("the commit is %q", got)
	}
	if len(a.toasts) != 1 || !strings.Contains(a.toasts[0], ", not pushed: Push failed") || !strings.Contains(a.toasts[0], "The commit stays on main") {
		t.Errorf("told %q", a.toasts)
	}
}
