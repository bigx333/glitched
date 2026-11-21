package main

import (
	"fmt"
	"testing"
	"time"

	"glitched/internal/daemon"
	"glitched/internal/provider/fake"
)

const agentsManifest = `{"id": "mine", "name": "mine", "main": "index.js", "contract": "glitched", "contractVersion": 2,
	"capabilities": ["ui.place", "ui.replace", "threads:read"], "glitched": {"places": [{"id": "body", "at": "agents.body", "replaces": true}]}}`

// agentsTree is the app showing a thread with n children, its agents
// panel open.
func agentsTree(t *testing.T, n int) (*harness, thread) {
	t.Helper()
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(false)))
	top := seed(t, d, "/work/t3code", "Ship the sidebar sort")
	for i := range n {
		seedChild(t, d, top, fmt.Sprintf("Child %d", i))
	}
	h := start(t, dir)
	parent := h.thread("Ship the sidebar sort")
	h.until("the children", func() bool { return len(h.a.shown(parent)) == n })
	h.a.select_(parent)
	h.tt.Advance(time.Second)
	h.click("Agents")
	h.tt.Advance(time.Second)
	return h, parent
}

// The agents panel's rows are the built-in Agents panel plugin's, till a
// plugin of yours replaces them; yours failing brings the built-in back,
// and with the built-in off Glitched draws its own.
func TestTheAgentsPanelIsABuiltInPluginYoursReplaces(t *testing.T) {
	h, parent := agentsTree(t, 2)
	shows := func(text string) bool {
		settlePlugins(t, h.a, h.tt, h.p)
		h.tt.Advance(time.Second)
		_, ok := h.tt.Find(text)
		return ok
	}
	builtin := func() bool {
		app := h.a.plugins.App("agents")
		return shows("Child 1") && app != nil && app.PlaceView("body", parent.id().String()) != nil
	}
	if !builtin() {
		t.Fatalf("the built-in doesn't draw the agents panel's rows: %q", h.tt.Texts())
	}
	expectBuiltinFailures(t)
	writeOwnPlugin(t, h.a, "mine", agentsManifest, mineModule)
	h.a.plugins.SetGranted("mine", "ui.replace", true)
	h.a.plugins.SetGranted("mine", "threads:read", true)
	if !shows("Mine") {
		t.Fatalf("yours doesn't replace the built-in: %q", h.tt.Texts())
	}
	if _, ok := h.tt.Find("Child 1"); ok {
		t.Fatalf("yours doesn't replace the built-in: %q", h.tt.Texts())
	}
	writeOwnPlugin(t, h.a, "mine", agentsManifest, brokenModule)
	if !builtin() {
		t.Fatalf("yours that throws left no built-in: %q", h.tt.Texts())
	}
	h.a.plugins.SetEnabled("mine", false)
	if err := h.a.plugins.SetEnabled("agents", false); err != nil {
		t.Fatal(err)
	}
	h.a.pluginsChanged(nil)
	if !shows("Child 1") || len(h.a.plugins.Replacers("agents.body")) > 0 {
		t.Fatalf("with no plugin to draw them, Glitched's own rows don't show: %q", h.tt.Texts())
	}
}

// The built-in's rows do what the panel's own did: past five, More shows
// every one; pointed at, a row's age gives way to … for its menu; a click
// shows its thread.
func TestTheAgentsPanelsRowsKeepTheirWays(t *testing.T) {
	h, _ := agentsTree(t, 7)
	has := func(text string) bool { _, ok := h.tt.Find(text); return ok }
	settlePlugins(t, h.a, h.tt, h.p)
	h.tt.Advance(time.Second)
	if !has("Child 2") || has("Child 1") || !has("More") {
		t.Fatalf("the panel doesn't show five, the newest first, and More: %q", h.tt.Texts())
	}
	h.click("More")
	settlePlugins(t, h.a, h.tt, h.p)
	h.tt.Advance(time.Second)
	if !has("Child 0") || has("More") {
		t.Fatalf("More doesn't show every child: %q", h.tt.Texts())
	}
	h.hover("Child 3")
	h.tt.Frame()
	h.tt.Frame()
	if !has("Thread actions") {
		t.Fatalf("pointed at, a row has no menu: %q", h.tt.Texts())
	}
	h.click("Child 3")
	h.tt.Advance(time.Second)
	if th, ok := h.a.find(h.a.selected); !ok || th.View.Title != "Child 3" {
		t.Fatalf("a click on a row didn't show its thread")
	}
}
