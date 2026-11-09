package plugins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAFailedReloadLeavesTheDatabaseOpen(t *testing.T) {
	state, u, fx := t.TempDir(), newUIThread(), &effects{}
	writeModule(t, state, "keeper", "", `
		export async function init() { await glitched.db.exec("CREATE TABLE IF NOT EXISTS n (v INTEGER)"); return { state: 0 } }
		export async function update({ state }) {
			await glitched.db.exec("INSERT INTO n VALUES (1)")
			const rows = await glitched.db.query("SELECT count(*) AS c FROM n")
			return { state: rows[0].c }
		}
		export function view({ state }) { return { body: { type: "text", text: "rows " + state } } }`)
	h := open(t, state, u, fx)
	stop := h.Watch(20 * time.Millisecond)
	defer stop()
	s := session(t, h, "keeper")
	u.until(t, "the view", func() bool { return strings.Contains(texts(s), "rows 0") })
	time.Sleep(30 * time.Millisecond)
	os.WriteFile(filepath.Join(Dir(state), "keeper", "index.js"), []byte("export function view( {"), 0o644)
	u.until(t, "the failed reload", func() bool { e := h.Installed()[0].Err; return e != nil })
	s.Dispatch(UIEvent{Type: "click", Handler: "add", Node: "add"}, nil)
	u.until(t, "a row added through the kept database", func() bool { return strings.Contains(texts(s), "rows 1") })
}

func TestAPageWhoseInitFailedStartsOnceItsCodeIsFixed(t *testing.T) {
	state, u, fx := t.TempDir(), newUIThread(), &effects{}
	writeModule(t, state, "fixme", "", `
		export function init() { throw new Error("not yet") }
		export function view() { return { body: { type: "text", text: "never" } } }`)
	h := open(t, state, u, fx)
	stop := h.Watch(20 * time.Millisecond)
	defer stop()
	s := session(t, h, "fixme")
	u.until(t, "the failed init", func() bool { return s.Err != nil })
	time.Sleep(30 * time.Millisecond)
	os.WriteFile(filepath.Join(Dir(state), "fixme", "index.js"), []byte(`
		export function init() { return { state: 1 } }
		export function view({ state }) { return { body: { type: "text", text: "fixed " + state } } }`), 0o644)
	u.until(t, "the page started by the fixed code", func() bool { return strings.Contains(texts(s), "fixed 1") && s.Err == nil })
}

func TestATabDrawsAgainAsItsThreadChanges(t *testing.T) {
	state, u, fx := t.TempDir(), newUIThread(), &effects{}
	writeModule(t, state, "tabby", `"capabilities": ["ui.place"], "glitched": { "tabs": [{ "id": "t", "title": "T" }] }, `, `
		export function view({ context }) { return { body: { type: "text", text: "about " + context.title } } }`)
	h := open(t, state, u, fx)
	s, err := h.TabSession("tabby", "t", "th1", ThreadRecord{ID: "th1", Title: "One"})
	if err != nil {
		t.Fatal(err)
	}
	u.until(t, "the tab", func() bool { return strings.Contains(texts(s), "about One") })
	h.TabSession("tabby", "t", "th1", ThreadRecord{ID: "th1", Title: "Uno"})
	u.until(t, "the renamed thread", func() bool { return strings.Contains(texts(s), "about Uno") })
}

func TestChangesInTwoCopiesOfAPlaceBothArrive(t *testing.T) {
	state, u, fx := t.TempDir(), newUIThread(), &effects{}
	writeModule(t, state, "rows", `"capabilities": ["ui.place"], "glitched": { "places": [{ "id": "p", "at": "thread.row" }] }, `, `
		export function init() { return { state: [] } }
		export function place({ items }) { return items.map(() => null) }
		export function update({ state, event }) {
			const until = Date.now() + 30; while (Date.now() < until) {}
			return { state: [...state, event.place.key + "=" + event.value] }
		}
		export function view({ state }) { return { body: { type: "text", text: state.join(",") } } }`)
	h := open(t, state, u, fx)
	app := h.App("rows")
	for i, key := range []string{"a", "b", "a", "b"} {
		v := string(rune('1' + i))
		app.Dispatch(UIEvent{Type: "change", Handler: "h", Node: "in", Value: v, Place: &PlaceRef{ID: "p", At: "thread.row", Key: key}}, nil)
	}
	u.until(t, "the last change of each row", func() bool {
		app.mu.Lock()
		got := string(app.state)
		app.mu.Unlock()
		return strings.Contains(got, "a=3") && strings.Contains(got, "b=4")
	})
	if app.Processed == 0 {
		t.Fatal("a place's drawing doesn't say which events it took")
	}
}

func TestALinkedPluginReloadsAsItsFilesChange(t *testing.T) {
	state, src, u, fx := t.TempDir(), t.TempDir(), newUIThread(), &effects{}
	copyPlugin(t, src, "counter", "counter")
	folder := filepath.Join(Dir(src), "counter")
	signAsOwn(t, state, folder)
	os.MkdirAll(Dir(state), 0o755)
	os.Symlink(folder, filepath.Join(Dir(state), "counter"))
	h := open(t, state, u, fx)
	stop := h.Watch(20 * time.Millisecond)
	defer stop()
	s := session(t, h, "counter")
	u.until(t, "the view", func() bool { return strings.Contains(texts(s), "Count: 0") })
	time.Sleep(30 * time.Millisecond)
	b, _ := os.ReadFile(filepath.Join(folder, "index.ts"))
	os.WriteFile(filepath.Join(folder, "index.ts"), []byte(strings.Replace(string(b), "`Count: ${", "`Linked: ${", 1)), 0o644)
	u.until(t, "the edit through the link", func() bool { return strings.Contains(texts(s), "Linked: 0") })
}
