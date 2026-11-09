package plugins

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

func TestTheDatabaseRefusesWritingElsewhereHoweverItIsSpelled(t *testing.T) {
	for _, q := range []string{
		"VACUUM INTO '/tmp/x.db'", "VACUUM  INTO '/tmp/x.db'", "VACUUM\nINTO '/tmp/x.db'", "VACUUM/**/INTO '/tmp/x.db'",
		"vacuum main into '/tmp/x.db'", "ATTACH/* */DATABASE '/tmp/x.db' AS x", "pragma max_page_count = 1e9", "SELECT load_extension('x')",
	} {
		if refused(q) == nil {
			t.Errorf("%q ran", q)
		}
	}
	for _, q := range []string{"SELECT 1", "CREATE TABLE t (a INTEGER) -- a comment", "VACUUM"} {
		if err := refused(q); err != nil {
			t.Errorf("%q: %v", q, err)
		}
	}
}

func TestAServiceDoesntRunTillItsGranted(t *testing.T) {
	state := t.TempDir()
	writeService(t, state, "svc", `"capabilities": ["service", "clipboard"], "glitched": { "service": "service.js", "timers": [{ "id": "t", "every": 30 }] }, `,
		`export function view() {}`, `
		await glitched.db.exec("CREATE TABLE ran (a INTEGER)")
		export function timer() { return { effects: [{ type: "clipboard.write", text: "x" }] } }`)
	var ran atomicCount
	s := StartServices(state, ServiceOptions{Logf: t.Logf, TimerScale: 0.002, Do: func(string, Effect) (any, error) { ran.add(); return nil, nil }})
	defer s.Close()
	time.Sleep(400 * time.Millisecond)
	if ran.get() != 0 {
		t.Fatal("a service's timer ran without its grant")
	}
	if _, err := os.Stat(filepath.Join(DataDir(state), "svc", "plugin.db")); err == nil {
		t.Fatal("a service's code ran without its grant")
	}
}

type atomicCount struct {
	mu sync.Mutex
	n  int
}

func (c *atomicCount) add()     { c.mu.Lock(); c.n++; c.mu.Unlock() }
func (c *atomicCount) get() int { c.mu.Lock(); defer c.mu.Unlock(); return c.n }

func TestAFailedPlaceDrawKeepsWhatTheLastOneSaid(t *testing.T) {
	state, u, fx := t.TempDir(), newUIThread(), &effects{}
	writeModule(t, state, "flaky", `"capabilities": ["ui.place"], "glitched": { "places": [{ "id": "p", "at": "sidebar.footer" }] }, `, `
		export function init() { return { state: 0 } }
		export function update({ state }) { return { state: state + 1 } }
		export function place({ state, items }) {
			if (state > 0) throw new Error("not now")
			return items.map(() => ({ type: "input", id: "in", value: "", controlled: true, onChange: "typed" }))
		}`)
	h := open(t, state, u, fx)
	app := h.App("flaky")
	app.SetItems("p", []PlaceItem{{Key: "", Version: "1"}})
	u.until(t, "the first drawing", func() bool { return app.PlaceView("p", "") != nil })
	rev, processed := app.Rev, app.Processed
	app.Dispatch(UIEvent{Type: "change", Handler: "typed", Node: "in", Value: "hi", Place: &PlaceRef{ID: "p", At: "sidebar.footer"}}, nil)
	u.until(t, "the failed draw", func() bool { return app.Err != nil })
	if app.Rev != rev || app.Processed != processed || app.PlaceView("p", "") == nil {
		t.Fatalf("a failed draw moved rev %d→%d, processed %d→%d", rev, app.Rev, processed, app.Processed)
	}
}

func TestASaveAsInitFailsStartsTheNewCode(t *testing.T) {
	state, u, fx := t.TempDir(), newUIThread(), &effects{}
	writeModule(t, state, "racy", "", `
		export function init() { const until = Date.now() + 400; while (Date.now() < until) {} throw new Error("old code") }
		export function view() { return { body: { type: "text", text: "never" } } }`)
	h := open(t, state, u, fx)
	stop := h.Watch(20 * time.Millisecond)
	defer stop()
	s := session(t, h, "racy")
	time.Sleep(100 * time.Millisecond) // the old init runs
	os.WriteFile(filepath.Join(Dir(state), "racy", "index.js"), []byte(`
		export function init() { return { state: 2 } }
		export function view({ state }) { return { body: { type: "text", text: "new " + state } } }`), 0o644)
	u.until(t, "the new code's start", func() bool { return strings.Contains(texts(s), "new 2") })
}

// SQLite itself refuses what would reach past the plugin's own file, past
// any filter on the statement's text.
func TestTheDatabasesAuthorizerKeepsItToItsFile(t *testing.T) {
	dir, other := t.TempDir(), t.TempDir()
	d := &pluginDB{path: filepath.Join(dir, "plugin.db")}
	defer d.close()
	db, err := d.open()
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"ATTACH DATABASE '" + filepath.Join(other, "a.db") + "' AS x",
		"VACUUM INTO '" + filepath.Join(other, "v.db") + "'",
		"PRAGMA max_page_count = 1000000",
		"SELECT load_extension('x')",
	} {
		if _, err := db.Exec(q); err == nil {
			t.Errorf("%q ran", q)
		}
	}
	if entries, _ := os.ReadDir(other); len(entries) != 0 {
		t.Fatalf("wrote %v elsewhere", entries)
	}
	if _, err := db.Exec("CREATE TABLE t (a INTEGER); INSERT INTO t VALUES (1)"); err != nil {
		t.Fatal(err)
	}
	// Through glitched.db, a quoted comment opener hides nothing.
	for _, q := range []string{
		"SELECT '/*'; ATTACH DATABASE '" + filepath.Join(other, "s.db") + "' AS pwn",
		`SELECT "/*" AS x; ATTACH DATABASE '` + filepath.Join(other, "s.db") + "' AS pwn; SELECT '*/'",
		"SELECT '/*'; VACUUM INTO '" + filepath.Join(other, "s.db") + "'",
	} {
		raw, _ := json.Marshal(map[string]string{"sql": q})
		if _, err := d.exec(context.Background(), raw); err == nil {
			t.Errorf("%q ran", q)
		}
	}
	if entries, _ := os.ReadDir(other); len(entries) != 0 {
		t.Fatalf("wrote %v elsewhere", entries)
	}
}

func TestAServiceThatIsItsMainModuleWaitsForItsGrant(t *testing.T) {
	state := t.TempDir()
	writeModule(t, state, "one", `"capabilities": ["service", "tools"], "glitched": { "service": "./index.js", "tools": [{ "name": "ping", "description": "Pings." }] }, `, `
		await glitched.db.exec("CREATE TABLE IF NOT EXISTS ran (a INTEGER)")
		export function tool() { return 1 }
		export function view() {}`)
	s := StartServices(state, ServiceOptions{Logf: t.Logf})
	defer s.Close()
	if _, err := s.CallTool(context.Background(), ToolCall{Plugin: "one", Name: "ping"}); err == nil {
		t.Fatal("its tool ran without the service grant")
	}
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(DataDir(state), "one", "plugin.db")); err == nil {
		t.Fatal("its code ran without the service grant")
	}
	grant(t, state, `{"grants": {"one": ["service"]}}`)
	eventually(t, "its tool once granted", func() bool {
		_, err := s.CallTool(context.Background(), ToolCall{Plugin: "one", Name: "ping"})
		return err == nil
	})
}
