package main

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/daemon"
	"glitched/internal/plugins/widget"
	"glitched/internal/provider/fake"
)

// The palette lists threads by recency, opens one with its diff on ⌘↩,
// keeps to actions after >, and starts a thread in a chosen project.
func TestPaletteCommands(t *testing.T) {
	h := start(t, inboxDir(t))
	h.thread("SIP re-INVITE handling")
	h.a.openCommandPalette()
	h.tt.Frame()
	var last time.Time
	n := 0
	for _, cmd := range h.a.commands() {
		if cmd.t == nil {
			continue
		}
		if n > 0 && cmd.t.View.UpdatedAt.After(last) {
			t.Errorf("%q, updated %v, listed after a thread updated %v", cmd.label, cmd.t.View.UpdatedAt, last)
		}
		last, n = cmd.t.View.UpdatedAt, n+1
	}
	if n != 5 {
		t.Errorf("%d threads listed, want 5", n)
	}
	h.tt.Type("websocket")
	h.tt.Key(ui.Cmd, ui.KeyEnter)
	loop := h.thread("Investigate websocket reconnect loop")
	if h.a.cmdkOpen || h.a.selected != loop.id() || !h.a.showDiff {
		t.Errorf("⌘↩: open %v, selected %v, diff %v", h.a.cmdkOpen, h.a.selected, h.a.showDiff)
	}

	h.a.openCommandPalette()
	h.tt.Frame()
	h.tt.Type(">")
	for _, cmd := range h.a.commands() {
		if cmd.section != "Actions" {
			t.Fatalf("%q listed after >", cmd.label)
		}
	}
	h.tt.Type("new thread in mygo")
	h.tt.Key(0, ui.KeyEnter)
	if cur := h.a.current(); !cur.id().IsZero() || cur.View.Workspace != "/work/mygo" {
		t.Errorf("new thread %v in %q, want a draft in /work/mygo", cur.id(), cur.View.Workspace)
	}
}

// ⌘↩ in the palette is the palette's: it doesn't reach the approval
// behind it.
func TestPaletteKeepsCmdEnter(t *testing.T) {
	h, th := asking(t, runTests)
	h.a.openCommandPalette()
	h.tt.Frame()
	h.tt.Type("toggle terminal")
	h.tt.Key(ui.Cmd, ui.KeyEnter)
	h.tt.Advance(time.Second)
	if r := lastRequest(th); r.State != api.RequestPending || len(th.Pending) != 0 {
		t.Errorf("⌘↩ in the palette answered the approval behind it: %v, %d pending", r.State, len(th.Pending))
	}
}

// Switch model opens the picker from a settings page too, over the
// chat's composer.
func TestPaletteSwitchModel(t *testing.T) {
	a := offline(t)
	tt := ui.NewTester(a.view, 1320, 860)
	a.router.Push("/settings/general")
	tt.Frame()
	a.openCommandPalette()
	tt.Frame()
	tt.Type("switch model")
	tt.Key(0, ui.KeyEnter)
	tt.Advance(time.Second)
	if a.router.Path() != "/" || !a.pickerOpen || !tt.HasText("OpenAI") {
		t.Errorf("path %q, picker %v", a.router.Path(), a.pickerOpen)
	}
}

// It opens the picker of the composer the chat comes back to, though the
// pickers were last another's: the thread shown before the settings page
// was deleted since, so the draft's.
func TestPaletteSwitchModelOpensThePickerOfTheComposerShown(t *testing.T) {
	h := start(t, tempDir(t))
	opus := api.ModelRef{Provider: "claude", Model: "Claude Opus 5.5", Effort: "high"}
	id := h.a.backend.Create(h.local(), t.TempDir(), "Mine", opus, api.AccessAsk)
	th := h.thread("Mine")
	h.until("the thread's view", func() bool { return th.View.Revision > 0 && len(th.Pending) == 0 })
	h.a.select_(th)
	h.tt.Advance(time.Second)
	h.a.router.Push("/settings/general")
	h.tt.Frame()
	h.a.backend.Delete(h.local(), id)
	h.until("the thread gone", func() bool { return h.local().Threads[id] == nil })
	h.a.openCommandPalette()
	h.tt.Frame()
	h.tt.Type("switch model")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Advance(time.Second)
	if h.a.router.Path() != "/" || !h.a.current().id().IsZero() || !h.a.pickerOpen || !h.tt.HasText("Faster") {
		t.Errorf("path %q, thread %q, picker %v: the window shows %q", h.a.router.Path(), h.a.current().View.Title, h.a.pickerOpen, h.tt.Texts())
	}
}

// Two threads of one title are two rows of the palette.
func TestPaletteListsThreadsOfOneTitle(t *testing.T) {
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(false)))
	seed(t, d, "/work/t3code", "Fix the build")
	seed(t, d, "/work/mygo", "Fix the build")
	h := start(t, dir)
	h.thread("Fix the build")
	h.a.openCommandPalette()
	h.tt.Frame()
	h.tt.Type("fix the build")
	h.tt.Frame()
	n := 0
	for _, s := range h.tt.Texts() {
		if s == "Fix the build" {
			n++
		}
	}
	if n != 4 {
		t.Fatalf("%q shows %d times, want twice in the palette and twice in the sidebar", "Fix the build", n)
	}
}

// A plugin lists the palette's actions and runs them as choosing them
// there does, once granted: only those, never past a confirm, and none of
// them answers an approval.
func TestAPluginRunsOnlyThePalettesActions(t *testing.T) {
	h, th := approving(t)
	writeOwnPlugin(t, h.a, "cmds", `{"id": "cmds", "name": "Cmds", "main": "index.js", "contract": "glitched", "contractVersion": 2,
		"capabilities": ["commands"], "contributes": {"commands": [{"id": "list", "title": "List"}, {"id": "run", "title": "Run"}, {"id": "all", "title": "All"}]}}`,
		`export async function command({ id, args }) {
	if (id === "list") {
		const cs = await glitched.commands.list()
		await glitched.toast("Listed", cs.map((c) => [c.id, c.section, c.label, c.hint ?? "", c.icon].join("=")).join("|"))
		return
	}
	const ids = id === "all" ? (await glitched.commands.list()).map((c) => c.id).filter((id) => !id.startsWith("project.") && !id.startsWith("plugin:")) : [args.id]
	for (const id of ids) {
		try {
			await glitched.commands.run(id)
			await glitched.toast("Ran " + id)
		} catch (e) {
			await glitched.toast("Refused " + id, e.message)
		}
	}
}`)
	settle := func() { settlePlugins(t, h.a, h.tt, h.p) }
	settle()
	run := func(cmd string, args map[string]any) string {
		t.Helper()
		h.a.plugins.RunCommand("cmds", cmd, args, func(err error) {
			if err != nil {
				t.Error(err)
			}
		})
		settle()
		return strings.Join(h.tt.Texts(), "|")
	}
	shown := h.a.showSidebar
	if texts := run("run", map[string]any{"id": "sidebar.toggle"}); !strings.Contains(texts, "Refused sidebar.toggle") || h.a.showSidebar != shown {
		t.Fatalf("a plugin not granted commands ran one: %q", texts)
	}
	if err := h.a.plugins.SetGranted("cmds", "commands", true); err != nil {
		t.Fatal(err)
	}
	settle()

	var want []string
	for _, cmd := range h.a.actions() {
		want = append(want, strings.Join([]string{cmd.id, cmd.section, cmd.label, cmd.hint, cmd.icon}, "="))
		if appIconNamed(cmd.icon) == nil || cmd.id == "" {
			t.Errorf("%q has no id or no icon by the name %q", cmd.label, cmd.icon)
		}
	}
	if texts := run("list", nil); !strings.Contains(texts, strings.Join(want, "|")) || !strings.Contains(texts, "plugin:cmds:run=Actions=Cmds: Run") {
		t.Fatalf("the list isn't the palette's actions, in its order: %q, want %q", texts, want)
	}

	h.a.openCommandPalette()
	h.tt.Frame()
	if texts := run("run", map[string]any{"id": "sidebar.toggle"}); !strings.Contains(texts, "Ran sidebar.toggle") || h.a.showSidebar == shown || h.a.cmdkOpen {
		t.Fatalf("running Toggle sidebar: sidebar %v, palette open %v: %q", h.a.showSidebar, h.a.cmdkOpen, texts)
	}
	for _, id := range []string{th.id().String(), "approve", "Toggle sidebar", ""} {
		if texts := run("run", map[string]any{"id": id}); !strings.Contains(texts, "Refused "+id) {
			t.Errorf("%q isn't one of the palette's actions, yet it ran: %q", id, texts)
		}
	}

	// Every action but the projects' and the plugins' own leaves the
	// approval to the user.
	run("all", nil)
	if t2, ok := h.a.find(th.id()); !ok || len(t2.View.Requests) == 0 {
		t.Fatal("an action answered the approval")
	}
	h.a.router.Push("/")
	h.a.select_(th)
	h.a.showSidebar = true
	h.tt.Advance(time.Second)
	if !h.tt.HasText("Approval needed") {
		t.Fatalf("the approval card is gone: %q", h.tt.Texts())
	}

	// Remove project asks with Glitched's confirm, and nothing runs while
	// a confirm waits.
	key := h.a.projectOf(th).key
	there := func() bool { return slices.ContainsFunc(h.a.projects, func(p *project) bool { return p.key == key }) }
	remove := "project.remove:" + key.String()
	if texts := run("run", map[string]any{"id": remove}); !strings.Contains(texts, "Ran "+remove) || !h.a.removeOpen || !there() {
		t.Fatalf("removing the project: asked %v, still there %v: %q", h.a.removeOpen, there(), texts)
	}
	for _, open := range []*bool{&h.a.removeOpen, &h.a.deleteOpen} {
		*open = true
		if texts := run("run", map[string]any{"id": "diff.toggle"}); !strings.Contains(texts, "a confirm waits for the user") {
			t.Errorf("an action ran under a confirm: %q", texts)
		}
		*open = false
	}
}

// The natives of a thread's row draw the sidebar's own parts of that
// thread's row, in the components they name alone.
func TestARowsNativesDrawTheSidebarsOwnParts(t *testing.T) {
	h, th := approving(t, "Write the tests")
	settlePlugins(t, h.a, h.tt, h.p)
	native := func(name string) *widget.Node {
		n, err := widget.DecodeRow([]byte(`{"type": "native", "name": "` + name + `"}`))
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	draw := func(*ui.Context) {}
	tt := ui.NewTester(func(c *ui.Context) { ui.Row(c).Children(func() { draw(c) }) }, 600, 80)
	shows := func(component, row, name string) {
		t.Helper()
		draw = func(c *ui.Context) { h.a.drawNative(c, component, nil, row, native(name), nil, nil) }
		tt.Frame()
		tt.Advance(time.Second)
	}
	id := th.id().String()
	for _, at := range [][2]string{{"thread.header.actions", id}, {"sidebar.body", ""}, {"sidebar.body", api.NewID().String()}} {
		shows(at[0], at[1], "thread.tile")
		if !tt.HasText("thread.tile draws only in sidebar.body.") {
			t.Errorf("in %s, about %q: %q", at[0], at[1], tt.Texts())
		}
	}

	shows("sidebar.body", id, "thread.children")
	if err := tt.Click("1 child thread"); err != nil {
		t.Fatalf("no children's toggle: %v, %q", err, tt.Texts())
	}
	tt.Frame()
	if !h.a.childrenOpen[th.id()] {
		t.Error("the toggle doesn't open the children")
	}

	shows("sidebar.body", id, "thread.rename")
	if _, ok := tt.Find("Thread title"); ok {
		t.Error("a thread not being renamed shows the field")
	}
	h.a.renaming, h.a.renameDraft = th.id(), "Renamed"
	shows("sidebar.body", id, "thread.rename")
	if _, ok := tt.Find("Thread title"); !ok {
		t.Errorf("a thread being renamed shows no field: %q", tt.Texts())
	}
	h.a.renaming = api.ID{}

	shows("sidebar.body", id, "thread.row.actions")
	if !tt.HasText("Snooze") {
		t.Errorf("the row's actions aren't the built-in's: %q", tt.Texts())
	}
	for _, name := range []string{"thread.tile", "thread.status"} {
		shows("palette", id, name)
		if drawn := tt.HasText(name + " draws only in sidebar.body."); drawn != (name == "thread.tile") {
			t.Errorf("%s in the palette: %q", name, tt.Texts())
		}
	}
}
