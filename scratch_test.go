package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/daemon"
	"glitched/internal/provider/fake"
)

// sendInDraft sends text from the draft, and is the thread it starts.
func sendInDraft(h *harness, text string) thread {
	h.t.Helper()
	h.a.draft = text
	h.tt.Advance(time.Second)
	h.click("Send")
	return h.thread(text)
}

// scratchFolders are the folders made under the state's scratch root.
func scratchFolders(t *testing.T, state string) []string {
	t.Helper()
	es, err := os.ReadDir(filepath.Join(state, "scratch"))
	if err != nil {
		t.Fatalf("no scratch folder: %v", err)
	}
	var dirs []string
	for _, e := range es {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	return dirs
}

// On a first run the draft has no project, and sending it makes the
// thread a folder of its own under the scratch root, named for the day and
// the message, where the thread runs.
func TestAThreadWithNoProjectGetsAFolderOfItsOwn(t *testing.T) {
	state := tempDir(t)
	h := start(t, state)
	h.untilShown("the composer's workspace", "No project")
	th := sendInDraft(h, "Fix the flicker, in the sidebar's rows please")
	dirs := scratchFolders(t, state)
	name := regexp.MustCompile("^\\d{4}-\\d{2}-\\d{2}-fix-the-flicker-in-the-[0-9a-f]{8}$")
	if len(dirs) != 1 || !name.MatchString(dirs[0]) {
		t.Fatalf("the scratch root holds %q, want one folder named for the day and the message", dirs)
	}
	if want := filepath.Join(state, "scratch", dirs[0]); th.View.Workspace != want {
		t.Fatalf("the thread runs in %q, want %q", th.View.Workspace, want)
	}
}

// Each thread with no project gets its own folder, and they show under
// the one No project chip.
func TestThreadsWithNoProjectShareOneChip(t *testing.T) {
	state := tempDir(t)
	h := start(t, state)
	sendInDraft(h, "First thing")
	h.click("New thread")
	sendInDraft(h, "Second thing")
	if dirs := scratchFolders(t, state); len(dirs) != 2 {
		t.Fatalf("the scratch root holds %q, want a folder for each thread", dirs)
	}
	h.untilShown("the No project chip", "No project")
	var names []string
	for _, r := range h.a.projectRecords() {
		names = append(names, r.Name)
	}
	if !slices.Equal(names, []string{"No project"}) {
		t.Fatalf("the strip shows %q, want the one No project chip", names)
	}
	if n := newThreadsIn(t, h, "No project"); n != 0 {
		t.Fatalf("the palette has %d New thread in No project, want New thread without a project only", n)
	}
}

// The workspace menu offers No project first, and no home folder that no
// thread runs in; choosing a project and then No project moves the draft
// back.
func TestTheWorkspaceMenuOffersNoProjectFirst(t *testing.T) {
	state := inboxDir(t)
	h := start(t, state)
	h.thread("Markdown table column alignment")
	h.tt.Advance(time.Second)
	h.click("Workspace")
	menu := h.tt.Menu()
	if len(menu) == 0 || menu[0] != "No project" || slices.Contains(menu, filepath.Base(homeDir())) {
		t.Fatalf("the menu has %q, want No project first and no home folder", menu)
	}
	if err := h.tt.ChooseMenuItem("mygo"); err != nil {
		t.Fatalf("%v; the menu has %q", err, h.tt.Menu())
	}
	h.tt.Frame()
	if ws := h.a.current().View.Workspace; ws != "/work/mygo" {
		t.Fatalf("choosing mygo left the draft in %q", ws)
	}
	h.click("Workspace")
	if err := h.tt.ChooseMenuItem("No project"); err != nil {
		t.Fatalf("%v; the menu has %q", err, h.tt.Menu())
	}
	h.tt.Frame()
	if ws := h.a.current().View.Workspace; ws != filepath.Join(state, "scratch") {
		t.Fatalf("choosing No project left the draft in %q", ws)
	}
}

// The palette's New thread without a project starts a draft in No
// project, from a thread in another.
func TestThePaletteStartsAThreadWithoutAProject(t *testing.T) {
	state := inboxDir(t)
	h := start(t, state)
	h.a.select_(h.thread("Markdown table column alignment"))
	h.tt.Advance(time.Second)
	h.a.openCommandPalette()
	h.tt.Frame()
	h.tt.Type("new thread without a project")
	h.tt.Key(0, ui.KeyEnter)
	settlePlugins(t, h.a, h.tt, h.p)
	if cur := h.a.current(); !cur.id().IsZero() || cur.View.Workspace != filepath.Join(state, "scratch") {
		t.Fatalf("the chat shows %v in %q, want a draft with no project", cur.id(), cur.View.Workspace)
	}
}

// Deleting a thread with no project, or removing them all, leaves their
// folders, and what the agent made in them.
func TestRemovingThreadsWithNoProjectLeavesTheirFolders(t *testing.T) {
	state := tempDir(t)
	h := startWith(t, state, func(c *daemon.Config) { c.Providers = daemon.FakeProviders(fake.New(fake.Turn(false))) })
	first := sendInDraft(h, "First thing")
	h.click("New thread")
	second := sendInDraft(h, "Second thing")
	h.until("the turns to end", func() bool {
		a, _ := h.a.find(first.id())
		b, _ := h.a.find(second.id())
		return a.View.LastTurn != nil && !a.View.Live() && b.View.LastTurn != nil && !b.View.Live()
	})
	h.tt.Advance(time.Second)
	if err := h.tt.RightClick("First thing"); err != nil {
		t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem("Delete…"); err != nil {
		t.Fatalf("%v; the menu has %q", err, h.tt.Menu())
	}
	h.tt.Advance(time.Second)
	if h.tt.HasText("Delete “First thing”?") {
		h.click("Delete")
	}
	h.until("the thread deleted", func() bool { _, ok := h.a.find(first.id()); return !ok })
	if _, err := os.Stat(first.View.Workspace); err != nil {
		t.Fatalf("the deleted thread's folder went: %v", err)
	}
	h.a.openCommandPalette()
	h.tt.Frame()
	h.tt.Type("remove threads without a project")
	h.tt.Key(0, ui.KeyEnter)
	settlePlugins(t, h.a, h.tt, h.p)
	h.click("Remove")
	h.until("the threads removed", func() bool { _, ok := h.a.find(second.id()); return !ok })
	if dirs := scratchFolders(t, state); len(dirs) != 2 {
		t.Fatalf("the scratch root holds %q after the threads went, want both folders", dirs)
	}
}

// Quick ask offers No project first, before any thread has none, and
// starting there makes the thread its folder.
func TestQuickAskOffersNoProjectFirst(t *testing.T) {
	state := tempDir(t)
	inbox(t, state)
	h := start(t, state)
	h.thread("SIP re-INVITE handling")
	ps := h.a.localProjects()
	if len(ps) == 0 || ps[0].name() != "No project" {
		t.Fatalf("quick ask offers %v first, want No project", ps)
	}
	h.a.quick.project = 0
	h.a.quick.query = "Rename the photos by date"
	h.a.startQuickThread()
	th := h.thread("Rename the photos by date")
	if filepath.Dir(th.View.Workspace) != filepath.Join(state, "scratch") {
		t.Fatalf("quick ask started in %q, want a folder of its own under the scratch root", th.View.Workspace)
	}
}

// Until it's sent, a draft with no project has no folder: its terminal
// opens in the home folder, not the scratch root, and plugins are told no
// workspace.
func TestADraftWithNoProjectShowsNoFolder(t *testing.T) {
	state := tempDir(t)
	h := start(t, state)
	h.untilShown("the composer's workspace", "No project")
	d := h.a.current()
	if d.View.Workspace != filepath.Join(state, "scratch") {
		t.Fatalf("the draft is in %q, want the scratch root", d.View.Workspace)
	}
	if ws := h.a.threadRecord(d).Workspace; ws != "" {
		t.Fatalf("plugins see the draft's workspace as %q, want none", ws)
	}
	h.a.showTerminal = true
	h.tt.Advance(time.Second)
	if _, ok := h.a.shells[d.View.Workspace]; ok {
		t.Fatal("a shell opened in the scratch root")
	}
}

// A folder added as a project stays its own project, even under the
// scratch root.
func TestAnAddedFolderUnderTheScratchRootStaysItsOwnProject(t *testing.T) {
	state := tempDir(t)
	dir := filepath.Join(state, "scratch", "kept")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	was := chooseFolder
	chooseFolder = func(*app) string { return dir }
	t.Cleanup(func() { chooseFolder = was })
	h := start(t, state)
	h.a.addProject()
	h.until("the draft in the added folder", func() bool { return h.a.current().View.Workspace == dir })
	th := sendInDraft(h, "Keep me in the added folder")
	if p := h.a.projectOf(th); p.noProject || p.key.workspace != dir {
		t.Fatalf("the thread is in %q (no project %v), want the added folder's project", p.key.workspace, p.noProject)
	}
}
