package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/daemon"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
)

// The panel shows the workspace's uncommitted change to the file the
// agent edited: HEAD before, the working tree after. Long lines wrap until
// the button says not to. The hunk bar steps between changes and marks
// one reviewed from the keyboard; a file row's actions mark the file
// reviewed and copy its path.
func TestDiffFileActions(t *testing.T) {
	h, th := checked(t)
	h.click("Edit " + sidebarPath)
	h.diffsRead()
	d := h.diffOf(th, sidebarPath)
	if d.Old.String() != sidebarBefore || d.New.String() != sidebarAfter {
		t.Fatalf("the diff compares %q with %q, want HEAD's text with the working tree's", d.Old.String(), d.New.String())
	}
	n := len(d.Hunks())
	if n < 2 {
		t.Fatalf("%d changes in the diff, want at least 2", n)
	}
	if !d.Wrap {
		t.Error("long lines don't wrap by default")
	}
	h.click("Wrap long lines")
	h.tt.Frame()
	if d.Wrap {
		t.Error("long lines still wrap after the button")
	}
	h.tt.Key(ui.Cmd, ui.KeyBracketRight)
	if i, _ := d.CurrentHunk(); i != 1 {
		t.Errorf("⌘] went to change %d, want 1", i)
	}
	h.tt.Key(ui.Cmd, ui.KeyBracketLeft)
	if i, _ := d.CurrentHunk(); i != 0 {
		t.Errorf("⌘[ went to change %d, want 0", i)
	}
	h.tt.Key(ui.Cmd|ui.Shift, ui.KeyEnter)
	if len(d.Hunks()) != n-1 {
		t.Errorf("%d changes after ⇧⌘↩, want %d", len(d.Hunks()), n-1)
	}
	h.hover("Diff of " + sidebarPath)
	h.tt.Advance(time.Second)
	h.click("Mark reviewed")
	if !d.reviewed {
		t.Error("file not marked reviewed")
	}
	h.click("Copy path")
	if h.tt.Clipboard() != sidebarPath {
		t.Errorf("clipboard %q", h.tt.Clipboard())
	}
	if _, ok := h.tt.Find("Revert file"); ok {
		t.Error("the panel offers to revert the file")
	}
}

// No action of the panel changes a file in the workspace, its repository
// included: every one only reads or marks the review.
func TestNoDiffActionChangesTheWorkspace(t *testing.T) {
	was, starts := editorCLI, startEditor
	editorCLI, startEditor = func() string { return "cursor" }, func(string, string) error { return nil }
	t.Cleanup(func() { editorCLI, startEditor = was, starts })
	h, th := checked(t)
	ws := th.View.Workspace
	before := snapshot(t, ws)
	h.click("Edit " + sidebarPath)
	h.diffsRead()
	d := h.diffOf(th, sidebarPath)
	r, ok := h.tt.Find("New text")
	if !ok {
		t.Fatal("no diff view")
	}
	h.tt.Move(r.X+r.W/2, r.Y+8)
	h.tt.Advance(time.Second)
	n := len(d.Hunks())
	h.click("Mark change reviewed")
	if len(d.Hunks()) != n-1 {
		t.Fatalf("%d changes after marking one of %d reviewed", len(d.Hunks()), n)
	}
	h.tt.Key(ui.Cmd, ui.KeyBracketRight)
	h.tt.Key(ui.Cmd, ui.KeyBracketLeft)
	h.tt.Key(ui.Cmd|ui.Shift, ui.KeyEnter)
	h.click("Wrap long lines")
	h.tt.SetSize(1600, 900)
	h.tt.Frame()
	h.click("Split")
	h.click("Unified")
	h.hover("Diff of " + sidebarPath)
	h.tt.Advance(time.Second)
	for _, label := range []string{"Mark reviewed", "Copy path", "Open in editor"} {
		h.click(label)
	}
	for len(d.Hunks()) > 0 {
		d.GoToHunk(0)
		h.tt.Frame()
		h.tt.Key(ui.Cmd|ui.Shift, ui.KeyEnter)
	}
	h.click("Close diff")
	h.tt.Advance(time.Second)
	if after := snapshot(t, ws); !maps.Equal(after, before) {
		t.Fatalf("the workspace changed under the panel:\n%v\nwas\n%v", after, before)
	}
}

// snapshot is every file under dir: its kind, size, time and content.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := e.Info()
		if err != nil {
			return err
		}
		var data []byte
		if fi.Mode().IsRegular() {
			if data, err = os.ReadFile(path); err != nil {
				return err
			}
		}
		files[path] = fmt.Sprintf("%v %d %v %x", fi.Mode(), fi.Size(), fi.ModTime().UnixNano(), sha256.Sum256(data))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// A file HEAD lacks shows whole, as added.
func TestANewFileIsAllAdded(t *testing.T) {
	ws := gitWorkspace(t)
	const path = "apps/web/src/components/ChevronIcon.tsx"
	if err := os.WriteFile(filepath.Join(ws, path), []byte("export const x = 1;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := loaded(ws, path)
	if add, del := d.Stats(); d.Old.String() != "" || add == 0 || del != 0 || d.noHead != "" {
		t.Fatalf("a new file: before %q, +%d −%d, HEAD unknown as %q; want nothing before and its line added", d.Old.String(), add, del, d.noHead)
	}
}

// loaded is the panel's viewer of the file path names in the workspace,
// read now.
func loaded(ws, path string) *fileDiff {
	return readDiff(ws, path, time.Time{})
}

// readNow is the app's viewer of the thread's file once a read of it, as
// it is now, lands.
func readNow(t *testing.T, a *app, p *posts, th thread, f fileChange) *fileDiff {
	t.Helper()
	deadline := time.After(30 * time.Second)
	for {
		p.run()
		_, busy := a.reading[diffKey{th.id(), f.path}]
		if d := a.diffFor(th, f); d != nil && !busy {
			return d
		}
		select {
		case <-p.wake:
		case <-deadline:
			t.Fatalf("no diff of %s", f.path)
		}
	}
}

// posted is the app before its manager starts, with what it posts queued.
func posted(t *testing.T) (*app, *posts) {
	p := &posts{wake: make(chan struct{}, 1)}
	return newApp(t.TempDir(), nil, p.post), p
}

// The panel shows only a file in the workspace, which an agent's edit
// could name otherwise; it says why it shows none.
func TestTheDiffShowsOnlyWorkspaceFiles(t *testing.T) {
	for _, tc := range []struct {
		name string
		// setup makes the workspace and the file the edit names, and
		// returns the path the edit gives.
		setup func(t *testing.T) (ws, path string)
		why   string
	}{
		{"climbs out with ..", func(t *testing.T) (string, string) {
			ws := gitWorkspace(t)
			write(t, filepath.Join(filepath.Dir(ws), "victim.txt"))
			return ws, "../victim.txt"
		}, "outside the workspace"},
		{"absolute, outside", func(t *testing.T) (string, string) {
			return gitWorkspace(t), write(t, filepath.Join(t.TempDir(), "victim.txt"))
		}, "outside the workspace"},
		{"absolute, in another repository", func(t *testing.T) (string, string) {
			return gitWorkspace(t), filepath.Join(gitWorkspace(t), sidebarPath)
		}, "outside the workspace"},
		{"a link out of the workspace", func(t *testing.T) (string, string) {
			ws := gitWorkspace(t)
			link(t, write(t, filepath.Join(t.TempDir(), "victim.txt")), filepath.Join(ws, "link.txt"))
			return ws, "link.txt"
		}, "symbolic link"},
		{"in a linked folder out of the workspace", func(t *testing.T) (string, string) {
			ws, out := gitWorkspace(t), t.TempDir()
			link(t, out, filepath.Join(ws, "linked"))
			write(t, filepath.Join(out, "victim.txt"))
			return ws, "linked/victim.txt"
		}, "outside the workspace"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ws, path := tc.setup(t)
			d := loaded(ws, path)
			if d.DiffState != nil || !strings.Contains(d.unshown, tc.why) {
				t.Fatalf("shown %v, saying %q; want it not shown, as %s", d.DiffState != nil, d.unshown, tc.why)
			}
		})
	}
}

// Where git can't say what HEAD has, the panel shows the working file
// alone, says why, and counts no change.
func TestAnUnknownHeadShowsTheFileAlone(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T) (ws, path string)
		why   string
	}{
		{"in no repository", func(t *testing.T) (string, string) {
			ws := t.TempDir()
			write(t, filepath.Join(ws, "notes.md"))
			return ws, "notes.md"
		}, "isn't in a git work tree"},
		{"a repository with no commits", func(t *testing.T) (string, string) {
			ws := t.TempDir()
			git(t, ws, "init", "-q")
			write(t, filepath.Join(ws, "notes.md"))
			return ws, "notes.md"
		}, "no commits"},
		{"where git isn't installed", func(t *testing.T) (string, string) {
			ws := gitWorkspace(t)
			t.Setenv("PATH", t.TempDir())
			return ws, sidebarPath
		}, "git isn't installed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ws, path := tc.setup(t)
			want, err := os.ReadFile(filepath.Join(ws, path))
			if err != nil {
				t.Fatal(err)
			}
			a, p := posted(t)
			th := thread{nil, &client.Thread{View: api.Thread{ID: api.NewID(), Workspace: ws}}}
			d := readNow(t, a, p, th, fileChange{path: path})
			if !strings.Contains(d.noHead, tc.why) || d.DiffState == nil || d.New.String() != string(want) {
				t.Fatalf("HEAD unknown as %q, showing %v; want the working file shown, as %s", d.noHead, d.DiffState != nil, tc.why)
			}
			if add, del := a.diffStats(th, fileChange{path: path}); add+del != 0 {
				t.Errorf("with HEAD unknown the file counts +%d −%d", add, del)
			}
		})
	}
}

// A git that hangs leaves HEAD's side unknown once its time is up, rather
// than the panel reading forever.
func TestASlowGitEndsUnknown(t *testing.T) {
	ws := gitWorkspace(t)
	slow := t.TempDir()
	// sleep outlives the shell it is killed with, holding git's output.
	if err := os.WriteFile(filepath.Join(slow, "git"), []byte("#!/bin/sh\nsleep 30; :\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", slow+string(os.PathListSeparator)+os.Getenv("PATH"))
	was := gitTimeout
	gitTimeout = 200 * time.Millisecond
	t.Cleanup(func() { gitTimeout = was })
	begun := time.Now()
	d := loaded(ws, sidebarPath)
	if took := time.Since(begun); took > 5*time.Second {
		t.Fatalf("the read took %v", took)
	}
	if !strings.Contains(d.noHead, "git took over") || d.DiffState == nil || d.New.String() != sidebarAfter {
		t.Fatalf("HEAD unknown as %q, showing %v; want the working file shown, as git took too long", d.noHead, d.DiffState != nil)
	}
}

// An app started from a git hook, or a shell in another repository, has
// git's variables set; HEAD's side is still the workspace's own.
func TestInheritedGitVariablesDontChangeHead(t *testing.T) {
	ws, other := gitWorkspace(t), gitWorkspace(t)
	if err := os.WriteFile(filepath.Join(other, sidebarPath), []byte("another repository's\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, other, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qam", "other")
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_WORK_TREE", other)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(other, ".git", "index"))
	if d := loaded(ws, sidebarPath); d.noHead != "" || d.Old.String() != sidebarBefore {
		t.Fatalf("HEAD's side is %q (unknown as %q), want the workspace's", d.Old.String(), d.noHead)
	}
}

// In a partial clone, HEAD's blob may not be there yet; reading the diff
// leaves it unknown rather than fetching it into .git.
func TestAPartialCloneIsNotFetchedFrom(t *testing.T) {
	origin := gitWorkspace(t)
	git(t, origin, "config", "uploadpack.allowFilter", "true")
	clone := filepath.Join(t.TempDir(), "clone")
	git(t, origin, "clone", "-q", "--no-checkout", "--filter=blob:none", "file://"+origin, clone)
	file := filepath.Join(clone, sidebarPath)
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(sidebarAfter), 0o644); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, filepath.Join(clone, ".git"))
	d := loaded(clone, sidebarPath)
	if !maps.Equal(before, snapshot(t, filepath.Join(clone, ".git"))) {
		t.Fatal("reading the diff fetched into .git")
	}
	if d.noHead == "" {
		t.Fatalf("HEAD's side is %q, want it unknown while its blob is missing", d.Old.String())
	}
}

// A read of a file the agent has edited again since lands too late: the
// newer read shows, whichever lands first.
func TestASupersededReadIsDropped(t *testing.T) {
	ws := gitWorkspace(t)
	a, p := posted(t)
	th := thread{nil, &client.Thread{View: api.Thread{ID: api.NewID(), Workspace: ws}}}
	older, newer := time.Unix(1, 0), time.Unix(2, 0)
	a.diffFor(th, fileChange{path: sidebarPath, stamp: older})
	a.diffFor(th, fileChange{path: sidebarPath, stamp: newer})
	deadline := time.After(30 * time.Second)
	for {
		p.mu.Lock()
		n := len(p.q)
		p.mu.Unlock()
		if n == 2 {
			break
		}
		select {
		case <-p.wake:
		case <-deadline:
			t.Fatalf("%d of the two reads landed", n)
		}
	}
	slices.Reverse(p.q)
	p.run()
	if d := a.diffs[diffKey{th.id(), sidebarPath}]; d == nil || !d.stamp.Equal(newer) {
		t.Fatal("the diff shows the older read, or none")
	}
}

// The diffs of a thread gone, as with its server, go with it, and a read
// of it still under way lands nowhere.
func TestAGoneThreadsDiffsAreDropped(t *testing.T) {
	ws := gitWorkspace(t)
	a, p := posted(t)
	th := thread{nil, &client.Thread{View: api.Thread{ID: api.NewID(), Workspace: ws}}}
	readNow(t, a, p, th, fileChange{path: sidebarPath})
	a.diffFor(th, fileChange{path: "README.md", stamp: time.Unix(1, 0)})
	a.pruneDiffs()
	for !p.run() {
		<-p.wake
	}
	if len(a.diffs)+len(a.reading) != 0 {
		t.Fatalf("%d diffs and %d reads kept of a thread no engine has", len(a.diffs), len(a.reading))
	}
}

// The panel reads and diffs a file off the UI thread: a frame never waits
// on git or the disk, and shows the file once the read lands.
func TestTheDiffIsReadOffTheUIThread(t *testing.T) {
	ws := gitWorkspace(t)
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	slow := t.TempDir()
	if err := os.WriteFile(filepath.Join(slow, "git"), []byte("#!/bin/sh\nsleep 1\nexec "+real+" \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", slow+string(os.PathListSeparator)+os.Getenv("PATH"))
	a, p := posted(t)
	th := thread{nil, &client.Thread{View: api.Thread{ID: api.NewID(), Workspace: ws}}}
	f := fileChange{path: sidebarPath}
	begun := time.Now()
	for range 3 {
		if a.diffFor(th, f) != nil {
			t.Fatal("a diff before its read landed")
		}
	}
	if took := time.Since(begun); took > 500*time.Millisecond {
		t.Fatalf("three frames' diffs took %v, waiting on git", took)
	}
	if d := readNow(t, a, p, th, f); d.New.String() != sidebarAfter {
		t.Fatalf("the read landed with %q", d.New.String())
	}
}

// A file over 1 MB, or a binary one, isn't read whole or diffed: the
// panel says why.
func TestALargeOrBinaryFileIsNotShown(t *testing.T) {
	ws := gitWorkspace(t)
	for name, tc := range map[string]struct {
		data []byte
		why  string
	}{
		"large.txt": {bytes.Repeat([]byte("a line of text\n"), maxDiffBytes/10), "over 1 MB"},
		"image.png": {[]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), "binary"},
	} {
		if err := os.WriteFile(filepath.Join(ws, name), tc.data, 0o644); err != nil {
			t.Fatal(err)
		}
		if d := loaded(ws, name); d.DiffState != nil || !strings.Contains(d.unshown, tc.why) {
			t.Errorf("%s: shown %v, saying %q; want it not shown, as %s", name, d.DiffState != nil, d.unshown, tc.why)
		}
	}
}

// write makes a file of the user's, and returns its path.
func write(t *testing.T, file string) string {
	t.Helper()
	if err := os.WriteFile(file, []byte("the user's own text\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return file
}

func link(t *testing.T, target, name string) {
	t.Helper()
	if err := os.Symlink(target, name); err != nil {
		t.Skip("no symbolic links:", err)
	}
}

func git(t *testing.T, ws string, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", append([]string{"-C", ws}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}

// An edit that names a file outside the workspace shows no diff, and the
// panel says why.
func TestAFileOutsideTheWorkspaceIsNotShown(t *testing.T) {
	ws := gitWorkspace(t)
	write(t, filepath.Join(filepath.Dir(ws), "victim.txt"))
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New([]provider.Event{
		provider.Work{Item: "e", Kind: "edit", Title: "Edit ../victim.txt", Status: api.WorkCompleted, Path: "../victim.txt"},
		provider.TextDelta{Text: "Edited it."},
	}))
	seed(t, d, ws, "Escape", "Go.")
	h := start(t, dir)
	h.a.select_(h.thread("Escape"))
	h.until("its transcript", func() bool { return h.tt.HasText("Edited it.") })
	h.tt.Advance(time.Second)
	h.click("Edit ../victim.txt")
	h.diffsRead()
	if !h.tt.HasText("Not shown: the file is outside the workspace.") || h.tt.HasText("the user's own text") {
		t.Errorf("the panel doesn't say why; texts %q", h.tt.Texts())
	}
}

// The primary review button's text contrasts with its face in every
// theme, hovered or not; the dark themes' greens are light.
func TestReviewButtonContrast(t *testing.T) {
	for _, th := range themes {
		for _, shade := range []float32{0.35, 0.2} {
			bg := th.colors.green.Mix(ui.Hex("#000000"), shade)
			if r := contrast(onColor(bg), bg); r < 4.5 {
				t.Errorf("%s: %.2f:1 on the review button at shade %.2f", th.name, r, shade)
			}
		}
	}
}

// A thread on a server has its files there: the panel lists them, and
// says so in place of a preview.
func TestARemoteThreadsDiffSaysWhereItsFilesAre(t *testing.T) {
	rdir := tempDir(t)
	token, err := daemon.Pair(rdir, "gui")
	if err != nil {
		t.Fatal(err)
	}
	r := engine(t, daemon.Config{Dir: rdir, Listen: []string{"127.0.0.1:0"}}, fake.New(checks))
	seed(t, r, "/srv/web", "Sort the sidebar", "Sort it.")
	dir := tempDir(t)
	engine(t, daemon.Config{Dir: dir}, fake.New(checks))
	h := start(t, dir)
	if err := h.addServer(client.Server{Label: "devbox", Addr: r.Addrs()[0].String(), Token: token}); err != nil {
		t.Fatal(err)
	}
	h.a.select_(h.thread("Sort the sidebar"))
	h.until("its transcript", func() bool { return h.tt.HasText("One test fails.") })
	h.click("Edit " + sidebarPath)
	h.diffsRead()
	if !h.tt.HasText("The files are on devbox.") || !h.tt.HasText(sidebarPath) {
		t.Fatalf("the diff of a server's thread: %q", h.tt.Texts())
	}
}

// twoEdits is a turn that edits the sidebar and the readme.
var twoEdits = []provider.Event{
	provider.Work{Item: "e", Kind: "edit", Title: "Edit " + sidebarPath, Status: api.WorkCompleted, Path: sidebarPath},
	provider.Work{Item: "r", Kind: "edit", Title: "Edit README.md", Status: api.WorkCompleted, Path: "README.md"},
	provider.TextDelta{Text: "Edited both."},
}

// Each thread's diff is its own: which file it shows and the files marked
// reviewed. A mark goes once the agent edits the file again.
func TestEachThreadReviewsItsOwnDiff(t *testing.T) {
	ws := gitWorkspace(t)
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(twoEdits))
	seed(t, d, ws, "First", "Edit both.")
	seed(t, d, ws, "Second", "Edit both.")
	h := start(t, dir)
	first, second := h.thread("First"), h.thread("Second")
	open := func(th thread, path string) {
		h.a.select_(th)
		h.until("its transcript", func() bool { return len(changedFiles(entriesOf(th))) == 2 })
		h.tt.Advance(time.Second)
		h.click("Edit " + path)
		h.diffsRead()
	}
	open(first, "README.md")
	h.diffOf(first, sidebarPath).reviewed = true
	open(second, sidebarPath)
	if h.diffOf(second, sidebarPath).reviewed {
		t.Error("marking the file reviewed in one thread marked it in another")
	}
	h.a.select_(first)
	h.tt.Advance(time.Second)
	if h.a.diffFile[first.id()] != "README.md" {
		t.Errorf("back in the first thread its diff shows %q, want the README it showed", h.a.diffFile[first.id()])
	}

	h.a.draft = "Edit both again."
	h.tt.Frame()
	h.click("Send")
	h.until("the second turn", func() bool { return strings.Count(texts(entriesOf(first)), "Edited both.") == 2 })
	h.tt.Advance(time.Second)
	if h.diffOf(first, sidebarPath).reviewed {
		t.Error("the mark stayed after the agent edited the file again")
	}
}

// Open in editor opens the file an edit names: in the workspace when its
// path is relative, as it is when absolute.
func TestOpenInEditorOpensTheEditedFile(t *testing.T) {
	ws := gitWorkspace(t)
	abs := filepath.Join(ws, "README.md")
	var opened []string
	was, starts := editorCLI, startEditor
	editorCLI, startEditor = func() string { return "cursor" }, func(ed, path string) error { opened = append(opened, path); return nil }
	t.Cleanup(func() { editorCLI, startEditor = was, starts })
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New([]provider.Event{
		provider.Work{Item: "e", Kind: "edit", Title: "Edit " + sidebarPath, Status: api.WorkCompleted, Path: sidebarPath},
		provider.Work{Item: "r", Kind: "edit", Title: "Edit " + abs, Status: api.WorkCompleted, Path: abs},
		provider.TextDelta{Text: "Edited both."},
	}))
	seed(t, d, ws, "Both", "Edit both.")
	h := start(t, dir)
	h.a.select_(h.thread("Both"))
	h.until("its transcript", func() bool { return h.tt.HasText("Edited both.") })
	h.tt.Advance(time.Second)
	h.click("Edit " + sidebarPath)
	h.diffsRead()
	for _, path := range []string{sidebarPath, abs} {
		r, ok := h.tt.Find("Diff of " + path)
		if !ok {
			t.Fatalf("no row for %s", path)
		}
		h.tt.Move(r.X+r.W/2, r.Y+r.H/2)
		h.tt.Advance(time.Second)
		h.click("Open in editor")
	}
	if want := []string{filepath.Join(ws, sidebarPath), abs}; !slices.Equal(opened, want) {
		t.Fatalf("opened %q, want %q", opened, want)
	}
}

// A split diff's panel goes without a panic, however it goes: closed, with
// its divider focused, for the preview in its place, or with the page. A
// panel going with an Exit is painted from its last frame, where MyGo's
// divider reads an Element of the frame that built it. (The page goes
// whole, its panels with it, painting no copy of them.)
func TestASplitDiffGoesWithoutAPanic(t *testing.T) {
	for name, leave := range map[string]func(h *harness){
		"closed": func(h *harness) { h.click("Close diff") },
		"closed with its divider focused": func(h *harness) {
			for range 40 {
				if h.tt.Key(0, ui.KeyTab); h.tt.Focused("Divider") {
					break
				}
			}
			if !h.tt.Focused("Divider") {
				h.t.Fatal("Tab never reached the divider")
			}
			h.click("Close diff")
		},
		"for the preview": func(h *harness) { h.tt.Key(ui.Cmd|ui.Shift, ui.KeyP) },
		"with the page":   func(h *harness) { h.a.router.Push("/settings/general") },
	} {
		t.Run(name, func(t *testing.T) {
			h, th := checked(t)
			h.tt.SetSize(1600, 900)
			h.a.showDiff = true
			h.tt.Frame()
			h.diffOf(th, sidebarPath)
			settlePlugins(t, h.a, h.tt, h.p)
			h.click("Split")
			if _, ok := h.tt.Find("Divider"); !ok || h.a.layout() != editor.DiffSplit {
				t.Fatal("the diff isn't split")
			}
			h.tt.Advance(time.Second)
			leave(h)
			h.tt.Advance(time.Second)
			settlePlugins(t, h.a, h.tt, h.p)
			if h.a.showDiff && h.a.router.Path() != "/settings/general" {
				t.Error("the diff panel didn't go")
			}
		})
	}
}
