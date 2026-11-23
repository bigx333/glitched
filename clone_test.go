package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/jsonfile"
)

func TestParseCloneSource(t *testing.T) {
	for _, tc := range []struct {
		in, url, repo, name string
	}{
		{"bigx333/glitched", "https://github.com/bigx333/glitched.git", "bigx333/glitched", "glitched"},
		{"  bigx333/glitched.git ", "https://github.com/bigx333/glitched.git", "bigx333/glitched", "glitched"},
		{"o-1/my_repo.js", "https://github.com/o-1/my_repo.js.git", "o-1/my_repo.js", "my_repo.js"},
		{"https://github.com/bigx333/glitched", "https://github.com/bigx333/glitched.git", "", "glitched"},
		{"https://github.com/bigx333/glitched.git", "https://github.com/bigx333/glitched.git", "", "glitched"},
		{"https://github.com/bigx333/glitched/tree/capy/branch", "https://github.com/bigx333/glitched.git", "", "glitched"},
		{"https://www.github.com/bigx333/glitched/blob/main/README.md", "https://github.com/bigx333/glitched.git", "", "glitched"},
		{"https://gitlab.com/group/sub/project.git", "https://gitlab.com/group/sub/project.git", "", "project"},
		{"https://git.example.com/scm/repo/", "https://git.example.com/scm/repo/", "", "repo"},
		{"ssh://git@example.com:2222/o/r.git", "ssh://git@example.com:2222/o/r.git", "", "r"},
		{"git@github.com:bigx333/glitched.git", "git@github.com:bigx333/glitched.git", "", "glitched"},
		{"git@gitlab.com:group/sub/thing", "git@gitlab.com:group/sub/thing", "", "thing"},
		{"git://example.com/r.git", "git://example.com/r.git", "", "r"},
		{"file:///srv/git/lighthouse.git", "file:///srv/git/lighthouse.git", "", "lighthouse"},
	} {
		got, ok := parseCloneSource(tc.in)
		if want := (cloneSource{tc.url, tc.repo, tc.name}); !ok || got != want {
			t.Errorf("parseCloneSource(%q) = %+v, %v; want %+v", tc.in, got, ok, want)
		}
	}
	for _, in := range []string{
		"", "glitched", "bigx333/", "/glitched", "./glitched", "bigx333/..", "../x", "o/n/x",
		"o/n!", "o n/r", "https://github.com/bigx333", "https://github.com/", "https:///o/r.git",
		"ftp://example.com/r.git", "ssh:///r.git", "file:///", "https://example.com/", "git@github.com:", "mailto:a@b",
	} {
		if got, ok := parseCloneSource(in); ok {
			t.Errorf("parseCloneSource(%q) = %+v, want it refused", in, got)
		}
	}
}

// bareRepo is a bare repository, lighthouse.git, whose one commit holds
// README.md, and its file:// URL.
func bareRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	work := filepath.Join(dir, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("lighthouse\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bare := filepath.Join(dir, "lighthouse.git")
	for _, args := range [][]string{
		{"-C", work, "init", "-q"}, {"-C", work, "add", "."},
		{"-C", work, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "readme"},
		{"clone", "-q", "--bare", work, bare},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return "file://" + filepath.ToSlash(bare)
}

// fakePrograms has a clone find only the scripts given, by name, in a
// folder of their own, which it returns. Each records its args, one a
// line, in <name>.args there.
func fakePrograms(t *testing.T, scripts map[string]string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fakes are shell scripts")
	}
	dir := t.TempDir()
	for name, body := range scripts {
		script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> '" + filepath.Join(dir, name+".args") + "'\n" + body + "\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	usePath(t, dir)
	return dir
}

// usePath has a clone find its programs in path.
func usePath(t *testing.T, path string) {
	was := clonePath
	clonePath = func() string { return path }
	t.Cleanup(func() { clonePath = was })
}

// argsOf are the args the fake name in dir ran with, a line each, nil
// if it didn't run.
func argsOf(t *testing.T, dir, name string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name+".args"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

// openCloneSheet opens Clone repository… from the draft's workspace menu
// and chooses parent, a new folder, for the clone to go into.
func openCloneSheet(h *harness) string {
	h.t.Helper()
	parent := filepath.Join(h.t.TempDir(), "Projects")
	was := chooseCloneParent
	chooseCloneParent = func(*app) string { return parent }
	h.t.Cleanup(func() { chooseCloneParent = was })
	h.thread("SIP re-INVITE handling")
	h.click("New thread")
	h.click("Workspace")
	if err := h.tt.ChooseMenuItem("Clone repository…"); err != nil {
		h.t.Fatalf("%v; the menu has %q", err, h.tt.Menu())
	}
	h.tt.Frame()
	if !h.tt.HasText("Clone a repository") {
		h.t.Fatalf("no sheet; the window shows %q", h.tt.Texts())
	}
	if !h.tt.Focused("URL or owner/name") {
		h.t.Fatal("the sheet's field doesn't have the focus")
	}
	h.click("Choose…")
	h.until("the parent chosen", func() bool { return h.a.clone.parent == parent })
	return parent
}

// cloneFrom types source into the sheet's field, presses Clone, and
// waits for the clone to end.
func cloneFrom(h *harness, source string) {
	h.t.Helper()
	h.click("URL or owner/name")
	h.tt.Type(source)
	h.tt.Frame()
	h.click("Clone")
	if h.a.clone.run == nil && h.a.clone.open && h.a.clone.failure == "" {
		h.t.Fatalf("Clone started nothing; the window shows %q", h.tt.Texts())
	}
	h.until("the clone to end", func() bool { return h.a.clone.run == nil })
	h.tt.Frame()
}

// A repository cloned through the sheet is a project, kept in
// projects.json with its chip, with a new thread in it; the parent
// chosen is the next clone's.
func TestCloneRepository(t *testing.T) {
	source := bareRepo(t)
	usePath(t, os.Getenv("PATH"))
	state := inboxDir(t)
	h := start(t, state)
	parent := openCloneSheet(h)
	h.tt.ClickAt(4, 4)
	h.tt.Frame()
	if !h.a.clone.open {
		t.Fatal("a click outside closed the sheet")
	}
	cloneFrom(h, source)
	dest := filepath.Join(parent, "lighthouse")
	if h.a.clone.open {
		t.Fatalf("the sheet stays open: %q", h.a.clone.failure)
	}
	if b, err := os.ReadFile(filepath.Join(dest, "README.md")); err != nil || string(b) != "lighthouse\n" {
		t.Fatalf("the clone holds %q, %v", b, err)
	}
	h.until("the draft in the clone", func() bool {
		cur := h.a.current()
		return cur.id().IsZero() && cur.View.Workspace == dest && cur.local()
	})
	h.untilShown("the project's chip", "lighthouse")
	var file []addedProject
	if err := jsonfile.Load(filepath.Join(state, "projects.json"), &file); err != nil {
		t.Fatal(err)
	}
	if len(file) != 1 || file[0].Workspace != dest {
		t.Fatalf("projects.json holds %v, want %s", file, dest)
	}
	h.a.openClone()
	if h.a.clone.parent != parent {
		t.Fatalf("the next clone goes into %q, want %q", h.a.clone.parent, parent)
	}
}

// A folder that's there and isn't empty is refused, and git doesn't run.
func TestCloneRefusesAFolderNotEmpty(t *testing.T) {
	fakes := fakePrograms(t, map[string]string{"git": "exit 0"})
	h := start(t, inboxDir(t))
	parent := openCloneSheet(h)
	dest := filepath.Join(parent, "lighthouse")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "notes.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	cloneFrom(h, "https://example.com/lighthouse.git")
	if !h.tt.HasText(dest + " already exists and isn't empty") {
		t.Fatalf("no refusal; the window shows %q", h.tt.Texts())
	}
	if args := argsOf(t, fakes, "git"); args != nil {
		t.Fatalf("git ran with %q", args)
	}
	if _, err := os.Stat(filepath.Join(dest, "notes.txt")); err != nil {
		t.Fatalf("the folder's file went: %v", err)
	}
}

// A clone that fails says why, offers Retry, and leaves no folder.
func TestCloneFailureOffersRetry(t *testing.T) {
	bareRepo(t)
	usePath(t, os.Getenv("PATH"))
	h := start(t, inboxDir(t))
	parent := openCloneSheet(h)
	cloneFrom(h, "file://"+filepath.ToSlash(filepath.Join(t.TempDir(), "missing.git")))
	if h.a.clone.failure == "" || !h.tt.HasText("Retry") {
		t.Fatalf("no failure or Retry; the window shows %q", h.tt.Texts())
	}
	if !h.tt.HasText(h.a.clone.failure) {
		t.Fatalf("the sheet doesn't say %q; it shows %q", h.a.clone.failure, h.tt.Texts())
	}
	if _, err := os.Stat(filepath.Join(parent, "missing")); !os.IsNotExist(err) {
		t.Fatalf("the failed clone left its folder: %v", err)
	}
	if strings.Contains(h.a.clone.failure, cloneHint) {
		t.Fatalf("a missing repository has the sign-in hint: %q", h.a.clone.failure)
	}
}

// Refused access says how to sign in, and no credentials from the URL.
func TestCloneRefusedSaysHowToSignIn(t *testing.T) {
	fakePrograms(t, map[string]string{"git": strings.Join([]string{
		"echo \"Cloning into '$5'...\" >&2",
		"echo 'remote: Repository not found.' >&2",
		"echo \"fatal: repository 'https://ghp_secret@github.com/o/n.git/' not found\" >&2",
		"exit 128",
	}, "\n")})
	h := start(t, inboxDir(t))
	openCloneSheet(h)
	cloneFrom(h, "https://ghp_secret@github.com/o/n.git")
	f := h.a.clone.failure
	if !strings.Contains(f, "remote: Repository not found.") || !strings.HasSuffix(f, cloneHint) || !h.tt.HasText(f) {
		t.Fatalf("the sheet says %q; the window shows %q", f, h.tt.Texts())
	}
	if strings.Contains(f, "ghp_secret") || !strings.Contains(f, "https://github.com/o/n.git/") {
		t.Fatalf("the failure keeps the credentials, or loses the URL: %q", f)
	}
}

// Cancel stops a clone and takes away the folder it made.
func TestCloneCancelled(t *testing.T) {
	fakePrograms(t, map[string]string{"git": "/bin/mkdir -p \"$5\"\necho 'Receiving objects:  10% (1/10)' >&2\nexec /bin/sleep 60"})
	h := start(t, inboxDir(t))
	parent := openCloneSheet(h)
	dest := filepath.Join(parent, "lighthouse")
	h.click("URL or owner/name")
	h.tt.Type("https://example.com/lighthouse.git")
	h.tt.Frame()
	h.click("Clone")
	h.until("the clone's progress", func() bool { return h.a.clone.line != "" })
	h.tt.Frame()
	if !h.tt.HasText("Cloning…") || !h.tt.HasText("Receiving objects:  10% (1/10)") {
		t.Fatalf("no progress; the window shows %q", h.tt.Texts())
	}
	if _, err := os.Stat(dest); err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	h.click("Cancel")
	h.tt.Frame()
	if h.a.clone.open || h.tt.HasText("Clone a repository") {
		t.Fatal("the sheet stays open")
	}
	for _, err := os.Stat(dest); !os.IsNotExist(err); _, err = os.Stat(dest) {
		if time.Since(began) > 10*time.Second {
			t.Fatalf("the cancelled clone's folder stays: %v", err)
		}
		h.p.run()
		time.Sleep(10 * time.Millisecond)
	}
	if slices.ContainsFunc(h.a.added, func(p addedProject) bool { return p.Workspace == dest }) {
		t.Fatal("the cancelled clone is a project")
	}
}

// owner/name clones with gh while gh is signed in to github.com, else
// with git, from GitHub.
func TestCloneOwnerName(t *testing.T) {
	gitClone := "/bin/mkdir -p \"$5\""
	for _, tc := range []struct {
		name    string
		scripts map[string]string
		ran     string
		args    func(dest string) []string
	}{
		{"gh signed in", map[string]string{"gh": "[ \"$1\" = auth ] && exit 0\n/bin/mkdir -p \"$4\"", "git": gitClone}, "gh",
			func(dest string) []string {
				return []string{"auth", "status", "--hostname", "github.com", "repo", "clone", "o/lighthouse", dest, "--", "--progress"}
			}},
		{"gh signed out", map[string]string{"gh": "exit 1", "git": gitClone}, "git",
			func(dest string) []string {
				return []string{"clone", "--progress", "--", "https://github.com/o/lighthouse.git", dest}
			}},
		{"no gh", map[string]string{"git": gitClone}, "git",
			func(dest string) []string {
				return []string{"clone", "--progress", "--", "https://github.com/o/lighthouse.git", dest}
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakes := fakePrograms(t, tc.scripts)
			h := start(t, inboxDir(t))
			dest := filepath.Join(openCloneSheet(h), "lighthouse")
			cloneFrom(h, "o/lighthouse")
			if got, want := argsOf(t, fakes, tc.ran), tc.args(dest); !slices.Equal(got, want) {
				t.Fatalf("%s ran with %q, want %q", tc.ran, got, want)
			}
			if tc.ran == "gh" && argsOf(t, fakes, "git") != nil {
				t.Fatalf("git ran too, with %q", argsOf(t, fakes, "git"))
			}
			h.until("the draft in the clone", func() bool { return h.a.current().View.Workspace == dest })
		})
	}
}

// The palette offers Clone repository… while the sheet is closed.
func TestPaletteClonesARepository(t *testing.T) {
	h := start(t, inboxDir(t))
	h.thread("SIP re-INVITE handling")
	h.a.openCommandPalette()
	h.a.cmdkQuery = "github repo"
	i := slices.IndexFunc(h.a.commands(), func(c command) bool { return c.id == "project.clone" })
	if i < 0 {
		t.Fatal("the palette finds no Clone repository… for \"github repo\"")
	}
	h.a.cmdkOpen = false
	h.a.commands()[i].run()
	h.tt.Frame()
	if !h.tt.HasText("Clone a repository") {
		t.Fatalf("no sheet; the window shows %q", h.tt.Texts())
	}
	if slices.ContainsFunc(h.a.actions(), func(c command) bool { return c.id == "project.clone" }) {
		t.Fatal("the palette offers a second clone sheet")
	}
	h.tt.Key(0, ui.KeyEscape)
	h.tt.Frame()
	if h.a.clone.open {
		t.Fatal("Escape left the sheet open")
	}
}

// A failed clone leaves an empty folder that was there before it, which
// is the user's, as it found it.
func TestCloneFailureKeepsAFolderItDidNotMake(t *testing.T) {
	bareRepo(t)
	usePath(t, os.Getenv("PATH"))
	h := start(t, inboxDir(t))
	parent := openCloneSheet(h)
	dest := filepath.Join(parent, "missing")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	cloneFrom(h, "file://"+filepath.ToSlash(filepath.Join(t.TempDir(), "missing.git")))
	if h.a.clone.failure == "" {
		t.Fatalf("the clone didn't fail; the window shows %q", h.tt.Texts())
	}
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("the failed clone took away the folder it found: %v", err)
	}
}

