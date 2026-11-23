package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func branchIn(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "branch", "--show-current").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

// worktreeThread starts a thread in a new worktree of the harness's git
// workspace from its menu, and returns it once the engine has made it.
func worktreeThread(t *testing.T, h *harness, ws, text string) thread {
	t.Helper()
	h.a.startIn(draft{env: h.local(), workspace: ws})
	h.tt.Frame()
	h.a.draft = text
	h.click("Workspace")
	h.tt.Advance(time.Second)
	if err := h.tt.ChooseMenuItem("New worktree"); err != nil {
		t.Fatalf("%v; the menu has %q", err, h.tt.Menu())
	}
	h.tt.Advance(time.Second)
	h.click("Send")
	var th thread
	h.until("the thread in its worktree", func() bool {
		th = h.a.current()
		return th.View.Branch != "" && th.View.LastTurn != nil && !th.View.Live()
	})
	return th
}

// A new thread started in a new worktree from the workspace menu runs in a
// worktree of the project's repository, says its branch where it shows
// where it runs, and stays under its project in the sidebar.
func TestANewThreadInANewWorktreeStaysUnderItsProject(t *testing.T) {
	h, seeded := workspaceHarness(t)
	ws := seeded.View.Workspace
	h.a.startIn(draft{env: h.local(), workspace: ws})
	h.tt.Frame()
	h.click("Workspace")
	h.tt.Advance(time.Second)
	if m := h.tt.Menu(); !slices.Contains(m, "Local folder") || !slices.Contains(m, "New worktree") {
		t.Fatalf("the menu of a draft in a repository has %q, want Local folder and New worktree", m)
	}
	h.tt.CloseMenu()
	h.tt.Advance(time.Second)

	th := worktreeThread(t, h, ws, "Check the certificates")
	if th.View.Project != ws || th.View.Workspace == ws || !strings.HasPrefix(th.View.Branch, "glitched/check-the-certificates-") {
		t.Fatalf("the thread runs in %q of %q on %q, want a worktree of %q on glitched/check-the-certificates-…", th.View.Workspace, th.View.Project, th.View.Branch, ws)
	}
	if got := branchIn(t, th.View.Workspace); got != th.View.Branch {
		t.Errorf("its worktree is on %q, want %q", got, th.View.Branch)
	}
	h.tt.Advance(time.Second)
	if !h.tt.HasText(th.View.Branch) {
		t.Errorf("nothing on screen names the branch %q", th.View.Branch)
	}
	if p := h.a.projectOf(th); p.key.workspace != ws || !slices.ContainsFunc(p.threads, func(x thread) bool { return x.id() == th.id() }) {
		t.Errorf("the thread is under the project %q, want %q", p.key.workspace, ws)
	}
	for _, p := range h.a.projects {
		if p.key.workspace == th.View.Workspace {
			t.Errorf("the worktree shows as a project of its own")
		}
	}
	h.a.startIn(draft{env: h.local(), workspace: ws})
	h.tt.Advance(time.Second)
	if h.a.newWorktree(h.a.current()) != nil {
		t.Errorf("the next draft asks for a worktree too, want the settings' Local")
	}
}

// Settings › General's New worktree is where a new thread starts until
// the menu says otherwise, and the button says so; a folder outside any
// repository offers no worktree at all.
func TestTheSettingsStartNewThreadsInAWorktreeOfARepositoryOnly(t *testing.T) {
	h, seeded := workspaceHarness(t)
	ws := seeded.View.Workspace
	h.a.settings.newThreadMode = 1
	h.a.startIn(draft{env: h.local(), workspace: ws})
	h.tt.Advance(time.Second)
	if want := "New worktree from " + branchIn(t, ws); !h.tt.HasText(want) {
		t.Errorf("the draft's button doesn't say %q", want)
	}
	plain := t.TempDir()
	h.a.startIn(draft{env: h.local(), workspace: plain})
	h.tt.Advance(time.Second)
	if h.a.newWorktree(h.a.current()) != nil || h.tt.HasText("New worktree from") {
		t.Errorf("a draft in a folder outside any repository asks for a worktree")
	}
	h.click("Workspace")
	h.tt.Advance(time.Second)
	if m := h.tt.Menu(); slices.Contains(m, "New worktree") || slices.Contains(m, "Local folder") {
		t.Errorf("the menu of a draft outside a repository has %q", m)
	}
}

// Deleting a thread in a worktree asks what becomes of the worktree: Keep
// worktree keeps it, Delete worktree removes a clean one, and one with
// changes goes only through Delete worktree and changes.
func TestDeletingAThreadInAWorktreeAsksAboutItsWorktree(t *testing.T) {
	h, seeded := workspaceHarness(t)
	ws := seeded.View.Workspace
	h.a.settings.confirmDelete = true
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }
	del := func(th thread, button string) {
		t.Helper()
		h.a.askDelete(th)
		h.tt.Advance(time.Second)
		if !h.tt.HasText(button) {
			t.Fatalf("the delete alert has no %q", button)
		}
		h.click(button)
		h.until("the delete", func() bool { _, ok := h.a.find(th.id()); return !ok })
	}

	kept := worktreeThread(t, h, ws, "Keep this one")
	del(kept, "Keep worktree")
	if !exists(kept.View.Workspace) {
		t.Errorf("Keep worktree removed it")
	}

	clean := worktreeThread(t, h, ws, "Clean one")
	del(clean, "Delete worktree")
	h.until("the clean worktree's removal", func() bool { return !exists(clean.View.Workspace) })

	dirty := worktreeThread(t, h, ws, "Dirty one")
	if err := os.WriteFile(filepath.Join(dirty.View.Workspace, "notes.txt"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.a.askDelete(dirty)
	h.tt.Advance(time.Second)
	if !h.tt.HasText("1 uncommitted change") || !h.tt.HasText("Delete worktree and changes") {
		t.Fatalf("the alert for a worktree with changes doesn't say so")
	}
	h.click("Delete worktree and changes")
	h.until("the delete", func() bool { _, ok := h.a.find(dirty.id()); return !ok })
	h.until("the worktree's removal", func() bool { return !exists(dirty.View.Workspace) })
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

// The worktree a draft asks for starts from the branch its button names,
// though a tag has its name, and a base picked for one project's draft
// isn't another's.
func TestADraftsWorktreeStartsFromTheBranchItNames(t *testing.T) {
	h, seeded := workspaceHarness(t)
	ws := seeded.View.Workspace
	branch := branchIn(t, ws)
	gitRun(t, ws, "commit", "-q", "--allow-empty", "-m", "tagged")
	gitRun(t, ws, "tag", branch)
	gitRun(t, ws, "reset", "-q", "--soft", "HEAD~1")
	th := worktreeThread(t, h, ws, "From the branch")
	if got, want := gitRun(t, th.View.Workspace, "rev-parse", "HEAD"), gitRun(t, ws, "rev-parse", "refs/heads/"+branch); got != want {
		t.Errorf("the worktree started at %s, want the branch %s's %s", got, branch, want)
	}

	h.a.worktree.base, h.a.worktree.baseIn = "picked-elsewhere", ws
	h.a.settings.newThreadMode = 1
	other := gitWorkspace(t)
	h.a.startIn(draft{env: h.local(), workspace: other})
	h.tt.Advance(time.Second)
	if w := h.a.newWorktree(h.a.current()); w == nil || w.Base != "refs/heads/"+branchIn(t, other) {
		t.Errorf("a draft in another project asks for %+v, want its own %q", w, branchIn(t, other))
	}
}

// Deleting the thread shown opens a draft in its project that starts a
// worktree from that project's branch, not one picked in another's.
func TestDeletingTheShownThreadLeavesABasePickedElsewhere(t *testing.T) {
	h, seeded := workspaceHarness(t)
	other := gitWorkspace(t)
	th := worktreeThread(t, h, other, "In the other project")
	h.a.worktree.base, h.a.worktree.baseIn = "only-first-project", seeded.View.Workspace
	h.a.settings.newThreadMode = 1
	h.a.deleteTree(th, false)
	h.until("the delete", func() bool { _, ok := h.a.find(th.id()); return !ok })
	h.tt.Advance(time.Second)
	d := h.a.current()
	if w := h.a.newWorktree(d); d.View.Workspace != other || w == nil || w.Base != "refs/heads/"+branchIn(t, other) {
		t.Errorf("the draft after the delete is in %s asking for %+v, want %s from its own %q", d.View.Workspace, w, other, branchIn(t, other))
	}
}

// The delete alert counts the changes in the worktree from its root, not
// in a repository nested where the thread runs.
func TestTheDeleteAlertCountsTheWorktreeNotARepositoryInIt(t *testing.T) {
	h, seeded := workspaceHarness(t)
	sub := filepath.Join(seeded.View.Workspace, filepath.Dir(sidebarPath))
	th := worktreeThread(t, h, sub, "In a subfolder")
	gitRun(t, th.View.Workspace, "init", "-q")
	gitRun(t, th.View.Workspace, "add", ".")
	gitRun(t, th.View.Workspace, "commit", "-q", "-m", "nested")
	root := strings.TrimSuffix(th.View.Workspace, string(filepath.Separator)+filepath.Dir(sidebarPath))
	if err := os.WriteFile(filepath.Join(root, "local-note"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if n := changesIn(th); n < 1 {
		t.Errorf("the alert counts %d changes, want the worktree root's note among them", n)
	}
}

// A base branch picked from the menu that shares its name with a tag is
// the branch the worktree starts from.
func TestAPickedBranchWithATagsNameIsTheBase(t *testing.T) {
	h, seeded := workspaceHarness(t)
	ws := seeded.View.Workspace
	gitRun(t, ws, "branch", "release")
	gitRun(t, ws, "commit", "-q", "--allow-empty", "-m", "tagged")
	gitRun(t, ws, "tag", "release")
	gitRun(t, ws, "reset", "-q", "--soft", "HEAD~1")
	delete(h.a.repos, ws)
	h.a.startIn(draft{env: h.local(), workspace: ws})
	h.a.worktree.picked, h.a.worktree.on = true, true
	h.tt.Frame()
	h.click("Workspace")
	h.tt.Advance(time.Second)
	if err := h.tt.ChooseMenuItem("Base branch", "release"); err != nil {
		t.Fatalf("%v; the menu has %q", err, h.tt.Menu())
	}
	h.tt.Advance(time.Second)
	if w := h.a.newWorktree(h.a.current()); w == nil || w.Base != "refs/heads/release" {
		t.Errorf("the draft asks for %+v, want refs/heads/release", w)
	}
}

// The delete alert counts the thread's own worktree, not another
// checkout of its repository nested where the thread runs.
func TestTheDeleteAlertCountsTheThreadsCheckoutNotAnotherOfItsRepository(t *testing.T) {
	h, seeded := workspaceHarness(t)
	sub := filepath.Join(seeded.View.Workspace, filepath.Dir(sidebarPath))
	th := worktreeThread(t, h, sub, "In a subfolder")
	if err := os.RemoveAll(th.View.Workspace); err != nil {
		t.Fatal(err)
	}
	gitRun(t, seeded.View.Workspace, "worktree", "add", "-q", "--detach", th.View.Workspace)
	if n := changesIn(th); n < 1 {
		t.Errorf("the alert counts %d changes, want the worktree's deleted files among them", n)
	}
}

// A new draft starts its worktree from the branch the folder is on now,
// not the one it was on for the draft before.
func TestANewDraftStartsFromTheBranchTheFolderIsOnNow(t *testing.T) {
	h, seeded := workspaceHarness(t)
	ws := seeded.View.Workspace
	h.a.settings.newThreadMode = 1
	h.a.startIn(draft{env: h.local(), workspace: ws})
	h.tt.Advance(time.Second)
	gitRun(t, ws, "switch", "-qc", "next")
	gitRun(t, ws, "commit", "-q", "--allow-empty", "-m", "next")
	h.a.startIn(draft{env: h.local(), workspace: ws})
	h.tt.Advance(time.Second)
	if w := h.a.newWorktree(h.a.current()); w == nil || w.Base != "refs/heads/next" {
		t.Errorf("the new draft asks for %+v, want refs/heads/next", w)
	}
}

// A branch whose name holds a space other than a newline is one choice.
func TestABranchWithASpaceInItsNameIsOneChoice(t *testing.T) {
	ws := gitWorkspace(t)
	b := "team/feature\u2003one"
	gitRun(t, ws, "branch", b)
	if got := repoOf(ws).branches; !slices.Contains(got, b) {
		t.Errorf("the branches are %q, want %q among them", got, b)
	}
}

// A Quick Ask starts its worktree from the branch its folder is on now.
func TestAQuickAskStartsFromTheBranchTheFolderIsOnNow(t *testing.T) {
	h, seeded := workspaceHarness(t)
	ws := seeded.View.Workspace
	h.a.settings.newThreadMode = 1
	h.a.startIn(draft{env: h.local(), workspace: ws})
	h.tt.Advance(time.Second)
	gitRun(t, ws, "switch", "-qc", "next")
	gitRun(t, ws, "commit", "-q", "--allow-empty", "-m", "next")
	want := gitRun(t, ws, "rev-parse", "HEAD")
	for i, p := range h.a.localProjects() {
		if p.key.workspace == ws {
			h.a.quick.project = i
		}
	}
	h.a.quick.query = "From Quick Ask"
	h.a.startQuickThread()
	var th thread
	h.until("the Quick Ask's worktree", func() bool { th = h.a.current(); return th.View.Branch != "" && th.View.LastTurn != nil })
	if got := gitRun(t, th.View.Workspace, "rev-parse", "HEAD"); got != want {
		t.Errorf("the Quick Ask started at %s, want next's %s", got, want)
	}
}

// New worktree picked in one draft isn't the next draft's choice, which
// is the one Settings › General makes.
func TestAPickedWorktreeIsNotTheNextDraftsChoice(t *testing.T) {
	h, seeded := workspaceHarness(t)
	h.a.startIn(draft{env: h.local(), workspace: seeded.View.Workspace})
	h.tt.Frame()
	h.click("Workspace")
	h.tt.Advance(time.Second)
	if err := h.tt.ChooseMenuItem("New worktree"); err != nil {
		t.Fatal(err)
	}
	h.a.startIn(draft{env: h.local(), workspace: gitWorkspace(t)})
	h.tt.Advance(time.Second)
	if w := h.a.newWorktree(h.a.current()); w != nil {
		t.Errorf("the next draft asks for %+v, want its folder as Settings says", w)
	}
}

// A bare repository isn't offered a worktree.
func TestABareRepositoryIsNotOfferedAWorktree(t *testing.T) {
	bare := filepath.Join(t.TempDir(), "bare.git")
	gitRun(t, filepath.Dir(bare), "clone", "-q", "--bare", gitWorkspace(t), bare)
	if repoOf(bare).ok {
		t.Error("a bare repository is offered a worktree")
	}
}

// The delete alert counts a worktree's changes whatever its project's
// folder is called.
func TestTheDeleteAlertCountsWhateverTheProjectIsCalled(t *testing.T) {
	h, _ := workspaceHarness(t)
	ws := filepath.Join(t.TempDir(), "two\nlines")
	gitRun(t, filepath.Dir(ws), "clone", "-q", gitWorkspace(t), ws)
	th := worktreeThread(t, h, ws, "Odd name")
	if err := os.WriteFile(filepath.Join(th.View.Workspace, "note"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if n := changesIn(th); n != 1 {
		t.Errorf("the alert counts %d changes, want 1", n)
	}
}

// Add project in a draft keeps New worktree picked in it.
func TestAddProjectKeepsTheDraftsWorktreeChoice(t *testing.T) {
	other := gitWorkspace(t)
	old := chooseFolder
	chooseFolder = func(*app) string { return other }
	t.Cleanup(func() { chooseFolder = old })
	h := start(t, inboxDir(t))
	h.a.startIn(draft{env: h.local(), workspace: gitWorkspace(t)})
	h.tt.Advance(time.Second)
	h.a.worktree.picked, h.a.worktree.on = true, true
	h.click("Add project")
	h.until("the added project's draft", func() bool { return h.a.current().View.Workspace == other })
	if w := h.a.newWorktree(h.a.current()); w == nil {
		t.Error("Add project dropped New worktree")
	}
}

// A base picked in a folder whose branch is gone since is not the base.
func TestABaseThatIsGoneIsNotTheBase(t *testing.T) {
	h, seeded := workspaceHarness(t)
	ws := seeded.View.Workspace
	gitRun(t, ws, "branch", "brief")
	h.a.settings.newThreadMode = 1
	h.a.startIn(draft{env: h.local(), workspace: ws})
	h.a.worktree.base, h.a.worktree.baseIn = "brief", ws
	gitRun(t, ws, "branch", "-D", "brief")
	h.a.repos = nil
	if w := h.a.newWorktree(h.a.current()); w == nil || w.Base != "refs/heads/"+branchIn(t, ws) {
		t.Errorf("the draft asks for %+v, want its current branch", w)
	}
}

// A draft with no project offers no worktree, even with Settings › General
// on New worktree and the scratch root in a repository; a project's draft
// lists Local folder and New worktree below the project rows.
func TestADraftWithNoProjectOffersNoWorktree(t *testing.T) {
	h, seeded := workspaceHarness(t)
	root := h.a.scratchRoot()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "root"}} {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	h.a.settings.newThreadMode = 1
	h.a.startIn(draft{env: h.local(), workspace: root})
	h.tt.Advance(time.Second)
	if h.a.newWorktree(h.a.current()) != nil || h.tt.HasText("New worktree from") {
		t.Errorf("a draft with no project asks for a worktree")
	}
	h.click("Workspace")
	h.tt.Advance(time.Second)
	if m := h.tt.Menu(); slices.Contains(m, "New worktree") || slices.Contains(m, "Local folder") {
		t.Errorf("the menu of a draft with no project has %q", m)
	}
	h.tt.CloseMenu()
	h.tt.Advance(time.Second)

	ws := seeded.View.Workspace
	h.a.startIn(draft{env: h.local(), workspace: ws})
	h.tt.Advance(time.Second)
	h.click("Workspace")
	h.tt.Advance(time.Second)
	m := h.tt.Menu()
	none, proj, local := slices.Index(m, "No project"), slices.Index(m, filepath.Base(ws)), slices.Index(m, "Local folder")
	if none < 0 || proj < 0 || local < 0 || !(none < proj && proj < local) || !slices.Contains(m, "New worktree") {
		t.Errorf("the menu of a project's draft is %q, want No project, then %s, then Local folder and New worktree", m, filepath.Base(ws))
	}
}
