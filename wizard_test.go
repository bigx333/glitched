package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/daemon"
	"glitched/internal/gittest"
	"glitched/internal/jsonfile"
	"glitched/internal/plugins"
	"glitched/internal/provider/fake"
	"glitched/internal/transport"
)

// ghBin is the bin dir TestMain puts ahead of the real PATH: its stub gh
// keeps the tests' engines off GitHub.
var ghBin string

// writeGH puts a gh of script, a shell script, in ghBin. On Windows a
// shell script is no gh.exe, which guard.Find alone finds: gh isn't
// installed there at all.
func writeGH(script string) {
	if err := os.WriteFile(filepath.Join(ghBin, "gh"), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		panic(err)
	}
}

// stubGH swaps in a gh of script for the engines of tests, and puts the
// default stub, which fails, back when t ends.
func stubGH(t *testing.T, script string) {
	t.Helper()
	writeGH(script)
	t.Cleanup(func() { writeGH("echo 'gh: stub' >&2\nexit 1") })
}

// stubRepos swaps in a gh that is signed in to github.com and whose api
// emits each repository, an object a line.
func stubRepos(t *testing.T, repos ...api.Repo) {
	t.Helper()
	var b strings.Builder
	b.WriteString("[ \"$1\" = auth ] && exit 0\n")
	for _, r := range repos {
		j, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		b.WriteString("echo '" + string(j) + "'\n")
	}
	stubGH(t, b.String())
}

// bareRepo is a bare repository, lighthouse.git, whose one commit holds
// README.md, and its file:// URL.
func bareRepo(t *testing.T) string {
	t.Helper()
	return bareRepoAt(t, filepath.Join(t.TempDir(), "lighthouse.git"))
}

// bareRepoAt makes that repository at bare, and is its file:// URL.
func bareRepoAt(t *testing.T, bare string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	work := filepath.Join(t.TempDir(), "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("lighthouse\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"-C", work, "init", "-q"}, {"-C", work, "add", "."},
		{"-C", work, "commit", "-qm", "readme"},
		{"clone", "-q", "--bare", work, bare},
	} {
		if out, err := gittest.Command(args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return "file://" + filepath.ToSlash(bare)
}

// nodeProjects are a server's Projects as its engine serves them, which
// count the clones asked of it; with hold set, a clone says one line and
// waits for its ctx to end, then closes stopped.
type nodeProjects struct {
	transport.Projects
	clones  atomic.Int32
	hold    bool
	stopped chan struct{}
	stop    func()
}

func (p *nodeProjects) Clone(ctx context.Context, req api.CloneRequest, progress func(string)) (api.Cloned, error) {
	p.clones.Add(1)
	if !p.hold {
		return p.Projects.Clone(ctx, req, progress)
	}
	progress("Receiving objects:  10% (1/10)")
	<-ctx.Done()
	close(p.stopped)
	return api.Cloned{}, ctx.Err()
}

// serverNode serves an engine of its own as a server does, on a loopback port
// whose one token is "any", and returns its Projects and its address.
func serverNode(t *testing.T) (*nodeProjects, string) {
	t.Helper()
	d := engine(t, daemon.Config{Dir: tempDir(t)}, fake.New(fake.Turn(false)))
	p := &nodeProjects{Projects: d.Endpoint().(transport.Projects), stopped: make(chan struct{})}
	tn := transport.Tailnet{Address: "127.0.0.1:0"}
	ctx, stop := context.WithCancel(context.Background())
	ln, err := tn.Listen(ctx)
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan struct{})
	go func() {
		defer close(served)
		key := transport.KeyOf("any")
		admit := func(_ *http.Request, proved func(transport.Key) bool) (transport.Grant, error) {
			if !proved(key) {
				return transport.Grant{}, transport.ErrRefused
			}
			return transport.Grant{Key: key, Done: ctx}, nil
		}
		(&transport.Server{Transport: tn, Endpoint: d.Endpoint(), Projects: p, Authorize: admit}).Serve(ctx, ln)
	}()
	p.stop = sync.OnceFunc(func() { stop(); <-served })
	t.Cleanup(p.stop)
	return p, ln.Addr().String()
}

// closedAddr is a loopback address nothing listens at.
func closedAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// wizardMachines is the app on dir's engine and two servers: laptop,
// which nothing answers at, and build-box, online.
func wizardMachines(t *testing.T, dir string) (*harness, *nodeProjects) {
	t.Helper()
	p, addr := serverNode(t)
	listServers(t, dir, client.Server{Label: "laptop", Addr: closedAddr(t), Token: "any"},
		client.Server{Label: "build-box", Addr: addr, Token: "any"})
	h := start(t, dir)
	h.until("build-box online and laptop offline", func() bool {
		b, l := h.a.serverEnv("build-box"), h.a.serverEnv("laptop")
		_, off := l.Status.(client.Offline)
		return h.a.reachable(b) && off
	})
	h.tt.Frame()
	return h, p
}

// showing is whether a text or a label of the window is s, whole.
func showing(h *harness, s string) bool { _, ok := h.tt.Find(s); return ok }

// newProject opens the wizard with the project strip's New project.
func newProject(h *harness) {
	h.t.Helper()
	h.click("New project")
	settlePlugins(h.t, h.a, h.tt, h.p)
	h.until("the wizard", func() bool { return showing(h, "Cancel") })
}

// pickAndCreate chooses Existing folder in the open wizard on this
// machine, which the folder chooseFolder gives answers, and creates the
// project.
func pickAndCreate(h *harness) {
	h.t.Helper()
	h.click("Existing folder")
	h.untilShown("Confirm", "Create the project")
	h.click("Create")
	h.tt.Frame()
}

// addFolder adds the folder chooseFolder gives as a project, through the
// wizard from the strip.
func addFolder(h *harness) {
	h.t.Helper()
	newProject(h)
	pickAndCreate(h)
}

// key presses key with mods and runs what that posts.
func (h *harness) key(mods ui.Modifiers, key ui.Key) {
	h.t.Helper()
	h.tt.Key(mods, key)
	h.tt.Frame()
	h.p.run()
	h.tt.Frame()
}

// replaceIn replaces what the field labelled label holds with s.
func (h *harness) replaceIn(label, s string) {
	h.t.Helper()
	h.click(label)
	h.tt.Key(ui.Cmd, ui.KeyA)
	h.tt.Type(s)
	h.tt.Frame()
}

// addedFile is projects.json in dir, nil while there is none.
func addedFile(t *testing.T, dir string) []addedProject {
	t.Helper()
	var ps []addedProject
	if err := jsonfile.Load(filepath.Join(dir, "projects.json"), &ps); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return ps
}

// folderTree is a folder of alpha (with deep in it), beta (a repository),
// .hidden and locked, which no one can read.
func folderTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"alpha/deep", "beta/.git", ".hidden", "locked"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(filepath.Join(root, "locked"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Join(root, "locked"), 0o755) })
	return root
}

// browseTo goes on build-box to Existing folder, and types root into the
// path field.
func browseTo(h *harness, root string) {
	h.t.Helper()
	newProject(h)
	h.click("build-box")
	h.click("Existing folder")
	h.untilShown("the browser", "Choose a folder on build-box")
	h.replaceIn("Path", root)
	h.key(0, ui.KeyEnter)
	h.until("the folder listed", func() bool { return showing(h, "alpha") })
}

// With one machine the wizard asks nothing of it: it opens on Source,
// with its first choice focused.
func TestTheWizardOnOneMachineStartsAtSource(t *testing.T) {
	h := start(t, inboxDir(t))
	h.thread("SIP re-INVITE handling")
	newProject(h)
	if !showing(h, "New project on this machine") || showing(h, "Which machine is it on?") {
		t.Fatalf("the wizard doesn't open on Source; it shows %q", h.tt.Texts())
	}
	if !h.tt.Focused("Existing folder") {
		t.Fatal("Existing folder doesn't have the focus")
	}
	if showing(h, "Back") {
		t.Fatal("the first step has a Back")
	}
}

// A machine that isn't online shows why, and neither a click nor the
// keyboard chooses it.
func TestTheWizardWontChooseAMachineOffline(t *testing.T) {
	h, _ := wizardMachines(t, inboxDir(t))
	newProject(h)
	if !showing(h, "Which machine is it on?") || !showing(h, "This machine") || !showing(h, "build-box") || !showing(h, "laptop") {
		t.Fatalf("no machines; the window shows %q", h.tt.Texts())
	}
	if !slices.ContainsFunc(h.tt.Texts(), func(s string) bool { return strings.HasPrefix(s, "Offline, retrying") }) {
		t.Fatalf("laptop doesn't say it's offline: %q", h.tt.Texts())
	}
	h.tt.Click("laptop")
	h.tt.Frame()
	if !showing(h, "Which machine is it on?") {
		t.Fatalf("a click chose laptop: %q", h.tt.Texts())
	}
	if !h.tt.Focused("This machine") {
		t.Fatal("This machine doesn't have the focus")
	}
	h.key(0, ui.KeyDown)
	if !h.tt.Focused("build-box") {
		t.Fatal("Down didn't skip laptop for build-box")
	}
	h.key(0, ui.KeyDown)
	if h.tt.Focused("laptop") || !h.tt.Focused("build-box") {
		t.Fatal("Down went to laptop")
	}
	h.key(0, ui.KeyEnter)
	if !showing(h, "New project on build-box") {
		t.Fatalf("Enter didn't choose build-box: %q", h.tt.Texts())
	}
}

// Back goes a step back from each step and keeps what was chosen there:
// the machine, the source, the folder, the clone's repository, its
// parent and its name.
func TestWizardBackKeepsTheChoices(t *testing.T) {
	root := folderTree(t)
	h, _ := wizardMachines(t, inboxDir(t))
	browseTo(h, root)
	h.click("alpha")
	h.until("alpha listed", func() bool { return showing(h, "deep") })
	h.click("Use this folder")
	alpha := filepath.Join(root, "alpha")
	h.untilShown("Confirm", alpha)
	h.click("Back")
	if !showing(h, "deep") || h.a.wizard.browse.field != alpha {
		t.Fatalf("Back from Confirm didn't keep the folder: %q", h.tt.Texts())
	}
	h.click("Back")
	if !h.tt.Focused("Existing folder") {
		t.Fatal("Back to Source doesn't keep Existing folder chosen")
	}
	h.click("Clone a repository")
	h.replaceIn("Repository", "o/lighthouse")
	if !showing(h, "Into ~/Projects") {
		t.Fatalf("a clone on a server doesn't go into ~/Projects there: %q", h.tt.Texts())
	}
	h.click("Choose…")
	h.untilShown("the parent's browser", "Clone into a folder on build-box")
	h.replaceIn("Path", root)
	h.key(0, ui.KeyEnter)
	h.until("the folder listed", func() bool { return showing(h, "alpha") })
	h.click("Clone here")
	h.untilShown("Clone", "Into "+root)
	h.click("Next")
	h.untilShown("Confirm", filepath.Join(root, "lighthouse"))
	h.replaceIn("Name", "beacon")
	h.untilShown("the name", filepath.Join(root, "beacon"))
	h.click("Back")
	if h.a.wizard.input != "o/lighthouse" || !showing(h, "Into "+root) || !h.tt.Focused("Repository") {
		t.Fatalf("Back to Clone lost the repository or its parent: %q", h.tt.Texts())
	}
	h.click("Back")
	if !h.tt.Focused("Clone a repository") {
		t.Fatal("Back to Source doesn't keep Clone chosen")
	}
	h.click("Back")
	if !h.tt.Focused("build-box") {
		t.Fatal("Back to Machine doesn't keep build-box chosen")
	}
	h.key(0, ui.KeyEnter)
	h.key(0, ui.KeyEnter)
	h.click("Next")
	if !showing(h, filepath.Join(root, "beacon")) {
		t.Fatalf("going on again lost the name: %q", h.tt.Texts())
	}
}

// Escape goes back a step, and closes the wizard from the first.
func TestEscapeGoesBackAndClosesTheWizard(t *testing.T) {
	h, _ := wizardMachines(t, inboxDir(t))
	newProject(h)
	h.click("build-box")
	h.click("Clone a repository")
	h.untilShown("Clone", "Clone a repository onto build-box")
	h.key(0, ui.KeyEscape)
	if !showing(h, "New project on build-box") {
		t.Fatalf("Escape on Clone didn't go to Source: %q", h.tt.Texts())
	}
	h.key(0, ui.KeyEscape)
	if !showing(h, "Which machine is it on?") {
		t.Fatalf("Escape on Source didn't go to Machine: %q", h.tt.Texts())
	}
	h.key(0, ui.KeyEscape)
	if h.a.wizard.open || showing(h, "Which machine is it on?") {
		t.Fatal("Escape on the first step didn't close the wizard")
	}
}

// The browser lists a server's folders through its engine: Enter opens
// one and Left goes up, a path typed goes there, hidden folders show
// when asked, and one that can't be read says why and leaves the listing
// as it was.
func TestTheWizardBrowsesAServersFolders(t *testing.T) {
	root := folderTree(t)
	h, _ := wizardMachines(t, inboxDir(t))
	browseTo(h, root)
	if !showing(h, "beta") || !showing(h, "locked") || showing(h, ".hidden") {
		t.Fatalf("the listing of %s is %q", root, h.tt.Texts())
	}
	h.click("Show hidden folders")
	if !showing(h, ".hidden") {
		t.Fatal("Show hidden folders shows no .hidden")
	}
	h.click("Show hidden folders")
	if showing(h, ".hidden") {
		t.Fatal(".hidden shows with hidden folders off")
	}
	h.key(0, ui.KeyDown)
	if !h.tt.Focused("alpha") {
		t.Fatal("Down from the hidden folders' box doesn't focus the first folder")
	}
	h.key(0, ui.KeyEnter)
	h.until("alpha listed", func() bool { return showing(h, "deep") })
	if h.a.wizard.browse.field != filepath.Join(root, "alpha") {
		t.Fatalf("the path field says %q in alpha", h.a.wizard.browse.field)
	}
	h.key(0, ui.KeyLeft)
	h.until("the root again", func() bool { return showing(h, "alpha") && !showing(h, "deep") })
	if h.a.wizard.browse.field != root {
		t.Fatalf("the path field says %q after Left", h.a.wizard.browse.field)
	}
	if os.Geteuid() == 0 {
		t.Skip("root reads a folder no one can")
	}
	h.click("locked")
	h.until("the failure", func() bool { return h.a.wizard.browse.failure != "" })
	h.tt.Frame()
	if !h.tt.HasText(h.a.wizard.browse.failure) || !strings.Contains(h.a.wizard.browse.failure, "permission denied") {
		t.Fatalf("the failure %q, the window %q", h.a.wizard.browse.failure, h.tt.Texts())
	}
	if !showing(h, "alpha") || h.a.wizard.browse.field != root {
		t.Fatalf("the failed listing lost the last one: %q", h.tt.Texts())
	}
}

// Nothing is a project until Create, which keeps the folder as one on
// the server it's on and starts a new thread there.
func TestCreateKeepsTheProjectOnItsServer(t *testing.T) {
	root := folderTree(t)
	dir := inboxDir(t)
	h, _ := wizardMachines(t, dir)
	browseTo(h, root)
	h.click("alpha")
	h.until("alpha listed", func() bool { return showing(h, "deep") })
	h.key(ui.Cmd, ui.KeyEnter)
	alpha := filepath.Join(root, "alpha")
	h.untilShown("Confirm", alpha)
	if ps := addedFile(t, dir); len(ps) != 0 {
		t.Fatalf("projects.json holds %+v before Create", ps)
	}
	h.click("Create")
	if ps := addedFile(t, dir); len(ps) != 1 || ps[0].Server != "build-box" || ps[0].Workspace != alpha {
		t.Fatalf("projects.json holds %+v, want alpha on build-box", ps)
	}
	box := h.a.serverEnv("build-box")
	h.until("the draft on build-box", func() bool {
		cur := h.a.current()
		return cur.id().IsZero() && cur.env == box && cur.View.Workspace == alpha
	})
	if h.a.wizard.open {
		t.Fatal("the wizard stays open")
	}
	h.untilShown("the project's chip", "alpha")
}

// A folder that's a project already says so, and Create shows it.
func TestCreateOfAFolderThatIsAProjectShowsIt(t *testing.T) {
	dir := pickFolder(t, "lighthouse")
	state := inboxDir(t)
	h := start(t, state)
	addAndLeave(h, dir)
	newProject(h)
	h.click("Existing folder")
	h.untilShown("Confirm", "lighthouse is already a project on this machine. Create shows it.")
	h.click("Create")
	h.until("the draft in it", func() bool { return h.a.current().View.Workspace == dir })
	if ps := addedFile(t, state); len(ps) != 1 {
		t.Fatalf("projects.json holds %+v", ps)
	}
}

// A clone on this machine goes into the folder chosen, is a project with
// a new thread in it, and the next clone goes into that folder too. A
// click outside leaves the wizard as it was.
func TestTheWizardClonesOnThisMachine(t *testing.T) {
	source := bareRepo(t)
	state := inboxDir(t)
	h := start(t, state)
	h.thread("SIP re-INVITE handling")
	parent := chooseParentGives(t)
	newProject(h)
	h.click("Clone a repository")
	if !h.tt.Focused("Repository") {
		t.Fatal("Clone's field doesn't have the focus")
	}
	h.tt.ClickAt(4, 4)
	h.tt.Frame()
	if !h.a.wizard.open {
		t.Fatal("a click outside closed the wizard")
	}
	h.replaceIn("Repository", source)
	h.click("Choose…")
	h.untilShown("the parent", "Into "+parent)
	if !h.tt.Focused("Repository") {
		t.Fatal("the field doesn't have the focus back after Choose…")
	}
	h.key(0, ui.KeyEnter)
	dest := filepath.Join(parent, "lighthouse")
	h.untilShown("Confirm", dest)
	if !h.tt.Focused("Name") {
		t.Fatal("Confirm's name doesn't have the focus")
	}
	h.key(0, ui.KeyEnter)
	h.until("the draft in the clone", func() bool {
		cur := h.a.current()
		return cur.id().IsZero() && cur.View.Workspace == dest && cur.local()
	})
	if b, err := os.ReadFile(filepath.Join(dest, "README.md")); err != nil || string(b) != "lighthouse\n" {
		t.Fatalf("the clone holds %q, %v", b, err)
	}
	h.untilShown("the project's chip", "lighthouse")
	if ps := addedFile(t, state); len(ps) != 1 || ps[0] != (addedProject{dest, "", h.a.localEnv().ID, ps[0].Added}) {
		t.Fatalf("projects.json holds %+v, want %s", ps, dest)
	}
	h.a.openWizard(sourceClone)
	h.tt.Frame()
	if !showing(h, "Into "+parent) {
		t.Fatalf("the next clone doesn't go into %s: %q", parent, h.tt.Texts())
	}
}

// chooseParentGives has Choose… give a new folder, Projects, for the
// clone to go into, made as the picker's New Folder makes it.
func chooseParentGives(t *testing.T) string {
	parent := filepath.Join(t.TempDir(), "Projects")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	was := chooseCloneParent
	chooseCloneParent = func(*app) string { return parent }
	t.Cleanup(func() { chooseCloneParent = was })
	return parent
}

// A clone that fails says why, with Retry, records nothing and leaves no
// folder; the choices stay, and Retry once the repository is there
// clones it.
func TestAFailedCloneOffersRetry(t *testing.T) {
	bareRepo(t)
	state := inboxDir(t)
	h := start(t, state)
	h.thread("SIP re-INVITE handling")
	parent := chooseParentGives(t)
	missing := filepath.Join(t.TempDir(), "missing.git")
	newProject(h)
	h.click("Clone a repository")
	h.replaceIn("Repository", "file://"+filepath.ToSlash(missing))
	h.click("Choose…")
	h.untilShown("the parent", "Into "+parent)
	h.click("Next")
	h.replaceIn("Name", "beacon")
	h.click("Clone and create")
	h.until("the clone to fail", func() bool { return h.a.wizard.failure != "" })
	h.tt.Frame()
	if !h.tt.HasText(h.a.wizard.failure) || !showing(h, "Retry") {
		t.Fatalf("no failure or Retry; the window shows %q", h.tt.Texts())
	}
	dest := filepath.Join(parent, "beacon")
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("the failed clone left its folder: %v", err)
	}
	if ps := addedFile(t, state); len(ps) != 0 {
		t.Fatalf("projects.json holds %+v after a failed clone", ps)
	}
	if !showing(h, dest) || h.a.wizard.name != "beacon" {
		t.Fatalf("the failure lost the name or the parent: %q", h.tt.Texts())
	}
	bareRepoAt(t, missing)
	h.click("Retry")
	h.until("the draft in the clone", func() bool { return h.a.current().View.Workspace == dest })
	if b, err := os.ReadFile(filepath.Join(dest, "README.md")); err != nil || string(b) != "lighthouse\n" {
		t.Fatalf("the clone holds %q, %v", b, err)
	}
}

// A clone on a server runs on its engine, into the parent chosen there,
// which the next clone there goes into.
func TestACloneOnAServerRunsThere(t *testing.T) {
	source := bareRepo(t)
	root := folderTree(t)
	state := inboxDir(t)
	h, box := wizardMachines(t, state)
	newProject(h)
	h.click("build-box")
	h.click("Clone a repository")
	h.replaceIn("Repository", source)
	h.click("Choose…")
	h.untilShown("the parent's browser", "Clone into a folder on build-box")
	h.replaceIn("Path", root)
	h.key(0, ui.KeyEnter)
	h.until("the folder listed", func() bool { return showing(h, "alpha") })
	h.click("Clone here")
	h.click("Next")
	dest := filepath.Join(root, "lighthouse")
	h.untilShown("Confirm", dest)
	h.click("Clone and create")
	env := h.a.serverEnv("build-box")
	h.until("the draft in the clone on build-box", func() bool {
		cur := h.a.current()
		return cur.env == env && cur.View.Workspace == dest
	})
	if n := box.clones.Load(); n != 1 {
		t.Fatalf("build-box's engine ran %d clones", n)
	}
	if ps := addedFile(t, state); len(ps) != 1 || ps[0].Server != "build-box" || ps[0].Workspace != dest {
		t.Fatalf("projects.json holds %+v", ps)
	}
	var f cloneFile
	if err := jsonfile.Load(filepath.Join(state, "clone.json"), &f); err != nil || f.Engines[env.ID.String()] != root || f.Parent != "" {
		t.Fatalf("clone.json holds %+v, %v", f, err)
	}
}

// Cancel stops a clone running on a server, and closes the wizard with
// nothing recorded.
func TestCancelStopsTheClone(t *testing.T) {
	state := inboxDir(t)
	h, box := wizardMachines(t, state)
	box.hold = true
	newProject(h)
	h.click("build-box")
	h.click("Clone a repository")
	h.replaceIn("Repository", "o/lighthouse")
	h.click("Next")
	h.click("Clone and create")
	h.untilShown("the clone's progress", "Receiving objects:  10% (1/10)")
	if !showing(h, "Cloning…") || !showing(h, "Back") {
		t.Fatalf("no progress; the window shows %q", h.tt.Texts())
	}
	h.click("Cancel")
	select {
	case <-box.stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("the clone on build-box wasn't stopped")
	}
	h.tt.Frame()
	if h.a.wizard.open {
		t.Fatal("the wizard stays open")
	}
	if ps := addedFile(t, state); len(ps) != 0 {
		t.Fatalf("projects.json holds %+v", ps)
	}
}

// A machine that went offline since it was chosen is said so at Create,
// which makes nothing and keeps the choices.
func TestCreateOnAMachineGoneOfflineSaysSo(t *testing.T) {
	root := folderTree(t)
	state := inboxDir(t)
	h, box := wizardMachines(t, state)
	browseTo(h, root)
	h.click("Use this folder")
	h.untilShown("Confirm", root)
	box.stop()
	h.until("build-box offline", func() bool { return !h.a.reachable(h.a.serverEnv("build-box")) })
	h.click("Create")
	if !showing(h, "build-box went offline") || !showing(h, root) {
		t.Fatalf("Create says nothing of build-box: %q", h.tt.Texts())
	}
	if ps := addedFile(t, state); len(ps) != 0 {
		t.Fatalf("projects.json holds %+v", ps)
	}
}

// projects.add and projects.clone open the wizard for a plugin, the
// latter past Source on Clone, and are refused while it waits for the
// user, as the palette is.
func TestPluginsOpenTheWizard(t *testing.T) {
	h := start(t, inboxDir(t))
	h.thread("SIP re-INVITE handling")
	if _, err := h.a.pluginEffect("project-strip", plugins.Effect{Type: "projects.clone"}); err != nil {
		t.Fatal(err)
	}
	h.tt.Frame()
	if !showing(h, "Clone a repository onto this machine") {
		t.Fatalf("projects.clone opened no Clone: %q", h.tt.Texts())
	}
	if _, err := h.a.pluginEffect("project-strip", plugins.Effect{Type: "projects.add"}); err == nil {
		t.Fatal("projects.add went through with the wizard open")
	}
	if slices.ContainsFunc(h.a.actions(), func(c command) bool { return c.id == "project.clone" || c.id == "project.add" }) {
		t.Fatal("the palette offers a second wizard")
	}
	h.click("Cancel")
	if _, err := h.a.pluginEffect("project-strip", plugins.Effect{Type: "projects.add", Args: json.RawMessage("{}")}); err != nil {
		t.Fatal(err)
	}
	h.tt.Frame()
	if !showing(h, "New project on this machine") {
		t.Fatalf("projects.add opened no wizard: %q", h.tt.Texts())
	}
}

// The palette's Clone repository… opens the wizard on Clone, whose Back
// is Source, and Escape from there closes it.
func TestThePaletteOpensTheWizardOnClone(t *testing.T) {
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
	if !showing(h, "Clone a repository onto this machine") {
		t.Fatalf("no Clone; the window shows %q", h.tt.Texts())
	}
	h.key(0, ui.KeyEscape)
	if !h.tt.Focused("Clone a repository") {
		t.Fatalf("Escape on Clone didn't go to Source with Clone chosen: %q", h.tt.Texts())
	}
	h.key(0, ui.KeyEscape)
	if h.a.wizard.open {
		t.Fatal("Escape on Source left the wizard open")
	}
}

// Removing a server's project forgets it there only: this machine's
// project of the same path stays.
func TestRemovingAServersProjectKeepsThisMachines(t *testing.T) {
	state := inboxDir(t)
	ws := t.TempDir()
	h, _ := wizardMachines(t, state)
	box := h.a.serverEnv("build-box")
	h.a.editProjects(func(ps *[]addedProject) {
		*ps = []addedProject{{ws, "", api.ID{}, "2026-11-23T10:00:00Z"}, {ws, "build-box", box.ID, "2026-11-23T10:00:00Z"}}
	})
	var onBox *project
	h.until("both projects", func() bool {
		h.tt.Frame()
		n := 0
		for _, p := range h.a.projects {
			if p.key.workspace == ws {
				n++
				if p.env == box {
					onBox = p
				}
			}
		}
		return n == 2 && onBox != nil
	})
	h.a.askRemove(onBox)
	h.tt.Frame()
	h.click("Remove")
	if ps := addedFile(t, state); len(ps) != 1 || ps[0].Server != "" || ps[0].Workspace != ws {
		t.Fatalf("projects.json holds %+v, want this machine's only", ps)
	}
	h.tt.Frame()
	if slices.ContainsFunc(h.a.projects, func(p *project) bool { return p.env == box }) ||
		!slices.ContainsFunc(h.a.projects, func(p *project) bool { return p.key == projectKey{"", ws} }) {
		t.Fatal("the projects shown aren't this machine's alone")
	}
}

// heldListing are a server's Projects whose listing of path, once begun,
// waits for release.
type heldListing struct {
	transport.Projects
	path           string
	began, release chan struct{}
}

func (p heldListing) Folders(ctx context.Context, q api.FolderQuery) (api.FolderList, error) {
	if q.Path == p.path {
		close(p.began)
		select {
		case <-p.release:
		case <-ctx.Done():
			return api.FolderList{}, ctx.Err()
		}
	}
	return p.Projects.Folders(ctx, q)
}

// While a folder is being listed none can be chosen, not even the one
// shown before it: the path field names it and the list says it's
// listing. One that fails to list leaves the folder before it to choose.
func TestAFolderBeingListedCantBeChosen(t *testing.T) {
	root := folderTree(t)
	h, box := wizardMachines(t, inboxDir(t))
	alpha := filepath.Join(root, "alpha")
	began, release := make(chan struct{}), make(chan struct{})
	box.Projects = heldListing{box.Projects, alpha, began, release}
	browseTo(h, root)
	h.replaceIn("Path", alpha)
	h.key(0, ui.KeyEnter)
	select {
	case <-began:
	case <-time.After(10 * time.Second):
		t.Fatal("the listing of alpha didn't reach build-box")
	}
	h.tt.Frame()
	if !showing(h, "Listing…") || showing(h, "beta") || h.a.wizard.browse.field != alpha {
		t.Fatalf("while alpha lists, the path says %q and the window %q", h.a.wizard.browse.field, h.tt.Texts())
	}
	h.key(ui.Cmd, ui.KeyEnter)
	h.tt.Click("Use this folder")
	h.tt.Frame()
	if showing(h, "Create the project") {
		t.Fatalf("a folder was chosen while alpha listed: %q", h.a.wizard.folder)
	}
	close(release)
	h.until("alpha listed", func() bool { return showing(h, "deep") })
	h.key(ui.Cmd, ui.KeyEnter)
	h.untilShown("Confirm", "Create the project")
	if h.a.wizard.folder != alpha {
		t.Fatalf("Confirm is for %q, want %q", h.a.wizard.folder, alpha)
	}
	h.click("Back")
	h.replaceIn("Path", filepath.Join(root, "missing"))
	h.key(0, ui.KeyEnter)
	h.until("the failure", func() bool { return h.a.wizard.browse.failure != "" })
	h.key(ui.Cmd, ui.KeyEnter)
	h.untilShown("Confirm", "Create the project")
	if h.a.wizard.folder != alpha {
		t.Fatalf("after the failed listing Confirm is for %q, want %q", h.a.wizard.folder, alpha)
	}
}

// Escape while a clone runs stops it, and once it has stopped goes back
// to Clone with the choices kept. Until then it says it's stopping, and
// no other clone starts.
func TestEscapeDuringACloneStopsItThenGoesBack(t *testing.T) {
	h, box := wizardMachines(t, inboxDir(t))
	box.hold = true
	newProject(h)
	h.click("build-box")
	h.click("Clone a repository")
	h.replaceIn("Repository", "o/lighthouse")
	h.click("Next")
	h.click("Clone and create")
	h.untilShown("the clone's progress", "Receiving objects:  10% (1/10)")
	// The clone's end is posted to the window; until it runs, the clone
	// is stopping.
	h.tt.Key(0, ui.KeyEscape)
	h.tt.Frame()
	if !showing(h, "Stopping the clone…") || !showing(h, "Create the project") {
		t.Fatalf("while the clone stops the window shows %q", h.tt.Texts())
	}
	h.tt.Click("Clone and create")
	h.tt.Frame()
	select {
	case <-box.stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("Escape didn't stop the clone")
	}
	h.untilShown("Clone again", "Clone a repository onto build-box")
	if h.a.wizard.input != "o/lighthouse" || h.a.wizard.failure != "" {
		t.Fatalf("back on Clone the field holds %q, and the failure is %q", h.a.wizard.input, h.a.wizard.failure)
	}
	if n := box.clones.Load(); n != 1 {
		t.Fatalf("%d clones started", n)
	}
}

// A server's project is on the engine it was made on: another engine
// paired under the same label doesn't show it, and it stays saved.
func TestAServersProjectStaysOnItsEngine(t *testing.T) {
	root := folderTree(t)
	state := inboxDir(t)
	h, _ := wizardMachines(t, state)
	browseTo(h, root)
	h.click("Use this folder")
	h.click("Create")
	old := h.a.serverEnv("build-box").ID
	if ps := addedFile(t, state); len(ps) != 1 || ps[0] != (addedProject{root, "build-box", old, ps[0].Added}) {
		t.Fatalf("projects.json holds %+v", ps)
	}
	if err := h.a.backend.RemoveServer("build-box"); err != nil {
		t.Fatal(err)
	}
	_, addr := serverNode(t)
	var added error = errors.New("not yet")
	h.a.backend.AddServer(client.Server{Label: "build-box", Addr: addr, Token: "any"}, func(err error) { added = err })
	h.until("the other engine online", func() bool {
		e := h.a.serverEnv("build-box")
		return added == nil && e != nil && h.a.reachable(e)
	})
	other := h.a.serverEnv("build-box")
	if other.ID == old {
		t.Fatal("the other engine is the same engine")
	}
	h.tt.Frame()
	if slices.ContainsFunc(h.a.projects, func(p *project) bool { return p.env == other && p.key.workspace == root }) {
		t.Fatalf("the project made on %s shows on %s", old, other.ID)
	}
	if ps := addedFile(t, state); len(ps) != 1 || ps[0].Engine != old {
		t.Fatalf("projects.json holds %+v", ps)
	}
}

// A project in an existing folder starts with the draft's worktree
// choice, as Add project did; a clone's starts fresh, with Settings'.
func TestOnlyAnExistingFolderKeepsTheDraftsWorktree(t *testing.T) {
	dir := t.TempDir()
	was := chooseFolder
	chooseFolder = func(*app) string { return dir }
	t.Cleanup(func() { chooseFolder = was })
	source := bareRepo(t)
	parent := chooseParentGives(t)
	h := start(t, inboxDir(t))
	h.a.worktree.picked, h.a.worktree.on = true, true
	addFolder(h)
	h.until("the draft in the folder", func() bool { return h.a.current().View.Workspace == dir })
	if !h.a.worktree.picked || !h.a.worktree.on {
		t.Fatal("the existing folder's draft lost the worktree choice")
	}
	newProject(h)
	h.click("Clone a repository")
	h.replaceIn("Repository", source)
	h.click("Choose…")
	h.untilShown("the parent", "Into "+parent)
	h.click("Next")
	h.click("Clone and create")
	dest := filepath.Join(parent, "lighthouse")
	h.until("the draft in the clone", func() bool { return h.a.current().View.Workspace == dest })
	if h.a.worktree.picked {
		t.Fatal("the clone's draft kept the worktree choice of the draft before it")
	}
}

// Where the last clone went and the last project started in on a server
// are kept by its engine: another engine paired under the same label gets
// neither, though the same folder is a project there.
func TestAReusedLabelGetsNoneOfTheOldEnginesPlaces(t *testing.T) {
	root := folderTree(t)
	h, _ := wizardMachines(t, inboxDir(t))
	old := h.a.serverEnv("build-box")
	h.a.recordProject(old, root)
	h.a.rememberParent(old, root)
	h.a.rememberProject(old, root)
	h.tt.Frame()
	if got := h.a.cloneParent(old); got != root {
		t.Fatalf("build-box's clone parent is %q, want %q", got, root)
	}
	if d, ok := h.a.lastDraft(old); !ok || d.workspace != root {
		t.Fatalf("build-box's last draft is %+v, %v", d, ok)
	}
	if err := h.a.backend.RemoveServer("build-box"); err != nil {
		t.Fatal(err)
	}
	_, addr := serverNode(t)
	var added error = errors.New("not yet")
	h.a.backend.AddServer(client.Server{Label: "build-box", Addr: addr, Token: "any"}, func(err error) { added = err })
	h.until("the other engine online", func() bool {
		e := h.a.serverEnv("build-box")
		return added == nil && e != nil && h.a.reachable(e)
	})
	other := h.a.serverEnv("build-box")
	h.a.recordProject(other, root)
	h.tt.Frame()
	if got := h.a.cloneParent(other); got != "~/Projects" {
		t.Fatalf("the other engine's clone parent is %q, want ~/Projects", got)
	}
	if d, ok := h.a.lastDraft(other); ok {
		t.Fatalf("the other engine's last draft is the old engine's: %+v", d)
	}
	h.a.rememberProject(other, root)
	if _, ok := h.a.lastDraft(other); !ok {
		t.Fatal("the other engine's own last project isn't its last draft")
	}
}

// clone.json and last-project.json written by label still read: this
// machine's entries as they were, a server's label as no server.
func TestPlacesKeptByLabelAreNoServers(t *testing.T) {
	root := folderTree(t)
	here, parent := t.TempDir(), t.TempDir()
	state := inboxDir(t)
	if err := os.WriteFile(filepath.Join(state, "clone.json"), []byte(`{"parent": "`+parent+`", "servers": {"build-box": "`+root+`"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := jsonfile.Write(filepath.Join(state, "last-project.json"), map[string]string{"": here, "build-box": root}); err != nil {
		t.Fatal(err)
	}
	h, _ := wizardMachines(t, state)
	box, local := h.a.serverEnv("build-box"), h.a.localEnv()
	h.a.recordProject(box, root)
	h.a.recordProject(local, here)
	h.tt.Frame()
	if got := h.a.cloneParent(box); got != "~/Projects" {
		t.Fatalf("build-box's clone parent is %q, want ~/Projects", got)
	}
	if got := h.a.cloneParent(local); got != parent {
		t.Fatalf("this machine's clone parent is %q, want %q", got, parent)
	}
	if d, ok := h.a.lastDraft(box); ok {
		t.Fatalf("build-box's last draft is the label's: %+v", d)
	}
	if d, ok := h.a.lastDraft(local); !ok || d.workspace != here {
		t.Fatalf("this machine's last draft is %+v, %v; want %s", d, ok, here)
	}
}

// A folder picked on this machine is the engine's to allow: its state
// folder, picked as a project or as where a clone goes, is refused with
// the engine's words, the wizard stays where it was and nothing is kept.
func TestThePickerCantChooseTheEnginesStateFolder(t *testing.T) {
	state := inboxDir(t)
	was, wasParent := chooseFolder, chooseCloneParent
	chooseFolder = func(*app) string { return state }
	chooseCloneParent = func(*app) string { return state }
	t.Cleanup(func() { chooseFolder, chooseCloneParent = was, wasParent })
	h := start(t, state)
	newProject(h)
	h.click("Existing folder")
	h.until("the refusal", func() bool { return h.a.wizard.failure != "" })
	h.tt.Frame()
	if f := h.a.wizard.failure; !strings.Contains(f, "engine's state") || !h.tt.HasText(f) || !showing(h, "Clone a repository") {
		t.Fatalf("picking the state folder failed with %q, and the window shows %q", f, h.tt.Texts())
	}
	if h.a.wizard.folder != "" {
		t.Fatalf("the wizard took %q", h.a.wizard.folder)
	}
	h.click("Clone a repository")
	parent := h.a.wizard.parent
	h.click("Choose…")
	h.until("the refusal", func() bool { return h.a.wizard.failure != "" })
	h.tt.Frame()
	if f := h.a.wizard.failure; !strings.Contains(f, "engine's state") || !h.tt.HasText(f) || !showing(h, "Into "+parent) {
		t.Fatalf("choosing the state folder to clone into failed with %q, and the window shows %q", f, h.tt.Texts())
	}
	if ps := addedFile(t, state); len(ps) != 0 {
		t.Fatalf("projects.json holds %+v", ps)
	}
	if _, err := os.Stat(filepath.Join(state, "clone.json")); !os.IsNotExist(err) {
		t.Fatalf("clone.json is there: %v", err)
	}
}

// The Clone step lists the repositories the machine's gh reaches,
// filtered by what the field holds; Enter picks the first, which
// Confirm names the clone's folder after.
func TestTheCloneStepListsTheMachinesRepositories(t *testing.T) {
	stubRepos(t,
		api.Repo{Name: "acme/lighthouse", Description: "A beacon for ships"},
		api.Repo{Name: "acme/harbor", Private: true},
		api.Repo{Name: "me/notes"},
	)
	h := start(t, inboxDir(t))
	h.thread("SIP re-INVITE handling")
	newProject(h)
	h.click("Clone a repository")
	h.untilShown("the repositories", "acme/lighthouse")
	if !showing(h, "A beacon for ships") || !showing(h, "Private") || !showing(h, "me/notes") {
		t.Fatalf("the repositories don't show fully: %q", h.tt.Texts())
	}
	h.replaceIn("Repository", "HARB")
	if !showing(h, "acme/harbor") || showing(h, "acme/lighthouse") || showing(h, "me/notes") {
		t.Fatalf("\"HARB\" filters to %q", h.tt.Texts())
	}
	h.key(0, ui.KeyEnter)
	h.untilShown("Confirm", "Create the project")
	if h.a.wizard.input != "acme/harbor" || h.a.wizard.name != "harbor" {
		t.Fatalf("Enter chose %q named %q, want acme/harbor", h.a.wizard.input, h.a.wizard.name)
	}
}

// Down from the field moves into the list, and Enter on a row chooses
// it, as a click does.
func TestTheArrowsChooseARepository(t *testing.T) {
	stubRepos(t,
		api.Repo{Name: "acme/lighthouse"},
		api.Repo{Name: "acme/harbor"},
	)
	h := start(t, inboxDir(t))
	h.thread("SIP re-INVITE handling")
	newProject(h)
	h.click("Clone a repository")
	h.untilShown("the repositories", "acme/harbor")
	if !h.tt.Focused("Repository") {
		t.Fatal("the field doesn't have the focus")
	}
	h.key(0, ui.KeyDown)
	if !h.tt.Focused("acme/lighthouse") {
		t.Fatalf("Down focused %q, want the first repository", h.tt.Texts())
	}
	h.key(0, ui.KeyDown)
	if !h.tt.Focused("acme/harbor") {
		t.Fatal("Down didn't focus the second repository")
	}
	h.key(0, ui.KeyEnter)
	h.untilShown("Confirm", "Create the project")
	if h.a.wizard.input != "acme/harbor" {
		t.Fatalf("Enter chose %q, want acme/harbor", h.a.wizard.input)
	}
}

// A URL no repository matches still goes on through the field, as it
// always has, the list saying so.
func TestAURLNoRepositoryMatchesStillGoesOn(t *testing.T) {
	source := bareRepo(t)
	stubRepos(t, api.Repo{Name: "acme/lighthouse"})
	h := start(t, inboxDir(t))
	h.thread("SIP re-INVITE handling")
	newProject(h)
	h.click("Clone a repository")
	h.untilShown("the repositories", "acme/lighthouse")
	h.replaceIn("Repository", source)
	h.untilShown("no match", "No repositories match")
	h.key(0, ui.KeyEnter)
	h.untilShown("Confirm", "Create the project")
	if h.a.wizard.name != "lighthouse" {
		t.Fatalf("the URL's clone is named %q", h.a.wizard.name)
	}
}

// When the engine can't list the repositories, the list says why,
// muted, and the field still takes a URL or owner/name.
func TestAListingFailureStillLetsTheFieldWork(t *testing.T) {
	stubGH(t, "echo 'gh: stub' >&2\nexit 1")
	h := start(t, inboxDir(t))
	h.thread("SIP re-INVITE handling")
	newProject(h)
	h.click("Clone a repository")
	h.untilShown("the reason", "gh isn't signed in to github.com on this machine: run gh auth login there")
	h.replaceIn("Repository", "o/lighthouse")
	h.click("Next")
	h.untilShown("Confirm", "Create the project")
	if h.a.wizard.input != "o/lighthouse" {
		t.Fatalf("the field lost %q", h.a.wizard.input)
	}
}

// A server's repositories are its engine's: build-box's gh lists them.
func TestACloneOnAServerListsItsRepositories(t *testing.T) {
	stubRepos(t,
		api.Repo{Name: "ops/lighthouse", Description: "Status page"},
		api.Repo{Name: "ops/harbor"},
	)
	h, _ := wizardMachines(t, inboxDir(t))
	newProject(h)
	h.click("build-box")
	h.click("Clone a repository")
	h.untilShown("build-box's repositories", "ops/lighthouse")
	h.click("ops/harbor")
	h.untilShown("Confirm", "Create the project")
	if h.a.wizard.input != "ops/harbor" {
		t.Fatalf("clicking a repository chose %q", h.a.wizard.input)
	}
}

// filterRepos puts the likeliest repository for a query first: an exact
// name or repository part, then a name starting with it, then a name
// holding every word, then a match the description helped, each rank
// keeping the listing's order.
func TestFilterReposRanks(t *testing.T) {
	repos := []api.Repo{
		{Name: "o/alight"},
		{Name: "acme/lighthouse", Description: "Status page"},
		{Name: "zzz/light"},
		{Name: "acme/harbor", Description: "A lighthouse for deploys"},
		{Name: "me/notes"},
	}
	for _, tc := range []struct {
		query string
		want  []string
	}{
		{"", []string{"o/alight", "acme/lighthouse", "zzz/light", "acme/harbor", "me/notes"}},
		{"light", []string{"zzz/light", "acme/lighthouse", "o/alight", "acme/harbor"}},
		{"LIGHT", []string{"zzz/light", "acme/lighthouse", "o/alight", "acme/harbor"}},
		{"acme light", []string{"acme/lighthouse", "acme/harbor"}},
		{"acme/harbor", []string{"acme/harbor"}},
		{"nowhere", nil},
	} {
		var got []string
		for _, r := range filterRepos(repos, tc.query) {
			got = append(got, r.Name)
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("filterRepos(%q) = %v, want %v", tc.query, got, tc.want)
		}
	}
}

// A listing comes page by page: the first hundred repositories show,
// searchable and pickable, while the rest still come, under "Checking
// for new repositories…".
func TestTheRepositoriesComeInPages(t *testing.T) {
	sig := t.TempDir()
	release := filepath.Join(sig, "release")
	stubGH(t, "[ \"$1\" = auth ] && exit 0\n"+
		"i=0; while [ $i -lt 100 ]; do echo '{\"name\":\"o/r'$i'\"}'; i=$((i+1)); done\n"+
		"while [ ! -e '"+release+"' ]; do /bin/sleep 0.02; done\n"+
		"i=100; while [ $i -lt 150 ]; do echo '{\"name\":\"o/r'$i'\"}'; i=$((i+1)); done")
	h := start(t, inboxDir(t))
	h.a.openWizard(sourceClone)
	h.tt.Frame()
	h.until("the first page", func() bool {
		c := h.a.reposOf(h.a.localEnv())
		return c.fetch != nil && len(c.fetch.repos) == 100
	})
	if !showing(h, "o/r0") || !showing(h, "Checking for new repositories…") {
		t.Fatalf("a listing still coming shows no rows or says nothing: %q", h.tt.Texts())
	}
	h.replaceIn("Repository", "o/r4")
	h.key(0, ui.KeyEnter)
	h.untilShown("Confirm", "Create the project")
	if h.a.wizard.input != "o/r4" {
		t.Fatalf("Enter picked %q, want o/r4", h.a.wizard.input)
	}
	h.click("Back")
	h.replaceIn("Repository", "")
	if !showing(h, "Checking for new repositories…") {
		t.Fatal("Back hides the listing still coming")
	}
	if err := os.WriteFile(release, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	h.until("the rest", func() bool {
		c := h.a.reposOf(h.a.localEnv())
		return c.fetch == nil && len(c.repos) == 150
	})
	h.tt.Frame()
	if showing(h, "Checking for new repositories…") {
		t.Fatal("a finished listing still says it's coming")
	}
}

// A machine's repositories are cached: a stale cache shows at once
// while a fresh listing runs, whose new repositories go on top; a fresh
// cache lists nothing again.
func TestTheRepositoriesAreCachedPerMachine(t *testing.T) {
	sig := t.TempDir()
	count := filepath.Join(sig, "api.calls")
	release := filepath.Join(sig, "release")
	stubGH(t, "[ \"$1\" = auth ] && exit 0\necho api >> '"+count+"'\n"+
		"echo '{\"name\":\"acme/lighthouse\"}'\n"+
		"echo '{\"name\":\"me/notes\"}'")
	h := start(t, inboxDir(t))
	h.a.openWizard(sourceClone)
	h.tt.Frame()
	h.untilShown("the repositories", "acme/lighthouse")
	h.a.closeWizard()
	h.tt.Frame()
	// Stale: the next showing lists again, and a repository pushed
	// meanwhile goes on top once it does.
	stubGH(t, "[ \"$1\" = auth ] && exit 0\necho api >> '"+count+"'\n"+
		"while [ ! -e '"+release+"' ]; do /bin/sleep 0.02; done\n"+
		"echo '{\"name\":\"acme/new\"}'\n"+
		"echo '{\"name\":\"acme/lighthouse\"}'\n"+
		"echo '{\"name\":\"me/notes\"}'")
	h.tt.Advance(31 * time.Second)
	h.tt.Frame()
	h.a.openWizard(sourceClone)
	h.tt.Frame()
	if !showing(h, "acme/lighthouse") || !showing(h, "me/notes") {
		t.Fatalf("the cache doesn't show while the refresh runs: %q", h.tt.Texts())
	}
	if !showing(h, "Checking for new repositories…") {
		t.Fatal("the refresh runs silently")
	}
	if err := os.WriteFile(release, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	h.until("the new repository on top", func() bool {
		c := h.a.reposOf(h.a.localEnv())
		return c.fetch == nil && len(c.repos) == 3 && c.repos[0].Name == "acme/new"
	})
	h.a.closeWizard()
	h.tt.Frame()
	// Fresh: showing it again lists nothing.
	h.a.openWizard(sourceClone)
	h.tt.Frame()
	h.untilShown("the repositories", "acme/new")
	calls, err := os.ReadFile(count)
	if err != nil || len(strings.Fields(string(calls))) != 2 {
		t.Fatalf("gh api ran %q, want twice", calls)
	}
}

// A wizard shown again while a listing still runs joins it rather than
// listing again.
func TestAReopenJoinsTheRunningListing(t *testing.T) {
	sig := t.TempDir()
	count := filepath.Join(sig, "api.calls")
	release := filepath.Join(sig, "release")
	stubGH(t, "[ \"$1\" = auth ] && exit 0\necho api >> '"+count+"'\n"+
		"while [ ! -e '"+release+"' ]; do /bin/sleep 0.02; done\n"+
		"echo '{\"name\":\"acme/lighthouse\"}'")
	h := start(t, inboxDir(t))
	h.a.openWizard(sourceClone)
	h.tt.Frame()
	h.untilShown("the listing", "Loading your repositories…")
	h.a.closeWizard()
	h.tt.Frame()
	h.a.openWizard(sourceClone)
	h.tt.Frame()
	if err := os.WriteFile(release, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	h.untilShown("the repositories", "acme/lighthouse")
	calls, err := os.ReadFile(count)
	if err != nil || len(strings.Fields(string(calls))) != 1 {
		t.Fatalf("gh api ran %q, want once", calls)
	}
}

// A listing that fails with nothing cached says why, muted, with a
// Retry that lists once gh works.
func TestAFailedListingRetries(t *testing.T) {
	stubGH(t, "[ \"$1\" = auth ] && exit 0\necho 'list broke' >&2\nexit 4")
	h := start(t, inboxDir(t))
	h.a.openWizard(sourceClone)
	h.tt.Frame()
	h.untilShown("the failure", "list broke")
	if !showing(h, "Retry") {
		t.Fatal("a failed listing offers no Retry")
	}
	stubRepos(t, api.Repo{Name: "acme/lighthouse"})
	h.click("Retry")
	h.untilShown("the repositories", "acme/lighthouse")
}

// A refresh that fails keeps the cached repositories showing, the
// failure and Retry under them.
func TestAFailedRefreshKeepsTheCache(t *testing.T) {
	stubRepos(t, api.Repo{Name: "acme/lighthouse"})
	h := start(t, inboxDir(t))
	h.a.openWizard(sourceClone)
	h.tt.Frame()
	h.untilShown("the repositories", "acme/lighthouse")
	h.a.closeWizard()
	h.tt.Frame()
	stubGH(t, "[ \"$1\" = auth ] && exit 0\necho 'list broke' >&2\nexit 4")
	h.tt.Advance(31 * time.Second)
	h.tt.Frame()
	h.a.openWizard(sourceClone)
	h.tt.Frame()
	h.untilShown("the failure", "list broke")
	if !showing(h, "acme/lighthouse") || !showing(h, "Retry") {
		t.Fatalf("the cache or Retry is gone: %q", h.tt.Texts())
	}
}

// A repository gone from a fresh complete listing leaves the cache when
// the refresh completes.
func TestARefreshDropsDeletedRepositories(t *testing.T) {
	stubRepos(t,
		api.Repo{Name: "acme/lighthouse"},
		api.Repo{Name: "acme/harbor"},
	)
	h := start(t, inboxDir(t))
	h.a.openWizard(sourceClone)
	h.tt.Frame()
	h.untilShown("the repositories", "acme/harbor")
	h.a.closeWizard()
	h.tt.Frame()
	stubRepos(t, api.Repo{Name: "acme/lighthouse"})
	h.tt.Advance(31 * time.Second)
	h.tt.Frame()
	h.a.openWizard(sourceClone)
	h.tt.Frame()
	h.until("the refresh done", func() bool {
		c := h.a.reposOf(h.a.localEnv())
		return c.fetch == nil && len(c.repos) == 1
	})
	h.tt.Frame()
	if showing(h, "acme/harbor") {
		t.Fatal("a deleted repository still shows")
	}
}
