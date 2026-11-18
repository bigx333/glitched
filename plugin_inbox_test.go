package main

import (
	"slices"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
)

// The sidebar's row actions and its project strip are built-in plugins;
// yours replace them once it may write threads and read them, and a
// broken one leaves the built-ins.
func TestTheRowActionsAndProjectStripAreBuiltInPluginsYoursReplace(t *testing.T) {
	dir := tempDir(t)
	inbox(t, dir)
	h := start(t, dir)
	th := h.thread("SIP re-INVITE handling")
	h.tt.Advance(time.Second)
	h.hover(th.View.Title)
	if !h.tt.HasText("Snooze") || !h.tt.HasText("All") {
		t.Fatalf("the built-ins don't draw: %q", h.tt.Texts())
	}

	manifest := `{"id": "mine", "name": "mine", "main": "index.js", "contract": "glitched", "contractVersion": 2,
		"capabilities": ["ui.place", "ui.replace", "threads:read", "threads:write"], "glitched": {"places": [
			{"id": "actions", "at": "thread.row.actions", "replaces": true},
			{"id": "strip", "at": "sidebar.projects", "replaces": true}]}}`
	writeOwnPlugin(t, h.a, "mine", manifest, `export function place({ at, items }) {
	return items.map(() => ({ type: "text", text: "Mine: " + at }))
}`)
	for _, c := range []string{"ui.replace", "threads:read"} {
		if err := h.a.plugins.SetGranted("mine", c, true); err != nil {
			t.Fatal(err)
		}
	}
	settlePlugins(t, h.a, h.tt, h.p)
	h.hover(th.View.Title)
	settlePlugins(t, h.a, h.tt, h.p)
	if !h.tt.HasText("Mine: sidebar.projects") || h.tt.HasText("All") {
		t.Errorf("yours doesn't replace the strip: %q", h.tt.Texts())
	}
	if h.tt.HasText("Mine: thread.row.actions") || !h.tt.HasText("Snooze") {
		t.Errorf("yours drew the row's actions without threads:write: %q", h.tt.Texts())
	}
	if err := h.a.plugins.SetGranted("mine", "threads:write", true); err != nil {
		t.Fatal(err)
	}
	settlePlugins(t, h.a, h.tt, h.p)
	h.hover(th.View.Title)
	settlePlugins(t, h.a, h.tt, h.p)
	if !h.tt.HasText("Mine: thread.row.actions") || h.tt.HasText("Snooze") {
		t.Errorf("yours doesn't replace the row's actions: %q", h.tt.Texts())
	}

	writeOwnPlugin(t, h.a, "mine", manifest, brokenModule)
	settlePlugins(t, h.a, h.tt, h.p)
	h.hover(th.View.Title)
	settlePlugins(t, h.a, h.tt, h.p)
	if !h.tt.HasText("Snooze") || !h.tt.HasText("All") {
		t.Fatalf("a broken replacement left no built-ins: %q", h.tt.Texts())
	}
}

const moverModule = `async function sip() {
	return (await glitched.threads.list()).find((t) => t.title === "SIP re-INVITE handling")
}
export async function command({ id, args }) {
	const t = await sip()
	const later = new Date(Date.now() + 86400e3).toISOString().replace(/\\.\\d+Z$/, "Z")
	switch (id) {
	case "record":
		await glitched.toast(["record", t.section, t.disposition.state, String(!!t.unread), String(t.requests || 0)].join(" "))
		return
	case "settle":
		await glitched.threads.setDisposition(t.id, { state: "settled" }, { animation: "settle" })
		await glitched.threads.markRead(t.id)
		return
	case "snooze":
		await glitched.threads.setDisposition(t.id, { state: "snoozed", until: later })
		return
	case "nap":
		await glitched.threads.setDisposition(t.id, { state: "snoozed", until: args.until })
		return
	case "back":
		await glitched.threads.setDisposition(t.id, { state: "inbox" }, { animation: "undo" })
		return
	case "wrong": {
		const tries = [
			[{ state: "nope" }, {}],
			[{ state: "inbox" }, { animation: "dance" }],
			[{ state: "snoozed", until: "2001-01-01T00:00:00Z" }, {}],
			[{ state: "snoozed", until: "tomorrow" }, {}],
			[{ state: "inbox" }, { animation: "settle" }],
			[{ state: "settled" }, { animation: "snooze" }],
		]
		for (const [d, o] of tries) {
			const took = await glitched.threads.setDisposition(t.id, d, o).then(() => true, () => false)
			if (took) throw new Error("took " + JSON.stringify(d) + " " + JSON.stringify(o))
		}
		return
	}
	case "filter": {
		const ps = await glitched.projects.list()
		const mygo = ps.find((p) => p.name === "mygo")
		if (await glitched.projects.filter(["nowhere:/x"]).then(() => true, () => false)) throw new Error("filtered to no project")
		await glitched.projects.filter([mygo.key])
		return
	}
	}
}
export async function update({ state, event }) {
	if (event.kind === "app" && event.type === "thread.moved") await glitched.toast("moved " + event.data.from + " to " + event.data.to)
	return { state }
}`

// A plugin files a thread as the row's own actions do, with their jokes,
// and hears where threads move; what it gets wrong is refused.
func TestAPluginFilesThreadsAndHearsThemMove(t *testing.T) {
	dir := tempDir(t)
	inbox(t, dir)
	h := start(t, dir)
	th := h.thread("SIP re-INVITE handling")
	h.tt.Advance(time.Second)
	writeOwnPlugin(t, h.a, "mover", `{"id": "mover", "name": "Mover", "main": "index.js", "contract": "glitched", "contractVersion": 2,
		"capabilities": ["threads:read", "threads:write", "ui.place"],
		"contributes": {"commands": [{"id": "record", "title": "r"}, {"id": "settle", "title": "s"}, {"id": "snooze", "title": "z"}, {"id": "back", "title": "b"}, {"id": "nap", "title": "n"}, {"id": "wrong", "title": "w"}, {"id": "filter", "title": "f"}]},
		"glitched": {"events": ["thread.moved"]}}`, moverModule)
	for _, c := range []string{"threads:read", "threads:write"} {
		if err := h.a.plugins.SetGranted("mover", c, true); err != nil {
			t.Fatal(err)
		}
	}
	settlePlugins(t, h.a, h.tt, h.p)
	run := func(cmd string, args ...map[string]any) {
		t.Helper()
		var failed error
		ran := false
		var a map[string]any
		if len(args) > 0 {
			a = args[0]
		}
		h.a.plugins.RunCommand("mover", cmd, a, func(err error) { failed, ran = err, true })
		h.until(cmd+" to run", func() bool { return ran })
		settlePlugins(t, h.a, h.tt, h.p)
		if failed != nil {
			t.Fatalf("%s: %v", cmd, failed)
		}
	}
	shows := func(s string) bool { return strings.Contains(strings.Join(h.tt.Texts(), "|"), s) }

	run("record")
	if !shows("record inbox inbox") {
		t.Errorf("the record doesn't say it's in the inbox: %q", h.tt.Texts())
	}
	run("wrong")
	run("settle")
	h.until("the engine to settle it", func() bool { return th.View.Disposition.State == api.Settled && len(th.Pending) == 0 })
	h.tt.Advance(100 * time.Millisecond)
	if _, ok := h.a.rowFx[th.id()]; !ok || !shows("Settled. Phew.") {
		t.Errorf("settling played no joke: %q", h.tt.Texts())
	}
	h.tt.Advance(3 * time.Second)
	settlePlugins(t, h.a, h.tt, h.p)
	if !shows("moved inbox to settled") {
		t.Errorf("the plugin didn't hear the thread move: %q", h.tt.Texts())
	}
	run("snooze")
	h.until("the engine to snooze it", func() bool { return th.View.Disposition.State == api.Snoozed && len(th.Pending) == 0 })
	if _, ok := h.a.rowFx[th.id()]; ok {
		t.Error("a snooze without an animation played one")
	}
	run("back")
	h.until("the engine to bring it back", func() bool { return th.View.Disposition.State == api.Open && len(th.Pending) == 0 })
	run("filter")
	if len(h.a.projectFilter) != 1 {
		t.Errorf("the filter is %v, want mygo alone", h.a.projectFilter)
	}

	// A snooze running out is a move no engine change tells of.
	until := h.a.now.Add(30 * time.Second).UTC().Truncate(time.Second).Add(time.Second)
	run("nap", map[string]any{"until": until.Format(time.RFC3339)})
	h.until("the engine to snooze it", func() bool { return th.View.Disposition.State == api.Snoozed && len(th.Pending) == 0 })
	h.tt.Advance(3 * time.Second)
	settlePlugins(t, h.a, h.tt, h.p)
	for range 40 {
		h.tt.Advance(time.Second)
		h.p.run()
	}
	settlePlugins(t, h.a, h.tt, h.p)
	if !shows("moved snoozed to inbox") {
		t.Errorf("the plugin didn't hear the snooze run out: %q", h.tt.Texts())
	}
}

// A plugin that can't read threads is told neither where a row is nor
// which projects there are, nor may it pick which the sidebar shows.
func TestAPluginThatCantReadThreadsIsNotToldWhereItsRowIs(t *testing.T) {
	dir := tempDir(t)
	inbox(t, dir)
	h := start(t, dir)
	th := h.thread("SIP re-INVITE handling")
	h.tt.Advance(time.Second)
	var mygo string
	for _, p := range h.a.projects {
		if p.name() == "mygo" {
			mygo = p.key.String()
		}
	}
	writeOwnPlugin(t, h.a, "blind", `{"id": "blind", "name": "Blind", "main": "index.js", "contract": "glitched", "contractVersion": 2,
		"capabilities": ["ui.place", "ui.replace", "threads:write"], "contributes": {"commands": [{"id": "filter", "title": "f"}]},
		"glitched": {"places": [{"id": "a", "at": "thread.row.actions", "replaces": true}]}}`,
		`export function place({ items }) { return items.map((it) => ({ type: "text", text: "section=" + it.context.section })) }
export async function command() {
	await glitched.projects.filter(["`+mygo+`"]).then(() => glitched.toast("filtered"), (e) => glitched.toast("refused"))
}`)
	for _, c := range []string{"ui.replace", "threads:write"} {
		if err := h.a.plugins.SetGranted("blind", c, true); err != nil {
			t.Fatal(err)
		}
	}
	settlePlugins(t, h.a, h.tt, h.p)
	h.hover(th.View.Title)
	settlePlugins(t, h.a, h.tt, h.p)
	if !slices.Contains(h.tt.Texts(), "section=") {
		t.Errorf("the row was told its section: %q", h.tt.Texts())
	}
	ran := false
	h.a.plugins.RunCommand("blind", "filter", nil, func(error) { ran = true })
	h.until("the command to run", func() bool { return ran })
	settlePlugins(t, h.a, h.tt, h.p)
	if !h.tt.HasText("refused") || len(h.a.projectFilter) != 0 {
		t.Errorf("a plugin that can't read threads filtered the projects to %v: %q", h.a.projectFilter, h.tt.Texts())
	}
}
