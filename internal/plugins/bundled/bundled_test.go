package bundled_test

import (
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	plugin "github.com/yxxbc/mygo-plugin"

	"glitched/internal/plugins"
	"glitched/internal/plugins/bundled"
	"glitched/internal/plugins/pack"
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
	t.Setenv("LC_ALL", "C") // git's messages, as the tests read them
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
	for _, want := range []string{"M a.txt", "?? new.txt", "on main", "pushes to origin/main."} {
		if !strings.Contains(text, want) {
			t.Errorf("the dialog doesn't say %q: %s", want, text)
		}
	}
	s.Dispatch(plugins.UIEvent{Type: "click", Handler: "commit.go", Node: "go", Place: ref}, map[string]any{"message": message})
	a.until("the dialog to close, or say why not", func() bool { v := view(); return v != nil && (v.Find("message") == nil || v.Find("error") != nil) })
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

// The push updates only the ref the dialog named, without force, whatever
// the repository's push config says.
func TestThePushIgnoresTheRepositorysPushConfig(t *testing.T) {
	work, origin := repo(t)
	git(t, work, "config", "remote.origin.push", "+refs/heads/main:refs/heads/elsewhere")
	git(t, work, "config", "push.default", "matching")
	git(t, work, "config", "push.followTags", "true")
	git(t, work, "tag", "-a", "-m", "Not this", "v1")
	header(t, work, "Only main")
	if got := git(t, origin, "log", "-1", "--format=%s", "main"); got != "Only main" {
		t.Errorf("origin's main is at %q", got)
	}
	if out, _ := exec.Command("git", "-C", origin, "rev-parse", "--verify", "-q", "refs/heads/elsewhere").Output(); len(out) > 0 {
		t.Error("the push followed the repository's refspec to elsewhere")
	}
	if out, _ := exec.Command("git", "-C", origin, "rev-parse", "--verify", "-q", "refs/tags/v1").Output(); len(out) > 0 {
		t.Error("the push followed push.followTags and pushed a tag")
	}
}

// A call the app refuses is told, and the row stays.
func TestARefusedCallLeavesTheRow(t *testing.T) {
	work, _ := repo(t)
	a := open(t)
	if err := a.h.SetGranted("header-actions", "programs", false); err != nil {
		t.Fatal(err)
	}
	s := a.h.App("header-actions")
	ctx := map[string]any{"thread": map[string]any{"id": "t1", "workspace": work}, "local": true, "panels": map[string]bool{}}
	s.SetItems("actions", []plugins.PlaceItem{{Key: "t1", Version: "1", Context: ctx}})
	a.until("the row", func() bool { return s.PlaceView("actions", "t1") != nil })
	s.Dispatch(plugins.UIEvent{Type: "click", Handler: "commit.open", Node: "commit", Place: &plugins.PlaceRef{ID: "actions", At: "thread.header.actions", Key: "t1", Context: ctx}}, nil)
	a.until("the refusal", func() bool { return len(a.toasts) > 0 })
	if !strings.Contains(a.toasts[0], "programs") || a.h.Failed("header-actions") != nil {
		t.Fatalf("told %q, failed %v", a.toasts, a.h.Failed("header-actions"))
	}
}

func TestAPushThatFailsKeepsTheCommitAndSaysSo(t *testing.T) {
	work, origin := repo(t)
	os.RemoveAll(origin)
	a, _ := header(t, work, "Keep it local")
	if got := git(t, work, "log", "-1", "--format=%s"); got != "Keep it local" {
		t.Errorf("the commit is %q", got)
	}
	if len(a.toasts) != 1 || !strings.Contains(a.toasts[0], ", not pushed: Push to origin/main failed: '"+origin+"' does not appear to be a git repository. The commit stays on main") {
		t.Errorf("told %q", a.toasts)
	}
}

// A push the remote turns down says why: that it has commits the branch
// lacks, or what refused it.
func TestARejectedPushSaysWhy(t *testing.T) {
	work, origin := repo(t)
	other := filepath.Join(t.TempDir(), "other")
	git(t, ".", "clone", "-q", origin, other)
	git(t, other, "commit", "-q", "--allow-empty", "-m", "Theirs")
	git(t, other, "push", "-q", "origin", "main")
	a, _ := header(t, work, "Mine")
	if len(a.toasts) != 1 || !strings.Contains(a.toasts[0], "Push to origin/main failed: the remote has commits this branch doesn't; pull them, then push again. The commit stays on main") {
		t.Errorf("diverged: told %q", a.toasts)
	}

	work, origin = repo(t)
	hook := filepath.Join(origin, "hooks", "pre-receive")
	os.WriteFile(hook, []byte("#!/bin/sh\necho 'main is frozen for the release'\nexit 1\n"), 0o755)
	a, _ = header(t, work, "Mine")
	if len(a.toasts) != 1 || !strings.Contains(a.toasts[0], "failed: the remote refused it (pre-receive hook declined): main is frozen for the release. The commit") {
		t.Errorf("hook: told %q", a.toasts)
	}
}

// A commit git refuses says why, as git's first line does.
func TestARefusedCommitSaysWhy(t *testing.T) {
	work, _ := repo(t)
	git(t, work, "config", "user.useConfigOnly", "true")
	git(t, work, "config", "user.name", "")
	git(t, work, "config", "user.email", "")
	_, s := header(t, work, "Nobody's")
	if e := (&widget.View{Body: s.PlaceView("actions", "t1")}).Find("error"); e == nil || e.Text != "git commit: Author identity unknown" {
		t.Errorf("the dialog says %+v", e)
	}
}

func TestEveryBuiltInPluginUsesTypeScriptAndCompiles(t *testing.T) {
	key, err := pack.LoadKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	entries, err := fs.ReadDir(bundled.FS, ".")
	if err != nil {
		t.Fatal(err)
	}
	onDisk, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	dirs := func(entries []fs.DirEntry) []string {
		var names []string
		for _, e := range entries {
			if e.IsDir() {
				names = append(names, e.Name())
			}
		}
		return names
	}
	if embedded, disk := dirs(entries), dirs(onDisk); !slices.Equal(embedded, disk) {
		t.Fatalf("bundled.FS embeds %v, but the folder holds %v; embed every plugin in bundled.go", embedded, disk)
	}
	var services []struct {
		id      string
		dir     string
		service string
	}
	var ids []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		id := e.Name()
		ids = append(ids, id)
		sub, err := fs.Sub(bundled.FS, id)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := fs.ReadFile(sub, pack.ManifestFile)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		var m struct {
			Main     string `json:"main"`
			Glitched struct {
				Service string `json:"service"`
			} `json:"glitched"`
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		for _, v := range plugins.CheckTypeScriptPlugin(id) {
			t.Errorf("%s: %v", id, v)
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
		if err := pack.Sign(dst, key); err != nil {
			t.Fatalf("%s: sign: %v", id, err)
		}
		if m.Glitched.Service != "" {
			services = append(services, struct {
				id      string
				dir     string
				service string
			}{id: id, dir: dst, service: m.Glitched.Service})
		}
	}
	if len(ids) == 0 {
		t.Fatal("found no built-in plugins")
	}

	q := make(chan func(), 1024)
	h := plugins.Open(t.TempDir(), plugins.Options{
		Post:        func(fn func()) { q <- fn },
		Bundled:     os.DirFS(root),
		ReleaseKeys: []string{key.Public()},
	})
	t.Cleanup(h.Close)
	installed := make(map[string]plugins.Plugin, len(ids))
	for _, p := range h.Installed() {
		installed[p.ID] = p
	}
	for _, id := range ids {
		p, ok := installed[id]
		if !ok {
			t.Errorf("%s: missing from Installed", id)
			continue
		}
		if err := h.Failed(id); err != nil {
			t.Errorf("%s: failed to load: %v", id, err)
		} else if p.Err != nil {
			t.Errorf("%s: plugin error: %v", id, p.Err)
		} else if !p.Enabled {
			t.Errorf("%s: not enabled (trust=%v)", id, p.Trust)
		}
	}
	for _, svc := range services {
		p, err := plugin.Load(svc.dir, plugin.Options{
			Main:            svc.service,
			Prelude:         plugins.Prelude,
			Contract:        plugins.Contract,
			ContractVersion: plugins.ContractVersion,
			DataDir:         t.TempDir(),
		})
		if err != nil {
			t.Errorf("%s (%s): %v", svc.id, svc.service, err)
			continue
		}
		p.Shutdown()
	}
}
