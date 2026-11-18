package plugins

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"glitched/internal/plugins/pack"
	"glitched/internal/plugins/widget"
)

// uiThread is the UI thread of a test: what the host posts waits until
// the test runs it.
type uiThread struct{ q chan func() }

func newUIThread() *uiThread { return &uiThread{q: make(chan func(), 4096)} }

func (u *uiThread) post(fn func()) { u.q <- fn }

// until runs what was posted until ok holds.
func (u *uiThread) until(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.After(20 * time.Second)
	for !ok() {
		select {
		case fn := <-u.q:
			fn()
		case <-time.After(time.Millisecond): // ok may hold with nothing posted
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// effects records the effects the app runs, and answers them.
type effects struct {
	ran    []Effect
	answer func(Effect) (any, error)
}

func (e *effects) do(_ string, ef Effect) (any, error) {
	e.ran = append(e.ran, ef)
	if e.answer != nil {
		return e.answer(ef)
	}
	return nil, nil
}

// signAsOwn signs the plugin in folder with the device's key of the state
// directory, as Glitched signs the plugins you write.
func signAsOwn(t *testing.T, state, folder string) {
	t.Helper()
	key, err := pack.LoadKey(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := pack.Sign(folder, key); err != nil {
		t.Fatal(err)
	}
}

// copyPlugin installs the plugin in examples/plugins/name as folder.
func copyPlugin(t *testing.T, state, name, folder string) {
	t.Helper()
	src := filepath.Join("../../examples/plugins", name)
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(Dir(state), folder)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	signAsOwn(t, state, dst)
}

// writeModule installs a plugin with the manifest and module.
func writeModule(t *testing.T, state, id, extra, js string) {
	t.Helper()
	dst := filepath.Join(Dir(state), id)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"id": "` + id + `", "name": "` + id + `", "main": "index.js", "contract": "glitched", "contractVersion": 1, ` + extra + `"contributes": {"pages": [{"id": "home", "title": "` + id + `"}]}}`
	if err := os.WriteFile(filepath.Join(dst, "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "index.js"), []byte(js), 0o644); err != nil {
		t.Fatal(err)
	}
	signAsOwn(t, state, dst)
}

func open(t *testing.T, state string, u *uiThread, fx *effects) *Host {
	t.Helper()
	h := Open(state, Options{Post: u.post, Do: fx.do, Logf: t.Logf, CallTimeout: 2 * time.Second, SignOwn: true})
	t.Cleanup(h.Close)
	return h
}

func session(t *testing.T, h *Host, id string) *Session {
	t.Helper()
	s, err := h.Session(id, "home")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// texts are the view's texts, its widgets' text and labels.
func texts(s *Session) string {
	if s.View == nil {
		return ""
	}
	var b strings.Builder
	s.View.Body.Walk(func(n *widget.Node) { b.WriteString(n.Text + "|" + n.Label + "|") })
	return b.String()
}

func TestCounterCountsAndKeepsItsStateBetweenEvents(t *testing.T) {
	state, u, fx := t.TempDir(), newUIThread(), &effects{}
	copyPlugin(t, state, "counter", "counter")
	h := open(t, state, u, fx)
	s := session(t, h, "counter")
	u.until(t, "the first view", func() bool { return strings.Contains(texts(s), "Count: 0") })
	if s.View.Find("dec").Disabled != true {
		t.Fatal("Less is enabled at 0")
	}
	for range 3 {
		s.Dispatch(UIEvent{Type: "click", Handler: "inc", Node: "inc"}, nil)
	}
	u.until(t, "three clicks", func() bool { return strings.Contains(texts(s), "Count: 3") })
	s.Dispatch(UIEvent{Type: "change", Handler: "name", Node: "name", Value: "Ada"}, map[string]any{"name": "Ada"})
	s.Dispatch(UIEvent{Type: "change", Handler: "details", Node: "details", Value: true}, map[string]any{"name": "Ada", "details": true})
	u.until(t, "the greeting and its details", func() bool {
		return strings.Contains(texts(s), "Hello, Ada!") && strings.Contains(texts(s), "The name has 3 characters.")
	})
	if !strings.Contains(texts(s), "Count: 3") {
		t.Fatalf("the count was lost: %s", texts(s))
	}
	if s.Err != nil {
		t.Fatal(s.Err)
	}
}

func TestChangesOfOneWidgetCoalesceWhileTheyWait(t *testing.T) {
	state, u, fx := t.TempDir(), newUIThread(), &effects{}
	writeModule(t, state, "slow", "", `
		export function init() { return { state: { seen: [] } } }
		export function view({ state }) { return { body: { type: "text", text: state.seen.join(",") } } }
		export function update({ state, event }) {
			const until = Date.now() + 50; while (Date.now() < until) {}
			return { state: { seen: [...state.seen, event.value] } }
		}`)
	h := open(t, state, u, fx)
	s := session(t, h, "slow")
	u.until(t, "the first view", func() bool { return s.View != nil })
	for _, v := range []string{"a", "ab", "abc", "abcd"} {
		s.Dispatch(UIEvent{Type: "change", Handler: "h", Node: "in", Value: v}, nil)
	}
	u.until(t, "the last change", func() bool { return strings.HasSuffix(texts(s), "abcd||") })
	if got := strings.Count(texts(s), ","); got > 1 {
		t.Fatalf("the waiting changes were each taken: %s", texts(s))
	}
}

func TestSnippetsAsksForThreadsAndIsDeniedUntilGranted(t *testing.T) {
	state, u := t.TempDir(), newUIThread()
	fx := &effects{answer: func(e Effect) (any, error) {
		if e.Type == "threads.list" {
			return []map[string]string{{"id": "t1", "title": "Fix the build", "status": "working"}}, nil
		}
		return nil, nil
	}}
	copyPlugin(t, state, "snippets", "snippets")
	h := open(t, state, u, fx)
	s := session(t, h, "snippets")
	u.until(t, "the denial", func() bool { return strings.Contains(texts(s), "Grant “threads:read”") })
	if len(fx.ran) != 0 {
		t.Fatalf("the app ran %v for a plugin not granted it", fx.ran)
	}
	if err := h.SetGranted("snippets", "threads:read", true); err != nil {
		t.Fatal(err)
	}
	// The grant holds at once: the page asks again, and keeps its state.
	if s.closed {
		t.Fatal("a grant reloaded the page")
	}
	s.Dispatch(UIEvent{Type: "click", Handler: "refresh", Node: "refresh"}, nil)
	u.until(t, "the threads", func() bool { return strings.Contains(texts(s), "Fix the build") })
	b, err := os.ReadFile(prefsPath(state))
	if err != nil || !strings.Contains(string(b), "threads:read") {
		t.Fatalf("plugins.json: %s, %v", b, err)
	}
	if got := Open(state, Options{}).Installed()[0].Capabilities; !got[2].Granted || got[3].Granted {
		t.Fatalf("the grants after a restart: %+v", got)
	}
}

func TestSnippetsSavesWhatItIsGivenAndRefusesAnEmptyPrompt(t *testing.T) {
	state, u, fx := t.TempDir(), newUIThread(), &effects{}
	copyPlugin(t, state, "snippets", "snippets")
	h := open(t, state, u, fx)
	s := session(t, h, "snippets")
	u.until(t, "the page", func() bool { return strings.Contains(texts(s), "No snippets yet.") })
	s.Dispatch(UIEvent{Type: "click", Handler: "new", Node: "new"}, nil)
	u.until(t, "the form", func() bool { return s.View.Find("prompt") != nil })
	s.Dispatch(UIEvent{Type: "click", Handler: "save", Node: "save"}, map[string]any{"title": "x", "prompt": " ", "mode": "draft", "pinned": false})
	u.until(t, "the refusal", func() bool { return strings.Contains(texts(s), "A snippet needs a prompt.") })
	s.Dispatch(UIEvent{Type: "click", Handler: "save", Node: "save"}, map[string]any{"title": "Review", "prompt": "Review the branch", "mode": "draft", "pinned": true})
	u.until(t, "the saved snippet", func() bool { return strings.Contains(texts(s), "Review the branch") })
	s.Dispatch(UIEvent{Type: "click", Handler: "use", Node: "use:1", Payload: json.RawMessage(`"1"`)}, nil)
	u.until(t, "the draft", func() bool { return len(fx.ran) == 1 })
	if e := fx.ran[0]; e.Type != "composer.draft" || !strings.Contains(string(e.Args), "Review the branch") {
		t.Fatalf("ran %s %s", e.Type, e.Args)
	}

	h.Close()
	u2 := newUIThread()
	h2 := open(t, state, u2, fx)
	s2 := session(t, h2, "snippets")
	u2.until(t, "the snippet after a restart", func() bool { return strings.Contains(texts(s2), "Review the branch") })
}

func TestCommandsRunTheirEffects(t *testing.T) {
	state, u, fx := t.TempDir(), newUIThread(), &effects{}
	copyPlugin(t, state, "snippets", "snippets")
	h := open(t, state, u, fx)
	if got := h.Commands(); len(got) != 2 || got[0].Title != "New snippet" || got[0].PluginName != "Snippets" {
		t.Fatalf("commands %+v", got)
	}
	var done []error
	h.RunCommand("snippets", "draft-pinned", nil, func(err error) { done = append(done, err) })
	u.until(t, "the command", func() bool { return len(done) == 1 && len(fx.ran) == 1 })
	if done[0] != nil || fx.ran[0].Type != "toast" {
		t.Fatalf("done %v, ran %+v", done, fx.ran)
	}
	h.RunCommand("snippets", "nope", nil, func(err error) { done = append(done, err) })
	u.until(t, "the failure", func() bool { return len(done) == 2 })
	if done[1] == nil || !strings.Contains(done[1].Error(), "unknown command") {
		t.Fatalf("done %v", done)
	}
}

func TestMalformedPluginsFailAlone(t *testing.T) {
	state, u, fx := t.TempDir(), newUIThread(), &effects{}
	writeModule(t, state, "unknown-widget", "", `export function view() { return { body: { type: "iframe" } } }`)
	writeModule(t, state, "deep", "", `export function view() {
		let n = { type: "text", text: "x" }
		for (let i = 0; i < 40; i++) n = { type: "column", children: [n] }
		return { body: n } }`)
	writeModule(t, state, "twice", "", `export function view() { return { body: { type: "column", children: [
		{ type: "button", id: "a", label: "A" }, { type: "button", id: "a", label: "B" } ] } } }`)
	writeModule(t, state, "nameless", "", `export function view() { return { body: { type: "button", label: "A" } } }`)
	writeModule(t, state, "huge-state", "", `export function init() { return { state: "x".repeat(2 << 20) } }
		export function view() { return { body: { type: "text", text: "x" } } }`)
	writeModule(t, state, "throws", "", `export function view() { throw new Error("no view today") }`)
	writeModule(t, state, "loops", "", `export function view() { for (;;) {} }`)
	writeModule(t, state, "bad-answer", "", `export function init() { return { state: 1, html: "<b>" } }
		export function view() { return { body: { type: "text", text: "x" } } }`)
	writeModule(t, state, "unknown-capability", `"capabilities": ["filesystem"], `, `export function view() {}`)
	writeModule(t, state, "syntax", "", `export function view( {`)
	copyPlugin(t, state, "counter", "zz-counter")
	h := open(t, state, u, fx)

	wants := map[string]string{
		"unknown-widget": `unknown widget type "iframe"`,
		"deep":           "nested over 24 deep",
		"twice":          `the id "a" is used twice`,
		"nameless":       "a button needs an id",
		"huge-state":     "over the 1048576 allowed",
		"throws":         "no view today",
		"loops":          "did not answer view",
		"bad-answer":     `unknown field "html"`,
	}
	for id, want := range wants {
		s := session(t, h, id)
		u.until(t, id+"'s error", func() bool { return s.Err != nil })
		if !strings.Contains(s.Err.Error(), want) {
			t.Errorf("%s: %v, want %q", id, s.Err, want)
		}
		if s.View != nil {
			t.Errorf("%s: a view shows", id)
		}
	}
	failed := map[string]string{}
	for _, p := range h.Installed() {
		if p.Err != nil {
			failed[filepath.Base(p.Folder)] = p.Err.Error()
		}
	}
	if !strings.Contains(failed["unknown-capability"], `unknown capability "filesystem"`) || failed["syntax"] == "" || len(failed) != 2 {
		t.Fatalf("failed to load: %q", failed)
	}
	s := session(t, h, "counter")
	u.until(t, "the counter, beside them", func() bool { return strings.Contains(texts(s), "Count: 0") })
}

func TestAppCallsNeedTheirCapability(t *testing.T) {
	state, u, fx := t.TempDir(), newUIThread(), &effects{}
	writeModule(t, state, "sneaky", `"capabilities": ["clipboard"], `, `
		async function try_(f) { try { return "ok:" + JSON.stringify(await f()) } catch (e) { return e.code + ":" + e.message } }
		export async function init() {
			return { state: [
				await try_(() => glitched.threads.create("rm -rf /")),
				await try_(() => glitched.clipboard.write("hi")),
				await try_(() => host.call("app", { type: "eval" })),
				await try_(() => glitched.openURL("file:///etc/passwd")),
			] }
		}
		export function view({ state }) { return { body: { type: "text", text: state.join(" | ") } } }`)
	h := open(t, state, u, fx)
	s := session(t, h, "sneaky")
	u.until(t, "the four answers", func() bool { return strings.Count(texts(s), " | ") == 3 })
	got := texts(s)
	for _, want := range []string{
		"denied:permission denied: threads.create needs the threads:write capability, which the plugin doesn't declare",
		"ok:null",
		`unknown effect "eval"`,
		"not a web address",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("no %s in %s", want, got)
		}
	}
	if len(fx.ran) != 1 || fx.ran[0].Type != "clipboard.write" {
		t.Fatalf("the app ran %+v", fx.ran)
	}
}

func TestDisablingAPluginStopsItAndPersists(t *testing.T) {
	state, u, fx := t.TempDir(), newUIThread(), &effects{}
	copyPlugin(t, state, "counter", "counter")
	h := open(t, state, u, fx)
	s := session(t, h, "counter")
	u.until(t, "the view", func() bool { return s.View != nil })
	if err := h.SetEnabled("counter", false); err != nil {
		t.Fatal(err)
	}
	if len(h.Entries()) != 0 || h.HasView("counter") || !s.closed {
		t.Fatalf("entries %v after disabling", h.Entries())
	}
	if _, err := h.Session("counter", "home"); err == nil {
		t.Fatal("a disabled plugin's page opened")
	}
	s.Dispatch(UIEvent{Type: "click", Handler: "inc", Node: "inc"}, nil)
	if p := Open(state, Options{}).Installed(); p[0].Enabled {
		t.Fatal("enabled again after a restart")
	}
	if err := h.SetEnabled("counter", true); err != nil {
		t.Fatal(err)
	}
	s = session(t, h, "counter")
	u.until(t, "the view again", func() bool { return s.View != nil })
}

func TestClosingDropsWhatThePluginAnswersLate(t *testing.T) {
	state, u, fx := t.TempDir(), newUIThread(), &effects{}
	writeModule(t, state, "late", "", `
		export function view() { const until = Date.now() + 300; while (Date.now() < until) {} return { body: { type: "text", text: "late" } } }`)
	h := open(t, state, u, fx)
	s := session(t, h, "late")
	h.Close()
	time.Sleep(500 * time.Millisecond)
	for len(u.q) > 0 {
		(<-u.q)()
	}
	if s.View != nil || s.Err != nil {
		t.Fatalf("a closed page took %v, %v", s.View, s.Err)
	}
	if _, err := h.Session("late", "home"); err == nil {
		t.Fatal("a closed host opened a page")
	}
}

func TestRetryStartsThePageOver(t *testing.T) {
	state, u, fx := t.TempDir(), newUIThread(), &effects{}
	writeModule(t, state, "flaky", "", `
		export function init() { const n = (host.storage.get("n") ?? 0) + 1; host.storage.set("n", n); return { state: n } }
		export function view({ state }) { if (state === 1) throw new Error("first time"); return { body: { type: "text", text: "try " + state } } }`)
	h := open(t, state, u, fx)
	s := session(t, h, "flaky")
	u.until(t, "the failure", func() bool { return s.Err != nil })
	s.Retry()
	u.until(t, "the second try", func() bool { return strings.Contains(texts(s), "try 2") && s.Err == nil })
	if !errors.Is(nil, s.Err) {
		t.Fatal(s.Err)
	}
}

func TestAnEffectWaitingWhenItsGrantGoesDoesNotRun(t *testing.T) {
	state, u, fx := t.TempDir(), newUIThread(), &effects{}
	writeModule(t, state, "runner", `"capabilities": ["threads:write"], `, `
		export async function init() { try { await glitched.threads.create("go") } catch (e) {} return { state: 0 } }
		export function view() { return { body: { type: "text", text: "x" } } }`)
	h := open(t, state, u, fx)
	if err := h.SetGranted("runner", "threads:write", true); err != nil {
		t.Fatal(err)
	}
	session(t, h, "runner")
	time.Sleep(300 * time.Millisecond) // the call waits for the UI thread
	if err := h.SetGranted("runner", "threads:write", false); err != nil {
		t.Fatal(err)
	}
	for len(u.q) > 0 {
		(<-u.q)()
	}
	if len(fx.ran) != 0 {
		t.Fatalf("ran %+v after the grant went", fx.ran)
	}
}

func TestEachEventSeesTheValuesOfItsMoment(t *testing.T) {
	state, u, fx := t.TempDir(), newUIThread(), &effects{}
	writeModule(t, state, "form", "", `
		export function init() { return { state: [] } }
		export function view({ state }) { return { body: { type: "text", text: state.join(",") } } }
		export function update({ state, event, values }) {
			const until = Date.now() + 40; while (Date.now() < until) {}
			return { state: [...state, event.type + ":" + values.f] }
		}`)
	h := open(t, state, u, fx)
	s := session(t, h, "form")
	u.until(t, "the view", func() bool { return s.View != nil })
	s.Dispatch(UIEvent{Type: "change", Handler: "h", Node: "f", Value: "first"}, map[string]any{"f": "first"})
	s.Dispatch(UIEvent{Type: "change", Handler: "h", Node: "f", Value: "first!"}, map[string]any{"f": "first!"})
	s.Dispatch(UIEvent{Type: "click", Handler: "save", Node: "save"}, map[string]any{"f": "first!"})
	s.Dispatch(UIEvent{Type: "change", Handler: "h", Node: "f", Value: "second"}, map[string]any{"f": "second"})
	u.until(t, "the second change", func() bool { return strings.Contains(texts(s), "change:second") })
	if !strings.Contains(texts(s), "click:first!") {
		t.Fatalf("save saw later values: %s", texts(s))
	}
}

func TestRetryDropsWhatTheEarlierStartAnswers(t *testing.T) {
	state, u, fx := t.TempDir(), newUIThread(), &effects{}
	writeModule(t, state, "counting", `"capabilities": ["clipboard"], `, `
		export async function init() {
			const n = (host.storage.get("n") ?? 0) + 1; host.storage.set("n", n)
			await glitched.clipboard.write("start " + n)
			return { state: n }
		}
		export function view({ state }) { return { body: { type: "text", text: "start " + state } } }`)
	h := open(t, state, u, fx)
	s := session(t, h, "counting")
	time.Sleep(300 * time.Millisecond) // the first start's call waits for the UI thread
	s.Retry()
	u.until(t, "the second start", func() bool { return strings.Contains(texts(s), "start 2") })
	time.Sleep(200 * time.Millisecond)
	for len(u.q) > 0 {
		(<-u.q)()
	}
	if !strings.Contains(texts(s), "start 2") {
		t.Fatalf("the earlier start's answer landed: %s", texts(s))
	}
}

func TestPagesAreNamesNotPaths(t *testing.T) {
	state := t.TempDir()
	dst := filepath.Join(Dir(state), "nav")
	os.MkdirAll(dst, 0o755)
	os.WriteFile(filepath.Join(dst, "plugin.json"), []byte(`{"id": "nav", "name": "Nav", "main": "index.js",
		"navigation": [{"title": "Up", "page": "../../settings/plugins"}, {"title": "Dot", "page": ".."}, {"title": "Fine", "page": "home"}]}`), 0o644)
	os.WriteFile(filepath.Join(dst, "index.js"), []byte(`export function page() { return { title: "x", sections: [] } }`), 0o644)
	signAsOwn(t, state, dst)
	h := Open(state, Options{Logf: t.Logf})
	defer h.Close()
	if got := h.Entries(); len(got) != 1 || got[0].Page != "home" {
		t.Fatalf("entries %+v", got)
	}
}

func TestTheNetworkGrantIsACapability(t *testing.T) {
	state, u, fx := t.TempDir(), newUIThread(), &effects{}
	writeModule(t, state, "net", `"permissions": {"network": ["example.com"]}, `, `
		export function view() { return { body: { type: "text", text: "network " + host.capability("network") } } }`)
	h := open(t, state, u, fx)
	s := session(t, h, "net")
	u.until(t, "the view", func() bool { return strings.Contains(texts(s), "network false") })
	if err := h.SetGranted("net", Network, true); err != nil {
		t.Fatal(err)
	}
	s.refresh() // the same runtime, asked again
	u.until(t, "the granted view", func() bool { return strings.Contains(texts(s), "network true") })
}

func TestALinkedFolderLoads(t *testing.T) {
	state, src := t.TempDir(), t.TempDir()
	copyPlugin(t, src, "counter", "counter")
	signAsOwn(t, state, filepath.Join(Dir(src), "counter"))
	os.MkdirAll(Dir(state), 0o755)
	if err := os.Symlink(filepath.Join(Dir(src), "counter"), filepath.Join(Dir(state), "counter")); err != nil {
		t.Fatal(err)
	}
	h := Open(state, Options{Logf: t.Logf})
	defer h.Close()
	if len(h.Entries()) != 1 {
		t.Fatalf("installed %+v", h.Installed())
	}
}

func TestAnEarlierStartFailingLeavesARetryBe(t *testing.T) {
	state, u, fx := t.TempDir(), newUIThread(), &effects{}
	writeModule(t, state, "slowfail", "", `
		export function init() {
			const n = (host.storage.get("n") ?? 0) + 1; host.storage.set("n", n)
			if (n === 1) { const until = Date.now() + 300; while (Date.now() < until) {} throw new Error("first") }
			return { state: n } }
		export function view({ state }) { return { body: { type: "text", text: "start " + state } } }`)
	h := open(t, state, u, fx)
	s := session(t, h, "slowfail")
	time.Sleep(50 * time.Millisecond) // the first init runs
	s.Retry()
	u.until(t, "the second start", func() bool { return strings.Contains(texts(s), "start 2") })
	time.Sleep(100 * time.Millisecond)
	for len(u.q) > 0 {
		(<-u.q)()
	}
	if s.Err != nil {
		t.Fatalf("the first start's failure landed: %v", s.Err)
	}
}

func TestSavingThePluginSwapsItsCodeAndKeepsThePagesState(t *testing.T) {
	state, u, fx := t.TempDir(), newUIThread(), &effects{}
	copyPlugin(t, state, "counter", "counter")
	h := open(t, state, u, fx)
	stop := h.Watch(20 * time.Millisecond)
	defer stop()
	s := session(t, h, "counter")
	u.until(t, "the view", func() bool { return strings.Contains(texts(s), "Count: 0") })
	s.Dispatch(UIEvent{Type: "click", Handler: "inc", Node: "inc"}, nil)
	s.Dispatch(UIEvent{Type: "click", Handler: "inc", Node: "inc"}, nil)
	u.until(t, "two", func() bool { return strings.Contains(texts(s), "Count: 2") })

	src := filepath.Join(Dir(state), "counter", "index.ts")
	b, _ := os.ReadFile(src)
	time.Sleep(30 * time.Millisecond)
	os.WriteFile(src, []byte(strings.Replace(string(b), "`Count: ${", "`Total: ${", 1)), 0o644)
	u.until(t, "the new code, with the state", func() bool { return strings.Contains(texts(s), "Total: 2") })

	os.WriteFile(src, []byte("export function view( {"), 0o644)
	u.until(t, "the broken save reported", func() bool {
		err := h.Installed()[0].Err
		return err != nil && strings.Contains(err.Error(), "runs the code it had")
	})
	s.Dispatch(UIEvent{Type: "click", Handler: "inc", Node: "inc"}, nil)
	u.until(t, "the old code still running", func() bool { return strings.Contains(texts(s), "Total: 3") })
}
