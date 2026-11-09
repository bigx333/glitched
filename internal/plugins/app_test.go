package plugins

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const badgeModule = `
export function init() { return { state: { changes: 0, ticks: 0, clicks: 0 } } }
export function place({ id, at, state, items }) {
	host.storage.set("calls", (host.storage.get("calls") ?? 0) + 1)
	return items.map((it) => it.context.status === "hidden" ? null : {
		type: "row", children: [
			{ type: "badge", text: (it.context.title ?? "?") + " " + state.changes + "/" + state.ticks + "/" + state.clicks },
			{ type: "button", id: "b", label: "Poke", onClick: { handler: "poke", payload: it.key } },
		] })
}
export function update({ state, event }) {
	if (event.kind === "timer") return { state: { ...state, ticks: state.ticks + 1 } }
	if (event.kind === "app" && event.type === "thread.changed") return { state: { ...state, changes: state.changes + 1 } }
	if (event.kind === "app" && event.type === "setting.changed") return { state: { ...state, changes: state.changes + 100 } }
	if (event.kind === "ui" && event.place && event.place.key === event.payload) return { state: { ...state, clicks: state.clicks + 1 } }
	return {}
}`

func badgeManifest(caps string) string {
	return `"capabilities": [` + caps + `], "glitched": {
		"places": [{ "id": "badge", "at": "thread.row" }],
		"events": ["thread.changed", "setting.changed"],
		"timers": [{ "id": "tick", "every": 30 }],
		"settings": [{ "id": "show", "kind": "switch", "label": "Show", "default": true },
			{ "id": "size", "kind": "number", "label": "Size", "min": 1, "max": 10, "default": 3 },
			{ "id": "mode", "kind": "choice", "label": "Mode", "options": [{ "value": "a", "label": "A" }, { "value": "b", "label": "B" }] }]
	}, `
}

func badgeText(s *Session, key string) string {
	n := s.PlaceView("badge", key)
	if n == nil {
		return ""
	}
	return n.Children[0].Text
}

func TestPlacesDrawEachCopyAndKeepItWhileTheNextIsDrawn(t *testing.T) {
	state, u, fx := t.TempDir(), newUIThread(), &effects{}
	writeModule(t, state, "badges", badgeManifest(`"ui.place", "threads:read"`), badgeModule)
	h := Open(state, Options{Post: u.post, Do: fx.do, Logf: t.Logf, CallTimeout: 2 * time.Second, TimerScale: 0.002})
	t.Cleanup(h.Close)
	if err := h.SetGranted("badges", "threads:read", true); err != nil {
		t.Fatal(err)
	}
	s := h.App("badges")
	if s == nil {
		t.Fatal("no app session")
	}
	items := []PlaceItem{
		{Key: "t1", Version: "1", Context: ThreadRecord{ID: "t1", Title: "One"}},
		{Key: "t2", Version: "1", Context: ThreadRecord{ID: "t2", Title: "Two"}},
		{Key: "t3", Version: "1", Context: map[string]string{"id": "t3", "status": "hidden"}},
	}
	s.SetItems("badge", items)
	u.until(t, "both badges", func() bool {
		return strings.HasPrefix(badgeText(s, "t1"), "One ") && strings.HasPrefix(badgeText(s, "t2"), "Two ")
	})
	if s.PlaceView("badge", "t3") != nil {
		t.Fatal("a null copy drew")
	}
	before := s.Rev
	s.SetItems("badge", items) // the same: nothing to draw
	time.Sleep(100 * time.Millisecond)
	for len(u.q) > 0 {
		(<-u.q)()
	}
	if s.Rev != before {
		t.Fatal("drew again for the same items")
	}
	items[0].Version, items[0].Context = "2", ThreadRecord{ID: "t1", Title: "Uno"}
	s.SetItems("badge", items)
	u.until(t, "the new title", func() bool { return strings.HasPrefix(badgeText(s, "t1"), "Uno ") })

	h.Emit("thread.changed", func(string) any { return ThreadRecord{ID: "t1"} })
	u.until(t, "the event", func() bool { return strings.HasPrefix(badgeText(s, "t1"), "Uno 1/") })
	u.until(t, "a timer", func() bool { return !strings.Contains(badgeText(s, "t1"), "/0/") })

	s.Dispatch(UIEvent{Type: "click", Handler: "poke", Node: "b", Payload: json.RawMessage(`"t2"`), Place: &PlaceRef{ID: "badge", At: "thread.row", Key: "t2"}}, nil)
	u.until(t, "the click", func() bool { return strings.HasSuffix(badgeText(s, "t2"), "/1") })
}

func TestEventsNeedTheirCapability(t *testing.T) {
	state, u, fx := t.TempDir(), newUIThread(), &effects{}
	writeModule(t, state, "nosy", `"capabilities": ["threads:read"], "glitched": { "events": ["thread.changed"] }, `, `
		export function init() { return { state: 0 } }
		export function update({ state }) { host.storage.set("heard", state + 1); return { state: state + 1 } }`)
	h := Open(state, Options{Post: u.post, Do: fx.do, Logf: t.Logf})
	t.Cleanup(h.Close)
	var told atomic.Int32
	h.Emit("thread.changed", func(string) any { told.Add(1); return ThreadRecord{ID: "t"} })
	if told.Load() != 0 {
		t.Fatal("a plugin without the grant was told")
	}
	if err := h.SetGranted("nosy", "threads:read", true); err != nil {
		t.Fatal(err)
	}
	h.Emit("thread.changed", func(string) any { told.Add(1); return ThreadRecord{ID: "t"} })
	if told.Load() != 1 {
		t.Fatal("a granted plugin wasn't told")
	}
	if r := h.Record("nosy", ThreadRecord{ID: "t", Title: "secret"}); r.Title != "secret" {
		t.Fatalf("record %+v", r)
	}
	h.SetGranted("nosy", "threads:read", false)
	if r := h.Record("nosy", ThreadRecord{ID: "t", Title: "secret"}); r.Title != "" || r.ID != "t" {
		t.Fatalf("a record without the grant: %+v", r)
	}
}

func TestSettingsReadAsDeclaredAndTellThePlugin(t *testing.T) {
	state, u, fx := t.TempDir(), newUIThread(), &effects{}
	writeModule(t, state, "badges", badgeManifest(`"ui.place", "threads:read"`), badgeModule+`
		export function view() { return { body: { type: "text", text: host.setting("show") + " " + host.setting("size") + " " + host.setting("mode") } } }`)
	h := Open(state, Options{Post: u.post, Do: fx.do, Logf: t.Logf})
	t.Cleanup(h.Close)
	got := h.SettingValues("badges")
	if got["show"] != true || got["size"] != 3.0 || got["mode"] != "a" {
		t.Fatalf("defaults %v", got)
	}
	if err := h.SetSetting("badges", "size", 99); err != nil {
		t.Fatal(err)
	}
	if err := h.SetSetting("badges", "mode", "c"); err == nil {
		t.Fatal("an option that isn't one took")
	}
	if err := h.SetSetting("badges", "nope", 1); err == nil {
		t.Fatal("a setting not declared took")
	}
	h.SetSetting("badges", "show", false)
	if got := h.SettingValues("badges"); got["size"] != 10.0 || got["show"] != false {
		t.Fatalf("after setting: %v", got)
	}
	s := session(t, h, "badges")
	u.until(t, "host.setting", func() bool { return strings.Contains(texts(s), "false 10 a") })
	app := h.App("badges")
	app.SetItems("badge", []PlaceItem{{Key: "k", Version: "1", Context: map[string]string{}}})
	u.until(t, "the badge", func() bool { return badgeText(app, "k") == "? 200/0/0" }) // the two settings set before: the plugin heard them
	h.SetSetting("badges", "size", 4)
	u.until(t, "setting.changed", func() bool { return badgeText(app, "k") == "? 300/0/0" })
	if err := h.setStatus("badges", json.RawMessage(`{"setting": "show", "status": {"badge": "Set up", "tone": "warning", "text": "Pick one."}}`)); err != nil {
		t.Fatal(err)
	}
	if st := h.Settings("badges")[0].Status; st == nil || st.Badge != "Set up" {
		t.Fatalf("status %+v", st)
	}
	if err := h.setStatus("badges", json.RawMessage(`{"setting": "show", "status": {"badge": "x", "tone": "pink"}}`)); err == nil {
		t.Fatal("a tone that isn't one took")
	}
	if Open(state, Options{}).SettingValues("badges")["size"] != 4.0 {
		t.Fatal("a setting didn't outlast a restart")
	}
}

func TestTheGlitchedBlockIsChecked(t *testing.T) {
	cases := map[string]string{
		`"capabilities": [], "glitched": { "places": [{ "id": "x", "at": "thread.row" }] }, `:                `needs the "ui.place" capability`,
		`"capabilities": ["ui.place"], "glitched": { "places": [{ "id": "x", "at": "nowhere" }] }, `:         `no slot "nowhere"`,
		`"glitched": { "timers": [{ "id": "t", "every": 5 }] }, `:                                            "under the 30 s allowed",
		`"glitched": { "events": ["keystrokes"] }, `:                                                         `no event "keystrokes"`,
		`"glitched": { "settings": [{ "id": "s", "kind": "slider", "label": "S" }] }, `:                      `kind "slider"`,
		`"glitched": { "settings": [{ "id": "s", "kind": "choice", "label": "S" }] }, `:                      "a choice needs options",
		`"capabilities": ["tools"], "glitched": { "tools": [{ "name": "Bad-Name", "description": "d" }] }, `: "is a lowercase letter",
		`"capabilities": ["programs"], "glitched": { "programs": ["/bin/sh"] }, `:                            "never a path",
		`"capabilities": ["service"], "glitched": { "service": "../escape.js" }, `:                           "a file in the plugin's folder",
		`"glitched": { "shortcuts": [{ "command": "c", "key": "k" }] }, `:                                    "needs cmd, ctrl or alt",
	}
	for manifest, want := range cases {
		state := t.TempDir()
		writeModule(t, state, "x", manifest, `export function view() {}`)
		h := Open(state, Options{Logf: t.Logf})
		err := h.Installed()[0].Err
		h.Close()
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", manifest, err, want)
		}
	}
}

func TestEachPluginHasADatabaseOfItsOwn(t *testing.T) {
	state, u, fx := t.TempDir(), newUIThread(), &effects{}
	module := `
		export async function init() {
			await glitched.db.exec("CREATE TABLE IF NOT EXISTS seen (url TEXT PRIMARY KEY, n INTEGER)")
			await glitched.db.exec("INSERT INTO seen VALUES (?, ?) ON CONFLICT(url) DO UPDATE SET n = n + 1", ["a", 1])
			const rows = await glitched.db.query("SELECT url, n FROM seen WHERE url = ?", ["a"])
			let refused = ""
			try { await glitched.db.exec("ATTACH DATABASE '/tmp/x.db' AS x") } catch (e) { refused = e.message }
			return { state: { rows, refused } }
		}
		export function view({ state }) { return { body: { type: "text", text: JSON.stringify(state) } } }`
	writeModule(t, state, "one", "", module)
	writeModule(t, state, "two", "", module)
	h := open(t, state, u, fx)
	one, two := session(t, h, "one"), session(t, h, "two")
	u.until(t, "both databases", func() bool {
		return strings.Contains(texts(one), `"n":1`) && strings.Contains(texts(two), `"n":1`)
	})
	if !strings.Contains(texts(one), "refuses ATTACH") {
		t.Fatalf("attach: %s", texts(one))
	}
	one.Retry()
	u.until(t, "one's second start", func() bool { return strings.Contains(texts(one), `"n":2`) })
	if !strings.Contains(texts(two), `"n":1`) {
		t.Fatal("two saw one's rows")
	}
}

func TestRunAndServiceNeedTheirGrants(t *testing.T) {
	state, u, fx := t.TempDir(), newUIThread(), &effects{}
	writeModule(t, state, "worker", `"capabilities": ["programs", "service"], "glitched": { "programs": ["git"], "service": "index.js" }, `, `
		export async function init() {
			const out = []
			for (const f of [() => glitched.run("git", ["--version"]), () => glitched.service.call("ping")]) {
				try { out.push(await f()) } catch (e) { out.push(e.code + ": " + e.message) }
			}
			return { state: out }
		}
		export function view({ state }) { return { body: { type: "text", text: state.join(" | ") } } }`)
	var ran []string
	h := Open(state, Options{Post: u.post, Do: fx.do, Logf: t.Logf,
		Run: func(_ context.Context, id string, allowed []string, args json.RawMessage) (any, error) {
			ran = append(ran, id+" "+strings.Join(allowed, ","))
			return map[string]any{"status": 0}, nil
		},
		ServiceCall: func(_ context.Context, id, method string, _ json.RawMessage) (any, error) {
			return id + "." + method, nil
		},
	})
	t.Cleanup(h.Close)
	s := session(t, h, "worker")
	u.until(t, "both denied", func() bool { return strings.Count(texts(s), "denied: permission denied") == 2 })
	h.SetGranted("worker", "programs", true)
	h.SetGranted("worker", "service", true)
	s = session(t, h, "worker")
	u.until(t, "both run", func() bool { return strings.Contains(texts(s), "worker.ping") })
	if len(ran) != 1 || ran[0] != "worker git" {
		t.Fatalf("ran %q", ran)
	}
}

func TestAPluginWithOnlyTimersWakesOnThem(t *testing.T) {
	state, u, fx := t.TempDir(), newUIThread(), &effects{}
	writeModule(t, state, "ticker", `"glitched": { "timers": [{ "id": "t", "every": 30 }] }, `, `
		export function update({ event }) { if (event.kind === "timer") host.storage.set("ticks", (host.storage.get("ticks") ?? 0) + 1); return {} }
		export function view() { return { body: { type: "text", text: "ticks " + (host.storage.get("ticks") ?? 0) } } }`)
	h := Open(state, Options{Post: u.post, Do: fx.do, Logf: t.Logf, TimerScale: 0.002, SignOwn: true})
	t.Cleanup(h.Close)
	u.until(t, "a tick with nothing shown", func() bool {
		return h.sessions[pageKey{plugin: "ticker"}] != nil && h.sessions[pageKey{plugin: "ticker"}].Rev > 1
	})
	s := session(t, h, "ticker")
	u.until(t, "the ticks", func() bool { return strings.Contains(texts(s), "ticks ") && !strings.Contains(texts(s), "ticks 0") })
}
