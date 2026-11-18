package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/daemon"
	"glitched/internal/plugins"
	"glitched/internal/provider/fake"
)

// yourDiffPanel replaces the diff panel with a header of its own and a
// body that embeds the native diff.body for the file shown, and says how
// many changes are left once one is marked reviewed there.
const yourDiffPanel = `export function init() { return { state: { left: null } } }
export function place({ state, items }) {
	return items.map((it) => it.context.part === "header"
		? { type: "text", text: "Your header" }
		: { type: "column", grow: true, children: [
			{ type: "text", id: "shown", text: "Yours: " + it.context.shown + (state.left === null ? "" : ", " + state.left + " left") },
			{ type: "native", id: "diff", name: NATIVE, props: { path: it.context.shown }, grow: true, onChange: "marked" },
		] })
}
export function update({ state, event }) {
	return event.handler === "marked" ? { state: { left: event.value.remaining } } : {}
}`

// A plugin of yours replaces the diff panel and still shows the diff:
// diff.body draws it, whose marks reach the plugin. One that names a
// locked part of the UI as a native widget is refused, and the built-in
// draws the panel.
func TestYourDiffPanelEmbedsTheNativeDiff(t *testing.T) {
	h, th := checked(t)
	h.click("Edit " + sidebarPath)
	h.diffsRead()
	manifest := func(id string) string {
		return strings.Replace(replacing(id, "thread.panel.diff"), `"ui.replace"]`, `"ui.replace", "threads:read"]`, 1)
	}

	writeOwnPlugin(t, h.a, "locked-diff", manifest("locked-diff"), strings.ReplaceAll(yourDiffPanel, "NATIVE", `"approval.card"`))
	h.a.plugins.SetGranted("locked-diff", "ui.replace", true)
	h.a.plugins.Reload()
	h.tt.Frame()
	h.until("the locked native to be refused", func() bool {
		err := h.a.plugins.Failed("locked-diff")
		return err != nil && strings.Contains(err.Error(), "approval.card is off limits to plugins")
	})
	settlePlugins(t, h.a, h.tt, h.p)
	if !h.tt.HasText("Changes") || h.tt.HasText("Your header") {
		t.Fatalf("with yours refused, the panel shows %q, want the built-in's", h.tt.Texts())
	}
	h.a.plugins.SetGranted("locked-diff", "ui.replace", false)

	writeOwnPlugin(t, h.a, "your-diff", manifest("your-diff"), strings.ReplaceAll(yourDiffPanel, "NATIVE", `"diff.body"`))
	h.a.plugins.SetGranted("your-diff", "ui.replace", true)
	h.a.plugins.Reload()
	h.tt.Frame()
	h.until("your panel", func() bool { return h.tt.HasText("Yours: " + sidebarPath) })
	if h.tt.HasText("Changes") || !h.tt.HasText("Your header") {
		t.Errorf("the panel shows %q, want yours in place of the built-in", h.tt.Texts())
	}
	d := h.diffOf(th, sidebarPath)
	n := len(d.Hunks())
	if _, ok := h.tt.Find("Mark change reviewed"); !ok || n == 0 {
		t.Fatalf("your panel has no native diff: %q", h.tt.Texts())
	}
	h.click("Mark change reviewed")
	h.until("the mark to reach your plugin", func() bool { return h.tt.HasText(fmt.Sprintf("Yours: %s, %d left", sidebarPath, n-1)) })
}

// The diff calls reach only the files the thread changed.
func TestTheDiffCallsReachOnlyTheThreadsFiles(t *testing.T) {
	h, th := checked(t)
	h.click("Edit " + sidebarPath)
	h.diffsRead()
	call := func(typ, args string) error {
		_, err := h.a.diffEffect(typ, json.RawMessage(args))
		return err
	}
	id := th.id().String()
	for typ, args := range map[string]string{
		"diff.setReviewed": `{"thread": "` + id + `", "path": "../../etc/passwd"}`,
		"diff.openFile":    `{"thread": "` + id + `", "path": "/etc/passwd"}`,
		"diff.set":         `{"thread": "` + id + `", "shown": "README.md"}`,
	} {
		if err := call(typ, args); err == nil || !strings.Contains(err.Error(), "the thread changed no file") {
			t.Errorf("%s %s: %v", typ, args, err)
		}
	}
	if err := call("diff.set", `{"thread": "nope", "wrap": false}`); err == nil {
		t.Error("diff.set took a thread that isn't one")
	}
	if err := call("diff.setReviewed", `{"thread": "`+id+`", "path": "`+sidebarPath+`", "reviewed": true}`); err != nil || !h.diffOf(th, sidebarPath).reviewed {
		t.Errorf("marking the file reviewed: %v", err)
	}
}

// A plugin that hears thread.files.changed is told of each edit to the
// files of the thread shown, and not of those it had when first shown.
func TestEditsToTheThreadsFilesAreTold(t *testing.T) {
	dir := tempDir(t)
	dst := filepath.Join(plugins.Dir(dir), "file-ears")
	os.MkdirAll(dst, 0o755)
	os.WriteFile(filepath.Join(dst, "plugin.json"), []byte(`{"id": "file-ears", "name": "File ears", "main": "index.js",
		"capabilities": ["ui.place", "threads:read"], "glitched": {"events": ["thread.files.changed"], "places": [{"id": "files", "at": "sidebar.footer"}]}}`), 0o644)
	os.WriteFile(filepath.Join(dst, "index.js"), []byte(`
		export function init() { return { state: [] } }
		export function update({ state, event }) { return event.type === "thread.files.changed" ? { state: [...state, ...event.data.files] } : {} }
		export function place({ state, items }) { return items.map(() => ({ type: "text", text: "edited " + state.length + " " + [...new Set(state)].sort().join(" ") })) }`), 0o644)
	signAsOwn(t, dir, dst)
	grant(t, dir, `{"grants": {"file-ears": ["threads:read"]}}`)
	ws := gitWorkspace(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(twoEdits))
	id := seed(t, d, ws, "Both", "Edit both.")
	h := start(t, dir)
	th := h.thread("Both")
	h.a.select_(th)
	h.until("its transcript", func() bool { return len(changedFiles(entriesOf(h.thread("Both")))) == 2 })
	h.until("nothing told of what it had", func() bool { return h.tt.HasText("edited 0 ") })
	if _, err := d.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: id, Body: api.MessageDispatch{Text: "Edit both again."}}); err != nil {
		t.Fatal(err)
	}
	h.until("the edits told", func() bool {
		return slices.ContainsFunc(h.tt.Texts(), func(s string) bool { return strings.HasSuffix(s, " README.md "+sidebarPath) })
	})
}

// The diff panel is drawn by a plugin, yet the first frame after it opens
// already shows it: a component's first drawing is waited for before it
// shows, never an empty frame.
func TestTheDiffPanelOpensDrawn(t *testing.T) {
	h, _ := checked(t)
	if h.a.showDiff {
		t.Fatal("the panel is open before it was asked for")
	}
	h.a.showDiff = true
	start := time.Now()
	h.tt.Frame()
	took := time.Since(start)
	if !h.tt.HasText("Changes") || !h.tt.HasText("Unified") {
		t.Fatalf("the first frame after opening shows %q, want the panel's header", h.tt.Texts())
	}
	if _, ok := h.tt.Find("Diff of " + sidebarPath); !ok {
		t.Fatalf("the first frame after opening has no file list: %q", h.tt.Texts())
	}
	t.Logf("the opening frame took %v", took)
}
