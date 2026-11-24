package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/daemon"
	"glitched/internal/plugins"
	"glitched/internal/provider"
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
	h.a.plugins.SetGranted("locked-diff", "threads:read", true)
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
	// Its context has the thread's files: without threads:read it doesn't
	// draw the panel.
	settlePlugins(t, h.a, h.tt, h.p)
	if !h.tt.HasText("Changes") || h.tt.HasText("Your header") {
		t.Fatalf("without threads:read the panel shows %q, want the built-in's", h.tt.Texts())
	}
	h.a.plugins.SetGranted("your-diff", "threads:read", true)
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
	// A change's own button marks it as the bar does: the plugin hears it.
	d.Accept(0)
	h.tt.Frame()
	h.until("the change's own mark to reach your plugin", func() bool { return h.tt.HasText(fmt.Sprintf("Yours: %s, %d left", sidebarPath, n-2)) })
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

// One edit is one tell: the plugin hears of it as it finishes, not again
// as it starts, though the transcript shows it under way between the two.
func TestOneEditIsToldOnce(t *testing.T) {
	dir := tempDir(t)
	dst := filepath.Join(plugins.Dir(dir), "file-ears")
	os.MkdirAll(dst, 0o755)
	os.WriteFile(filepath.Join(dst, "plugin.json"), []byte(`{"id": "file-ears", "name": "File ears", "main": "index.js",
		"capabilities": ["ui.place", "threads:read"], "glitched": {"events": ["thread.files.changed"], "places": [{"id": "files", "at": "sidebar.footer"}]}}`), 0o644)
	os.WriteFile(filepath.Join(dst, "index.js"), []byte(`
		export function init() { return { state: [] } }
		export function update({ state, event }) { return event.type === "thread.files.changed" ? { state: [...state, ...event.data.files] } : {} }
		export function place({ state, items }) { return items.map(() => ({ type: "text", text: "edited " + state.length + " " + state.join(" ") })) }`), 0o644)
	signAsOwn(t, dir, dst)
	grant(t, dir, `{"grants": {"file-ears": ["threads:read"]}}`)
	ws := gitWorkspace(t)
	held := []provider.Event{
		provider.Work{Item: "e", Kind: "edit", Title: "Edit " + sidebarPath, Status: api.WorkStarted, Path: sidebarPath},
		provider.RequestOpened{ID: "approve-1", Kind: api.KindApproval, Body: json.RawMessage(`{"operation":"edit","path":"` + sidebarPath + `"}`)},
		provider.Work{Item: "e", Kind: "edit", Title: "Edit " + sidebarPath, Status: api.WorkCompleted, Path: sidebarPath},
		provider.TextDelta{Text: "Edited it."},
	}
	d := engine(t, daemon.Config{Dir: dir}, fake.New(held))
	id := seed(t, d, ws, "Held")
	h := start(t, dir)
	h.a.select_(h.thread("Held"))
	h.until("nothing told of what it had", func() bool { return h.tt.HasText("edited 0 ") })
	ep := d.Endpoint()
	if _, err := ep.Submit(context.Background(), api.Command{ID: api.NewID(), Target: id, Body: api.MessageDispatch{Text: "Edit it."}}); err != nil {
		t.Fatal(err)
	}
	h.until("the edit under way", func() bool {
		fs := changedFiles(entriesOf(h.thread("Held")))
		return len(fs) == 1 && fs[0].edited.IsZero() && h.thread("Held").View.Run == api.RunWaiting
	})
	snap, err := ep.Read(context.Background(), api.Query{Thread: id})
	if err != nil || len(snap.Threads) != 1 || len(snap.Threads[0].Requests) != 1 {
		t.Fatalf("the approval: %+v, %v", snap.Threads, err)
	}
	if _, err := ep.Submit(context.Background(), api.Command{ID: api.NewID(), Target: id, Body: api.RequestRespond{Request: snap.Threads[0].Requests[0].ID, Generation: snap.Threads[0].Requests[0].Generation, Answer: json.RawMessage(`{"allow":true}`)}}); err != nil {
		t.Fatal(err)
	}
	h.until("the run ended with the edit", func() bool {
		fs := changedFiles(entriesOf(h.thread("Held")))
		return len(fs) == 1 && !fs[0].edited.IsZero() && h.thread("Held").View.Run == api.RunIdle
	})
	h.tt.Advance(time.Second)
	if !h.tt.HasText("edited 1 " + sidebarPath) {
		t.Fatalf("the plugin shows %q, want one tell of the one edit", slices.DeleteFunc(h.tt.Texts(), func(s string) bool { return !strings.HasPrefix(s, "edited ") }))
	}
}

// waitingFirstDraws has frames wait up to d for a component's first
// drawing while t runs.
func waitingFirstDraws(t *testing.T, d time.Duration) {
	was := firstDrawWait
	firstDrawWait = d
	t.Cleanup(func() { firstDrawWait = was })
}

// The diff panel is drawn by a plugin, yet the first frame after it opens
// already shows it: a component's first drawing is waited for before it
// shows, never an empty frame. The wait can't run out here, however slow
// the machine.
func TestTheDiffPanelOpensDrawn(t *testing.T) {
	waitingFirstDraws(t, time.Hour)
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

// A first drawing held past the wait leaves the panel empty as it opens,
// after the frame waited for it, and the worker's post of it is what
// fills the panel.
func TestADiffPanelDrawnLateFillsInOnceDrawn(t *testing.T) {
	waitingFirstDraws(t, 100*time.Millisecond)
	h, th := checked(t)
	writeOwnPlugin(t, h.a, "late-diff", `{"id": "late-diff", "name": "Late diff", "main": "index.js", "contract": "glitched", "contractVersion": 2,
		"capabilities": ["ui.place", "ui.replace", "threads:read"], "glitched": {"places": [{"id": "panel", "at": "thread.panel.diff", "replaces": true}]}}`,
		`export function place({ items }) { return items.map(() => ({ type: "text", text: "Late changes" })) }`)
	for _, c := range []string{"ui.replace", "threads:read"} {
		if err := h.a.plugins.SetGranted("late-diff", c, true); err != nil {
			t.Fatal(err)
		}
	}
	h.a.plugins.SetGranted("diff-panel", "ui.replace", false) // the built-in doesn't draw it instead
	h.a.plugins.Reload()
	h.tt.Advance(time.Second)
	// Its drawings wait till the test lets them go.
	gate := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(release)
	t.Cleanup(plugins.HoldPlaceDraws(func(plugin string) {
		if plugin == "late-diff" {
			<-gate
		}
	}))
	h.a.showDiff = true
	start := time.Now()
	h.tt.Frame()
	if took := time.Since(start); took < firstDrawWait {
		t.Fatalf("the opening frame took %v, less than the wait: it didn't wait for the first drawing", took)
	}
	if h.tt.HasText("Late changes") {
		t.Fatalf("the panel opened drawn, though its drawing came after the wait: %q", h.tt.Texts())
	}
	release()
	// The drawing comes in the worker's post, with no frame drawn: the
	// session has it once the post runs.
	key := th.id().String() + "/header"
	deadline := time.After(10 * time.Second)
	for h.a.plugins.Live("late-diff").PlaceView("panel", key) == nil {
		if h.p.run() {
			continue
		}
		select {
		case <-h.p.wake:
		case <-time.After(10 * time.Millisecond):
		case <-deadline:
			t.Fatal("the late drawing was never posted")
		}
	}
	h.tt.Frame()
	if !h.tt.HasText("Late changes") {
		t.Fatalf("the frame after the drawing's post doesn't show it: %q", h.tt.Texts())
	}
}

// A copy your plugin leaves undrawn (null) is waited on once, not on every
// frame after it.
func TestAnUndrawnCopyIsWaitedOnOnce(t *testing.T) {
	a := offline(t)
	writeOwnPlugin(t, a, "blank-actions", replacing("blank-actions", "thread.header.actions"), `export function place({ items }) { return items.map(() => null) }`)
	a.plugins.SetGranted("blank-actions", "ui.replace", true)
	a.plugins.SetGranted("header-actions", "ui.replace", false) // no fallback draws it either
	a.plugins.Reload()
	tt := newTester(t, a, 1320, 860)
	settleTester(tt)
	start := time.Now()
	for range 5 {
		tt.Frame()
	}
	if took := time.Since(start); took > firstDrawWait {
		t.Fatalf("5 frames took %v: the undrawn copy is waited on again", took)
	}
}
