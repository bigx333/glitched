package main

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo"
)

const menusManifest = `{"id": "mine", "name": "Mine", "main": "index.js", "contract": "glitched", "contractVersion": 2,
	"capabilities": ["ui.place", "threads:read", "threads:write", "messages:read"],
	"contributes": {"commands": [{"id": "tag", "title": "Tag thread"}, {"id": "pin", "title": "Pin project"}, {"id": "quote", "title": "Quote"}]},
	"glitched": {"menus": [{"command": "tag", "at": "thread.menu"}, {"command": "pin", "at": "project.menu"}, {"command": "quote", "at": "message.actions", "icon": "copy"}]}}`

const menusModule = `export async function command({ id, args }) {
	if (id === "tag") await glitched.threads.rename(args.thread.id, "Tagged: " + args.thread.title)
	if (id === "pin") await glitched.toast("Pinned " + args.project.name, args.project.workspace ? "with its folder" : "without its folder")
	if (id === "quote") await glitched.toast("Quoted the " + args.message.role, args.message.text || "no text")
}`

// A plugin's commands join the thread's and the project's menus and a
// message's actions, after the app's own, and are passed what they're
// about, as far as the plugin's grants let it see.
func TestAPluginsCommandsJoinTheAppsMenus(t *testing.T) {
	h, th := checked(t)
	const msg = "Sort them by most recent activity."
	title := th.View.Title
	h.tt.RightClick(title)
	plain := h.tt.Menu()
	h.tt.CloseMenu()

	writeOwnPlugin(t, h.a, "mine", menusManifest, menusModule)
	for _, c := range []string{"threads:read", "threads:write", "messages:read"} {
		if err := h.a.plugins.SetGranted("mine", c, true); err != nil {
			t.Fatal(err)
		}
	}
	settle := func() { settlePlugins(t, h.a, h.tt, h.p) }
	settle()
	shows := func(s string) bool { return strings.Contains(strings.Join(h.tt.Texts(), "|"), s) }

	if err := h.tt.RightClick(title); err != nil {
		t.Fatal(err)
	}
	if m := h.tt.Menu(); len(m) != len(plain)+2 || m[len(m)-3] != "Tag thread" || m[len(m)-1] != "Delete…" {
		t.Fatalf("the thread's menu is %q; without the plugin %q", m, plain)
	}
	if err := h.tt.ChooseMenuItem("Tag thread"); err != nil {
		t.Fatal(err)
	}
	settle()
	h.until("the rename", func() bool { return th.View.Title == "Tagged: "+title })
	h.tt.Advance(time.Second)

	project := h.a.projects[0].name()
	if err := h.tt.RightClick(project); err != nil {
		t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem("Pin project"); err != nil {
		t.Fatalf("the project's menu is %q: %v", h.tt.Menu(), err)
	}
	settle()
	if !shows("Pinned "+project) || !shows("with its folder") {
		t.Fatalf("the project's command wasn't passed it: %q", h.tt.Texts())
	}

	h.hover(msg)
	h.tt.Advance(time.Second)
	h.click("Quote")
	settle()
	if !shows("Quoted the user") || !shows(msg) {
		t.Fatalf("the message's action wasn't passed it: %q", h.tt.Texts())
	}

	if err := h.a.plugins.SetGranted("mine", "messages:read", false); err != nil {
		t.Fatal(err)
	}
	h.tt.Advance(10 * time.Second)
	h.hover(msg)
	h.tt.Advance(time.Second)
	h.click("Quote")
	settle()
	if !shows("no text") {
		t.Fatalf("the message's text went to a plugin without messages:read: %q", h.tt.Texts())
	}
}

// threads.delete asks the user, whatever the settings say; the plugin
// deletes nothing itself.
func TestAPluginsDeleteAsksFirst(t *testing.T) {
	h, th := checked(t)
	h.a.settings.confirmDelete = false
	writeOwnPlugin(t, h.a, "mine", `{"id": "mine", "name": "Mine", "main": "index.js", "contract": "glitched", "contractVersion": 2,
		"capabilities": ["ui.place", "threads:read", "threads:write"],
		"contributes": {"commands": [{"id": "drop", "title": "Drop"}]}, "glitched": {"menus": [{"command": "drop", "at": "thread.menu"}]}}`,
		`export async function command({ args }) {
	await glitched.threads.delete(args.thread.id)
	await glitched.threads.delete(args.thread.id).catch((e) => glitched.toast("Refused", e.message))
}`)
	for _, c := range []string{"threads:read", "threads:write"} {
		if err := h.a.plugins.SetGranted("mine", c, true); err != nil {
			t.Fatal(err)
		}
	}
	settlePlugins(t, h.a, h.tt, h.p)
	h.until("the agent to finish", func() bool { return !th.View.Live() })
	if err := h.tt.RightClick(th.View.Title); err != nil {
		t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem("Drop"); err != nil {
		t.Fatal(err)
	}
	settlePlugins(t, h.a, h.tt, h.p)
	if h.a.deleting.Thread == nil || !h.a.deleteOpen || !h.tt.HasText("Delete “"+th.View.Title+"”?") {
		t.Fatalf("the delete didn't ask: %q", h.tt.Texts())
	}
	if _, ok := h.a.find(th.id()); !ok {
		t.Fatal("the plugin deleted the thread unasked")
	}
	if !strings.Contains(strings.Join(h.tt.Texts(), "|"), "another delete waits for the user") {
		t.Errorf("a second delete took over the open confirm: %q", h.tt.Texts())
	}
}

// A plugin's notification about a thread opens it as the app's do, its
// draft goes in that thread's composer, its project removal asks first,
// and it hears the window come to the front and go.
func TestAPluginNotifiesDraftsAndRemovesAboutAThread(t *testing.T) {
	h, th := checked(t)
	var got []mygo.NotificationOptions
	h.a.notify = func(o mygo.NotificationOptions) { got = append(got, o) }
	writeOwnPlugin(t, h.a, "n", `{"id": "n", "name": "N", "main": "index.js", "contract": "glitched", "contractVersion": 2,
		"capabilities": ["notify", "composer", "threads:read", "threads:write"],
		"contributes": {"commands": [{"id": "go", "title": "Go"}, {"id": "bad", "title": "Bad"}]}, "glitched": {"events": ["window.focused"]}}`,
		`export function init() { return { state: {} } }
export function view() { return { body: { type: "text", text: "n" } } }
export async function update({ event }) {
	if (event.kind === "app" && event.type === "window.focused") await glitched.toast(event.data.focused ? "In front" : "Behind")
	return {}
}
export async function command({ id }) {
	const [t] = (await glitched.threads.list()).filter((t) => t.title === "`+th.View.Title+`")
	if (id === "bad") {
		const why = []
		for (const title of ["", "x".repeat(201)]) await glitched.threads.rename(t.id, title).catch((e) => why.push(e.message))
		await glitched.projects.remove("/nowhere").catch((e) => why.push(e.message))
		await glitched.projects.remove(t.workspace, "elsewhere").catch((e) => why.push(e.message))
		await glitched.toast("Refused", why.join(" / "))
		return
	}
	await glitched.notify("Reviewed", "All good.", { thread: t.id })
	await glitched.composer.draft("Ship it", { thread: t.id })
	await glitched.projects.remove(t.workspace, t.machine)
	await glitched.projects.remove(t.workspace, t.machine).catch((e) => glitched.toast("Twice", e.message))
}`)
	for _, c := range []string{"threads:read", "threads:write"} {
		if err := h.a.plugins.SetGranted("n", c, true); err != nil {
			t.Fatal(err)
		}
	}
	settle := func() { settlePlugins(t, h.a, h.tt, h.p) }
	settle()
	h.until("the agent to finish", func() bool { return !th.View.Live() })
	if _, err := h.a.plugins.Session("n", ""); err != nil {
		t.Fatal(err)
	}
	h.a.router.Push("/settings/general")
	settle()
	run := func(cmd string) {
		h.a.plugins.RunCommand("n", cmd, nil, func(err error) {
			if err != nil {
				t.Error(err)
			}
		})
		settle()
	}
	run("go")
	if len(got) != 1 || got[0].ID != "thread:"+th.id().String() {
		t.Fatalf("notifications %+v, want one about the thread", got)
	}
	if h.a.selected != th.id() || h.a.draft != "Ship it" {
		t.Errorf("the draft went to %v as %q", h.a.selected, h.a.draft)
	}
	if !h.a.removeOpen {
		t.Error("removing the project didn't ask")
	}
	if !strings.Contains(strings.Join(h.tt.Texts(), "|"), "another removal waits for the user") {
		t.Errorf("a second removal took over the open confirm: %q", h.tt.Texts())
	}
	h.a.removeOpen = false
	run("bad")
	texts := strings.Join(h.tt.Texts(), "|")
	if !strings.Contains(texts, `a title of up to 200 bytes / threads.rename needs a title of up to 200 bytes / no project "/nowhere" / no project`) {
		t.Errorf("the bad calls weren't refused: %q", texts)
	}

	h.tt.SetWindowActive(false)
	settle()
	if !h.tt.HasText("Behind") {
		t.Errorf("the plugin didn't hear the window go behind: %q", h.tt.Texts())
	}
}
