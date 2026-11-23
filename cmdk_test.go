package main

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
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
	if shown, _ := paletteShows(t, h); !slices.EqualFunc(shown, h.a.commands(), func(x, y command) bool { return x.label == y.label }) {
		t.Errorf("the palette lists %d results, not the %d of its commands", len(shown), len(h.a.commands()))
	}
	h.tt.Type("websocket")
	h.tt.Key(ui.Cmd, ui.KeyEnter)
	settlePlugins(t, h.a, h.tt, h.p)
	loop := h.thread("Investigate websocket reconnect loop")
	if h.a.cmdkOpen || h.a.selected != loop.id() || !h.a.showDiff {
		t.Errorf("⌘↩: open %v, selected %v, diff %v", h.a.cmdkOpen, h.a.selected, h.a.showDiff)
	}

	h.a.openCommandPalette()
	h.tt.Frame()
	h.tt.Type(">")
	shown, _ := paletteShows(t, h)
	for _, cmd := range shown {
		if cmd.section != "Actions" {
			t.Fatalf("%q listed after >", cmd.label)
		}
	}
	h.tt.Type("new thread in mygo")
	h.tt.Key(0, ui.KeyEnter)
	settlePlugins(t, h.a, h.tt, h.p)
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
	tt := newTester(t, a, 1320, 860)
	a.router.Push("/settings/general")
	tt.Frame()
	a.openCommandPalette()
	tt.Frame()
	tt.Type("switch model")
	tt.Key(0, ui.KeyEnter)
	settleTester(tt)
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
	id := h.a.backend.Create(h.local(), t.TempDir(), "Mine", opus, api.AccessAsk, nil)
	th := h.thread("Mine")
	h.until("the thread's view", func() bool { return th.View.Revision > 0 && len(th.Pending) == 0 })
	h.a.select_(th)
	h.tt.Advance(time.Second)
	h.a.router.Push("/settings/general")
	h.tt.Frame()
	h.a.backend.Delete(h.local(), id, false)
	h.until("the thread gone", func() bool { return h.local().Threads[id] == nil })
	h.a.openCommandPalette()
	h.tt.Frame()
	h.tt.Type("switch model")
	h.tt.Key(0, ui.KeyEnter)
	settlePlugins(t, h.a, h.tt, h.p)
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
	settlePlugins(t, h.a, h.tt, h.p)
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
	const out = []
	for (const id of ids) {
		await glitched.commands.run(id).then(() => out.push("Ran " + id), (e) => out.push("Refused " + id + ": " + e.message))
	}
	await glitched.toast(out.join(", "))
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
	if _, all, _ := strings.Cut(run("all", nil), "Ran thread.new,"); all == "" || strings.Contains(strings.Split(all, "|")[0], "Refused") {
		t.Errorf("the palette refused one of its own actions: %q", all)
	}
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
	remove := "project.remove:" + key.id()
	if texts := run("run", map[string]any{"id": remove}); !strings.Contains(texts, "Ran "+remove) || !h.a.removeOpen || !there() {
		t.Fatalf("removing the project: asked %v, still there %v: %q", h.a.removeOpen, there(), texts)
	}
	for _, open := range []*bool{&h.a.removeOpen, &h.a.deleteOpen} {
		*open = true
		if texts := run("run", map[string]any{"id": "diff.toggle"}); !strings.Contains(texts, "a dialog waits for the user") {
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
		draw = func(c *ui.Context) { h.a.drawNative(c, component, nil, row, native(name), nil, componentCopy{}) }
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

// Nothing runs while a plugin's dialog holds the window's keys, as ⌘K
// doesn't open the palette then, and an action's id names a project
// without its workspace's path.
func TestAPluginRunsNoActionUnderADialog(t *testing.T) {
	h := start(t, inboxDir(t))
	writeOwnPlugin(t, h.a, "modal", `{"id": "modal", "name": "Modal", "main": "index.js", "contract": "glitched", "contractVersion": 2, "capabilities": ["commands"],
		"contributes": {"pages": [{"id": "home", "title": "Modal"}], "commands": [{"id": "list", "title": "List"}, {"id": "run", "title": "Run"}]}}`,
		`export function view() {
	return { body: { type: "dialog", id: "wait", title: "Plugin modal", open: true, children: [{ type: "text", text: "Still editing" }] } }
}
export async function command({ id }) {
	if (id === "list") {
		await glitched.toast("Ids", (await glitched.commands.list()).map((c) => c.id).join(" "))
		return
	}
	await glitched.commands.run("settings:appearance").then(() => glitched.toast("Ran"), (e) => glitched.toast("Refused", e.message))
}`)
	if err := h.a.plugins.SetGranted("modal", "commands", true); err != nil {
		t.Fatal(err)
	}
	settle := func() { settlePlugins(t, h.a, h.tt, h.p) }
	run := func(cmd string) string {
		h.a.plugins.RunCommand("modal", cmd, nil, func(err error) {
			if err != nil {
				t.Error(err)
			}
		})
		settle()
		return strings.Join(h.tt.Texts(), "|")
	}
	settle()
	texts := run("list")
	for _, p := range h.a.projects {
		if !strings.Contains(texts, "project.remove:") || strings.Contains(texts, p.key.workspace) {
			t.Fatalf("the ids name %s's workspace: %q", p.name(), texts)
		}
	}

	h.a.router.Push("/plugins/modal/home")
	settle()
	// The page's view comes on a session of its own, which the frame
	// opens and settling the plugins doesn't wait for: ⌘K would find
	// Loading… under it.
	h.until("the page's dialog", func() bool { return h.tt.HasText("Plugin modal") })
	h.tt.Advance(time.Second)
	h.tt.Key(ui.Cmd, ui.KeyK)
	h.tt.Frame()
	if !h.tt.HasText("Plugin modal") || h.a.cmdkOpen {
		t.Fatalf("the dialog doesn't hold ⌘K: %q", h.tt.Texts())
	}
	if texts := run("run"); !strings.Contains(texts, "a dialog waits for the user") || h.a.router.Path() != "/plugins/modal/home" {
		t.Fatalf("an action ran under the dialog, to %s: %q", h.a.router.Path(), texts)
	}
}

// An action waits on a dialog from the pass that shows it, and runs again
// from the pass that shows it gone, as effects run between passes.
func TestAnActionWaitsOnTheDialogOnScreen(t *testing.T) {
	a := offline(t)
	writeOwnPlugin(t, a, "modal", `{"id": "modal", "name": "Modal", "main": "index.js", "contract": "glitched", "contractVersion": 2, "contributes": {"pages": [{"id": "home", "title": "Modal"}]}}`,
		`export function view() {
	return { body: { type: "dialog", id: "wait", title: "Plugin modal", open: true, children: [{ type: "text", text: "Still editing" }] } }
}`)
	stored, _ := offlinePosts.Load(a)
	p := stored.(*posts)
	tt := ui.NewTesterWith(a.view, 1320, 860, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
	settlePlugins(t, a, tt, p)
	until := func(shown bool) {
		t.Helper()
		for deadline := time.Now().Add(3 * time.Second); tt.HasText("Plugin modal") != shown; {
			if time.Now().After(deadline) {
				t.Fatalf("the dialog never came or went (%v)", shown)
			}
			p.run()
			tt.Frame()
		}
	}
	a.router.Push("/plugins/modal/home")
	until(true)
	if err := a.runCommand("sidebar.toggle", ""); err == nil {
		t.Error("an action ran under the dialog the window shows")
	}
	tt.Key(0, ui.KeyEscape)
	until(false)
	if err := a.runCommand("sidebar.toggle", ""); err != nil {
		t.Errorf("an action waited on a dialog the window no longer shows: %v", err)
	}
}

// paletteShows is what the palette lists, once its built-in plugin took
// every key pressed: its results, as the palette's commands are, and the
// one chosen.
func paletteShows(t *testing.T, h *harness) (shown []command, chosen int) {
	t.Helper()
	settlePlugins(t, h.a, h.tt, h.p)
	app := h.a.plugins.App("palette")
	if app == nil {
		t.Fatal("the built-in palette doesn't run")
	}
	var list *widget.Node
	var find func(*widget.Node)
	find = func(n *widget.Node) {
		if n == nil || list != nil {
			return
		}
		if n.Type == "list" {
			list = n
			return
		}
		for _, ch := range n.Children {
			find(ch)
		}
	}
	find(app.PlaceView("palette", "palette:"+strconv.Itoa(h.a.cmdkOpens)))
	if list == nil {
		t.Fatal("the palette draws no results")
	}
	by := map[string]command{}
	for _, cmd := range h.a.actions() {
		by["a:"+cmd.id] = cmd
	}
	for _, cmd := range h.a.threadCommands() {
		by["t:"+cmd.t.id().String()] = cmd
	}
	var key string
	json.Unmarshal(list.Value, &key)
	chosen = -1
	for _, it := range list.Items {
		if it.Header {
			continue
		}
		if it.Key == key {
			chosen = len(shown)
		}
		shown = append(shown, by[it.Key])
	}
	return shown, chosen
}

const paletteManifest = `{"id": "mine", "name": "mine", "main": "index.js", "contract": "glitched", "contractVersion": 2,
	"capabilities": ["ui.place", "ui.replace", "threads:read"], "glitched": {"places": [{"id": "palette", "at": "palette", "replaces": true}]}}`

// The palette is the built-in Command palette plugin's, till a plugin of
// yours replaces it; yours throwing, answering null or failing to draw its
// rows brings the built-in back, and with the built-in off Glitched draws
// its own.
func TestThePaletteIsABuiltInPluginYoursReplaces(t *testing.T) {
	a := offline(t)
	tt := newTester(t, a, 1320, 860)
	tt.Frame()
	a.openCommandPalette()
	shows := func(text string) bool {
		settleTester(tt)
		return tt.HasText(text)
	}
	builtin := func() bool {
		settleTester(tt)
		app := a.plugins.App("palette")
		drawn := shows("Toggle diff panel")
		return drawn && app != nil && app.PlaceView("palette", "palette:"+strconv.Itoa(a.cmdkOpens)) != nil
	}
	if !builtin() {
		t.Fatalf("the built-in doesn't draw the palette: %q", tt.Texts())
	}

	writeOwnPlugin(t, a, "mine", paletteManifest, mineModule)
	a.plugins.SetGranted("mine", "ui.replace", true)
	a.plugins.SetGranted("mine", "threads:read", true)
	if !shows("Mine") || shows("Toggle diff panel") {
		t.Fatalf("yours doesn't replace the built-in: %q", tt.Texts())
	}
	for name, js := range map[string]string{
		"throws": brokenModule,
		"null":   `export function place({ items }) { return items.map(() => null) }`,
		"rows": `export function place({ items }) {
	return items.map(() => ({ type: "list", id: "l", label: "Mine", height: 100, items: [{ key: "a", label: "Mine" }] }))
}
export function rows() { throw new Error("broken on purpose") }`,
	} {
		writeOwnPlugin(t, a, "mine", paletteManifest, js)
		a.openCommandPalette()
		if !builtin() {
			t.Fatalf("yours that %s left no built-in: %q", name, tt.Texts())
		}
	}

	a.plugins.SetEnabled("mine", false)
	if err := a.plugins.SetEnabled("palette", false); err != nil {
		t.Fatal(err)
	}
	a.pluginsChanged(nil)
	a.openCommandPalette()
	if !shows("Toggle diff panel") || len(a.plugins.Replacers("palette")) > 0 {
		t.Fatalf("with no plugin to draw it, Glitched's own palette doesn't show: %q", tt.Texts())
	}
}

// With the built-in off and yours failing, Glitched draws its own palette,
// and Settings says so beside the failure.
func TestAFailedPaletteSaysGlitchedDrawsItsOwn(t *testing.T) {
	a := offline(t)
	tt := newTester(t, a, 1320, 860)
	tt.Frame()
	if err := a.plugins.SetEnabled("palette", false); err != nil {
		t.Fatal(err)
	}
	a.pluginsChanged(nil)
	writeOwnPlugin(t, a, "mine", paletteManifest, mineModule)
	a.plugins.SetGranted("mine", "ui.replace", true)
	a.plugins.SetGranted("mine", "threads:read", true)
	a.openCommandPalette()
	settleTester(tt)
	if !tt.HasText("Mine") {
		t.Fatalf("yours doesn't draw the palette: %q", tt.Texts())
	}
	writeOwnPlugin(t, a, "mine", paletteManifest, brokenModule)
	settleTester(tt)
	if !tt.HasText("Toggle diff panel") || tt.HasText("Mine") || a.plugins.Failed("mine") == nil {
		t.Fatalf("yours that throws didn't leave Glitched's own palette: %v %q", a.plugins.Failed("mine"), tt.Texts())
	}
	tt.Key(0, ui.KeyEscape)
	p := installed(a, "mine")
	a.pluginsOpen[p.Folder] = true
	a.router.Push("/settings/plugins")
	settleTester(tt)
	if why := a.partState(p, "palette").why; !tt.HasText(why) || !strings.HasPrefix(why, "It failed, so Glitched draws its own:") {
		t.Errorf("Settings says %q of the failed palette", why)
	}
}

const queryPalette = `
export function init() {
  return {state: {query: ""}};
}
export function place({state, items}) {
  return items.map(() => ({
    type: "column", children: [
      {type: "input", id: "query", style: "bare", placeholder: "Mine", controlled: true, value: state.query, onChange: "query", onSubmit: "submit"},
      {type: "text", text: state.query === "toggle terminal" ? "Mine: Toggle terminal" : "Mine: no match"}
    ]
  }));
}
export async function update({state, event}) {
  if (event.kind !== "ui") return {};
  if (event.handler === "query") {
    if (event.value.endsWith("!")) throw new Error("broken on purpose");
    return {state: {query: event.value}};
  }
  if (event.handler === "submit" && state.query === "toggle terminal") {
    await glitched.commands.run("terminal.toggle", {palette: event.place.key});
  }
  return {};
}
`

// With the built-in off, a palette of yours that fails mid-search keeps
// its last drawing, so Enter runs what it showed, not what Glitched's
// own palette would have chosen.
func TestAPaletteThatFailsMidSearchKeepsWhatItShowed(t *testing.T) {
	h := start(t, inboxDir(t))
	th := h.thread("SIP re-INVITE handling")
	h.a.select_(th)
	if err := h.a.plugins.SetEnabled("palette", false); err != nil {
		t.Fatal(err)
	}
	h.a.pluginsChanged(nil)
	writeOwnPlugin(t, h.a, "mine", strings.Replace(paletteManifest, `"threads:read"]`, `"threads:read", "commands"]`, 1), queryPalette)
	for _, g := range []string{"ui.replace", "threads:read", "commands"} {
		h.a.plugins.SetGranted("mine", g, true)
	}
	step := func() {
		settlePlugins(t, h.a, h.tt, h.p)
		h.tt.Advance(time.Second)
		settlePlugins(t, h.a, h.tt, h.p)
	}
	h.a.openCommandPalette()
	step()
	h.tt.Type("toggle terminal")
	step()
	if !h.tt.HasText("Mine: Toggle terminal") {
		t.Fatalf("yours doesn't show its result: %q", h.tt.Texts())
	}
	h.tt.Type("!")
	step()
	if h.a.plugins.Failed("mine") == nil || !h.tt.HasText("Mine: Toggle terminal") {
		t.Fatalf("yours that failed mid-search didn't keep what it showed: %v %q", h.a.plugins.Failed("mine"), h.tt.Texts())
	}
	h.tt.Key(0, ui.KeyEnter)
	step()
	if h.a.selected != th.id() || !h.a.showTerminal {
		t.Errorf("Enter after the failure left %s selected and the terminal shown %v, want %s and true", h.a.selected, h.a.showTerminal, th.id())
	}
}

// The pointer chooses the row it moves onto, never one that comes under
// it at rest: there the arrows' choice holds. Up and Down go round from
// one end to the other.
func TestThePalettesChoiceFollowsThePointerAsItMoves(t *testing.T) {
	h := start(t, inboxDir(t))
	h.thread("SIP re-INVITE handling")
	h.tt.Move(500, 330) // where Add project… will be
	h.tt.Frame()
	h.a.openCommandPalette()
	chosen := func() string {
		shown, i := paletteShows(t, h)
		if i < 0 {
			return ""
		}
		return shown[i].label
	}
	if got := chosen(); got != "New thread" {
		t.Fatalf("the palette opened under the pointer chose %q, want the first", got)
	}
	h.tt.Key(0, ui.KeyDown)
	if got := chosen(); got != "New thread without a project" {
		t.Fatalf("Down chose %q", got)
	}
	r, ok := h.tt.Find("Switch model")
	if !ok {
		t.Fatal("Switch model isn't shown")
	}
	h.tt.Move(r.X+2, r.Y+2)
	if got := chosen(); got != "Switch model" {
		t.Fatalf("the pointer moving onto Switch model chose %q", got)
	}
	shown, _ := paletteShows(t, h)
	h.a.cmdkOpen = false
	h.tt.Frame()
	h.a.openCommandPalette()
	if got := chosen(); got != "New thread" {
		t.Fatalf("the palette opened again under the pointer chose %q, want the first", got)
	}
	h.tt.Key(0, ui.KeyUp)
	if got := chosen(); got != shown[len(shown)-1].label {
		t.Fatalf("Up from the first chose %q, want the last", got)
	}
	h.tt.Key(0, ui.KeyDown)
	if got := chosen(); got != "New thread" {
		t.Fatalf("Down from the last chose %q, want the first", got)
	}
}

// The palette's keys say what they do, each row tells assistive tools
// what it shows, and Escape puts it away.
func TestThePalettesKeysAndNames(t *testing.T) {
	h := start(t, inboxDir(t))
	h.thread("SIP re-INVITE handling")
	h.a.openCommandPalette()
	paletteShows(t, h)
	for _, s := range []string{"esc", "↑↓", "navigate", "↩", "open", "⌘↩", "open with diff", "Type", ">", "for actions"} {
		if !slices.Contains(h.tt.Texts(), s) {
			t.Errorf("no %q among the palette's keys", s)
		}
	}
	if _, ok := h.tt.AccessByRole(ui.RoleButton, "New thread ⌘N"); !ok {
		t.Error("the New thread row doesn't name its shortcut to assistive tools")
	}
	h.tt.Type("websocket")
	paletteShows(t, h)
	if _, ok := h.tt.AccessByRole(ui.RoleButton, "Investigate websocket reconnect loop t3code now"); !ok {
		t.Errorf("the thread's row doesn't name its project and age: %+v", h.tt.AccessAll())
	}
	h.tt.Key(0, ui.KeyEscape)
	h.tt.Frame()
	if h.a.cmdkOpen {
		t.Error("Escape left the palette open")
	}
}

// The palette's rows show in the frame it opens in: Glitched waits for
// the rows in view as it waits for the palette's first drawing.
func TestThePalettesRowsShowAsItOpens(t *testing.T) {
	a := offline(t)
	tt := ui.NewTester(a.view, 1320, 860)
	tt.Frame()
	a.openCommandPalette()
	tt.Frame()
	if app := a.plugins.App("palette"); app == nil || app.PlaceView("palette", "palette:"+strconv.Itoa(a.cmdkOpens)) == nil {
		t.Fatal("the built-in palette didn't draw as it opened")
	}
	for _, s := range []string{"New thread", "⌘N", "Toggle diff panel", "⌘D"} {
		if !slices.Contains(tt.Texts(), s) {
			t.Errorf("%q isn't shown as the palette opens: %q", s, tt.Texts())
		}
	}
}

// What was chosen in a palette closed since runs, and leaves the palette
// opened after it be, as Glitched's own palette did.
func TestAnEarlierChoiceLeavesThePaletteOpenedSince(t *testing.T) {
	h := start(t, inboxDir(t))
	h.thread("SIP re-INVITE handling")
	h.a.openCommandPalette()
	paletteShows(t, h)
	h.tt.Type("toggle terminal")
	paletteShows(t, h)
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Key(0, ui.KeyEscape)
	h.tt.Frame()
	h.a.openCommandPalette()
	h.tt.Frame()
	settlePlugins(t, h.a, h.tt, h.p)
	if !h.a.showTerminal || !h.a.cmdkOpen {
		t.Fatalf("terminal %v, palette open %v: want the terminal toggled and the new palette open", h.a.showTerminal, h.a.cmdkOpen)
	}
}

// Enter runs what the query before it found, though the query is
// replaced before the palette has caught up, as on Glitched's own.
func TestThePaletteRunsWhatWasFoundBeforeEnter(t *testing.T) {
	h := start(t, inboxDir(t))
	h.thread("SIP re-INVITE handling")
	h.a.openCommandPalette()
	paletteShows(t, h)
	diff, terminal := h.a.showDiff, h.a.showTerminal
	h.tt.Type("toggle diff panel")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Key(ui.Cmd, ui.KeyA)
	h.tt.Type("toggle terminal")
	settlePlugins(t, h.a, h.tt, h.p)
	if h.a.showDiff == diff || h.a.showTerminal != terminal || h.a.cmdkOpen {
		t.Fatalf("diff %v, terminal %v, palette open %v: want the diff toggled alone and the palette closed", h.a.showDiff, h.a.showTerminal, h.a.cmdkOpen)
	}
}

// The palette's rows are as tall as Glitched's own palette draws them,
// header, rows and No results, and its list as tall, at any interface
// font size: its rows follow the interface font's lines, as its text does
// on each platform, not a height measured once on one of them.
func TestThePalettesRowsAreAsTallAsGlitchedsOwn(t *testing.T) {
	for _, size := range []float64{11, 13, 16} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			// gaps are what the palette shows apart, the builtin drawing it or
			// Glitched: a row's height, the Actions header and the rows down
			// to Toggle diff panel, the keys from the first of a few results,
			// and from No results.
			gaps := func(builtin bool) [4]float32 {
				a := offline(t)
				a.settings.fontSize = size
				if !builtin {
					if err := a.plugins.SetEnabled("palette", false); err != nil {
						t.Fatal(err)
					}
					a.pluginsChanged(nil)
				}
				tt := newTester(t, a, 1320, 860)
				tt.Frame()
				a.openCommandPalette()
				settleTester(tt)
				drawn := a.plugins.App("palette") != nil && a.plugins.App("palette").PlaceView("palette", a.paletteKey()) != nil
				if drawn != builtin {
					t.Fatalf("the built-in drew the palette: %v, want %v", drawn, builtin)
				}
				y := func(text string) float32 {
					t.Helper()
					r, ok := tt.Find(text)
					if !ok {
						t.Fatalf("no %q in %q", text, tt.Texts())
					}
					return r.Y
				}
				row := y("Toggle terminal") - y("Toggle diff panel")
				header := y("Toggle diff panel") - y("Actions")
				tt.Type("toggle")
				settleTester(tt)
				few := y("navigate") - y("Toggle diff panel")
				tt.Type(" no such thing")
				settleTester(tt)
				return [4]float32{row, header, few, y("navigate") - y("No results")}
			}
			if own, builtin := gaps(false), gaps(true); own != builtin {
				t.Errorf("at %vpt the built-in's row, header, few results and No results are %v apart, Glitched's own %v", size, builtin, own)
			}
		})
	}
}
