// Package conformance is the suite every adapter of package browse
// passes, so that the browser, and agents in it, behave the same on every
// engine: navigation and its events, the app's own pages, script
// evaluation, and the page script's snapshots and refs on pages made to
// catch where engines differ. An adapter's test runs it with a Harness on
// a real engine (see sysweb's).
package conformance

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"glitched/internal/browse"
	"glitched/internal/webtarget"
)

// Harness is an engine for the suite.
type Harness struct {
	// Open makes pages, as the browser does.
	Open browse.Opener
	// Do runs fn where the view builds, as page methods are called, and
	// returns once it ran.
	Do func(fn func())
	// Show shows the page in a window, as the pane does, for engines
	// that load only what shows; nil if pages load unshown.
	Show func(p browse.Page)
	// Known are the suite's tests the engine fails for a known reason,
	// by name, as a bug of its own to fix upstream; they are skipped,
	// saying why.
	Known map[string]string
}

// recorder notes a page's events.
type recorder struct {
	mu                        sync.Mutex
	navigated, titles, opened []string
	failed                    []string // url, then why
	changed                   int
}

func (r *recorder) events() browse.Events {
	note := func(fn func()) { r.mu.Lock(); fn(); r.mu.Unlock() }
	return browse.Events{
		Navigated: func(u string) { note(func() { r.navigated = append(r.navigated, u) }) },
		Titled:    func(s string) { note(func() { r.titles = append(r.titles, s) }) },
		Failed:    func(u, why string) { note(func() { r.failed = append(r.failed, u, why) }) },
		Opened:    func(u string) { note(func() { r.opened = append(r.opened, u) }) },
		Changed:   func() { note(func() { r.changed++ }) },
	}
}

func (r *recorder) last(list *[]string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(*list) == 0 {
		return ""
	}
	return (*list)[len(*list)-1]
}

func (r *recorder) count(list *[]string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(*list)
}

// suite is one run's server and engine.
type suite struct {
	t   *testing.T
	h   Harness
	srv *httptest.Server
}

func (s *suite) url(path string) string { return s.srv.URL + path }

// until waits for cond, failing the test after a while.
func (s *suite) until(what string, cond func() bool) {
	s.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			s.t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// framed waits for the snapshot page's frame to load.
func (s *suite) framed(p browse.Page) {
	s.t.Helper()
	s.until("the frame", func() bool {
		v, err := p.EvalContext(context.Background(), `return ["framed", "hidden-frame"].every((id) => !!document.getElementById(id).contentDocument?.querySelector("button"))`)
		return err == nil && v == true
	})
}

// open opens a page at path and waits for it to load.
func (s *suite) open(path string) (browse.Page, *recorder) {
	s.t.Helper()
	r := &recorder{}
	var p browse.Page
	var err error
	s.h.Do(func() { p, err = s.h.Open(s.url(path), r.events()) })
	if err != nil {
		s.t.Fatal(err)
	}
	if s.h.Show != nil {
		s.h.Do(func() { s.h.Show(p) })
	}
	s.t.Cleanup(func() { s.h.Do(p.Close) })
	s.until("the first page", func() bool { return r.last(&r.navigated) == s.url(path) })
	s.ready(p)
	if path == "/snap" {
		s.framed(p)
	}
	return p, r
}

// ready waits until the page has loaded.
func (s *suite) ready(p browse.Page) {
	s.t.Helper()
	s.until("the page to load", func() bool {
		v, err := p.EvalContext(context.Background(), "return document.readyState")
		loading := true
		s.h.Do(func() { loading = p.IsLoading() })
		return err == nil && v == "complete" && !loading
	})
}

func (s *suite) eval(p browse.Page, js string) any {
	s.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	v, err := p.EvalContext(ctx, js)
	if err != nil {
		s.t.Fatalf("%s: %v", js, err)
	}
	return v
}

// Run runs the suite.
func Run(t *testing.T, h Harness) {
	s := &suite{t: t, h: h, srv: httptest.NewServer(fixtures())}
	defer s.srv.Close()
	defer s.srv.CloseClientConnections() // the slow page's
	for _, c := range []struct {
		name string
		test func(*testing.T)
	}{
		{"Navigation", s.navigation},
		{"AppPages", s.appPages},
		{"Failures", s.failures},
		{"UnresolvedHost", s.unresolved},
		{"Windows", s.windows},
		{"Closed", s.closed},
		{"Eval", s.evaluation},
		{"Snapshot", s.snapshot},
		{"Limits", s.limits},
		{"Refs", s.refs},
		{"Tampering", s.tampering},
	} {
		t.Run(c.name, func(t *testing.T) {
			if why, ok := h.Known[c.name]; ok {
				t.Skip("known: " + why)
			}
			c.test(t)
		})
	}
}

// Loading, the title, LoadURL, back, forward and reload, each a
// navigation the tab hears of.
func (s *suite) navigation(t *testing.T) {
	s.t = t
	p, r := s.open("/a")
	s.until("the title", func() bool { return r.last(&r.titles) == "Page A" })
	r.mu.Lock()
	changed := r.changed
	r.mu.Unlock()
	if changed == 0 {
		t.Error("loading changed nothing")
	}
	s.h.Do(func() { p.LoadURL(s.url("/b")) })
	s.until("/b", func() bool { return r.last(&r.navigated) == s.url("/b") })
	s.until("its title", func() bool { return r.last(&r.titles) == "Page B" })
	s.ready(p)
	var back, fwd bool
	s.h.Do(func() { back, fwd = p.CanGoBack(), p.CanGoForward() })
	if !back || fwd {
		t.Fatalf("at /b: back %v, forward %v; want back only", back, fwd)
	}
	s.h.Do(p.GoBack)
	s.until("back at /a", func() bool { return r.last(&r.navigated) == s.url("/a") })
	s.ready(p)
	s.h.Do(func() { fwd = p.CanGoForward() })
	if !fwd {
		t.Fatal("no forward after going back")
	}
	s.h.Do(p.GoForward)
	s.until("forward at /b", func() bool { return r.last(&r.navigated) == s.url("/b") })
	s.ready(p)
	// A reload is a new page: what a script left is gone.
	s.eval(p, "window.mark = 1; return 1")
	s.h.Do(p.Reload)
	s.until("the reload", func() bool {
		v, err := p.EvalContext(context.Background(), "return typeof window.mark")
		return err == nil && v == "undefined"
	})
	// A link the user follows.
	s.ready(p)
	s.eval(p, "document.querySelector('a').click(); return 1")
	s.until("the link to /a", func() bool { return r.last(&r.navigated) == s.url("/a") })
}

// The app's own pages, as the one saying a load failed: no navigation,
// and scripts run in them.
func (s *suite) appPages(t *testing.T) {
	s.t = t
	p, r := s.open("/a")
	n := r.count(&r.navigated)
	s.h.Do(func() { p.LoadHTML(`<!doctype html><title>Own</title><p id="own">mine</p>`) })
	s.until("the app's page", func() bool {
		v, err := p.EvalContext(context.Background(), "return document.getElementById('own')?.textContent")
		return err == nil && v == "mine"
	})
	time.Sleep(200 * time.Millisecond)
	if got := r.count(&r.navigated); got != n {
		t.Errorf("LoadHTML was a navigation: %v", r.navigated[n:])
	}
}

// A load that fails says where and why.
func (s *suite) failures(t *testing.T) {
	s.t = t
	p, r := s.open("/a")
	down := closedURL()
	s.h.Do(func() { p.LoadURL(down) })
	s.until("the failure", func() bool { return r.count(&r.failed) > 0 })
	r.mu.Lock()
	at, why := r.failed[0], r.failed[1]
	r.mu.Unlock()
	if strings.TrimSuffix(at, "/") != strings.TrimSuffix(down, "/") || why == "" {
		t.Errorf("failed at %q (%q), want %s and why", at, why, down)
	}
}

// An address no name server knows fails too.
func (s *suite) unresolved(t *testing.T) {
	s.t = t
	p, r := s.open("/a")
	s.h.Do(func() { p.LoadURL("http://glitched-conformance.invalid/") })
	s.until("the failure", func() bool { return r.count(&r.failed) > 0 })
}

// A page closed tells nothing more, not even what it was doing: a page
// loading, one failing to, and one asking for a window as it loads.
func (s *suite) closed(t *testing.T) {
	s.t = t
	for _, u := range []string{s.url("/a"), closedURL(), s.url("/opens")} {
		r := &recorder{}
		var p browse.Page
		var err error
		s.h.Do(func() {
			if p, err = s.h.Open(u, r.events()); err == nil {
				p.Close()
			}
		})
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Second)
		r.mu.Lock()
		if len(r.navigated)+len(r.titles)+len(r.failed)+len(r.opened)+r.changed > 0 {
			t.Errorf("%s, closed, told %v, %v, failed %v, opened %v, %d changes", u, r.navigated, r.titles, r.failed, r.opened, r.changed)
		}
		r.mu.Unlock()
	}
}

// A page asking for a window gets none: the tab hears of it.
func (s *suite) windows(t *testing.T) {
	s.t = t
	p, r := s.open("/a")
	s.eval(p, "window.open('/b', '_blank'); return 1")
	s.until("the window asked for", func() bool { return r.last(&r.opened) == s.url("/b") })
	s.eval(p, "document.getElementById('blank').click(); return 1")
	s.until("the link to a new window", func() bool { return r.count(&r.opened) == 2 })
}

// Scripts run as the body of an async function: their results come back
// decoded, promises awaited, throws as errors.
func (s *suite) evaluation(t *testing.T) {
	s.t = t
	p, _ := s.open("/a")
	if v := s.eval(p, "return 1 + 1"); v != float64(2) {
		t.Errorf("1 + 1 = %#v", v)
	}
	v, _ := s.eval(p, "return {a: [1, 'x', null, true]}").(map[string]any)
	if a, _ := v["a"].([]any); len(a) != 4 || a[0] != float64(1) || a[1] != "x" || a[2] != nil || a[3] != true {
		t.Errorf("object = %#v", v)
	}
	if v := s.eval(p, "return new Promise(r => setTimeout(() => r('late'), 50))"); v != "late" {
		t.Errorf("promise = %#v", v)
	}
	if v := s.eval(p, "const x = await Promise.resolve(3); return x * 2"); v != float64(6) {
		t.Errorf("await = %#v", v)
	}
	if _, err := p.EvalContext(context.Background(), "throw new Error('boom')"); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("a throw returned %v", err)
	}
	if v := s.eval(p, "return undefined"); v != nil {
		t.Errorf("undefined = %#v", v)
	}
}

// The snapshot of a page made to catch where engines and the script could
// differ.
func (s *suite) snapshot(t *testing.T) {
	s.t = t
	p, _ := s.open("/snap")
	// A frame still loading: its page never comes.
	s.eval(p, `const slow = document.createElement("iframe"); slow.src = "/slow"; document.body.append(slow); return 1`)
	snap, err := webtarget.Take(context.Background(), p, webtarget.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("snapshot of %s:\n%s", snap.URL, snap.Text)
	ref := fmt.Sprintf(" [ref=s%de", snap.Epoch)
	for _, want := range []string{
		`- heading "Fixture" [level=1]`,
		`- heading "Untagged heading" [level=2]`,
		`- button "Inside contents"` + ref,
		`- treeitem "Child item"` + ref,
		`- combobox` + ref,
		`- option "One"`,
		`- option "Two" [selected]`,
		`- button "More"` + ref,
		`- button "Fenced" [disabled]` + ref,
		`- button "Peeking"` + ref,
		`- link "svg link"` + ref,
		`- textbox "Shadow label"` + ref,
		`- button "In frame"` + ref,
		`- link [url="/b"]` + ref,
		"- iframe [loading]",
		`- button "Scripted frame"` + ref,
	} {
		if !strings.Contains(snap.Text, want) {
			t.Errorf("no %q", want)
		}
	}
	// A closed dropdown's options have no box to act on: no refs.
	if strings.Contains(snap.Text, `- option "One" [ref`) {
		t.Error("a closed dropdown's option has a ref")
	}
	for _, hidden := range []string{"Invisible", "Hidden attr", "ARIA hidden", "Not displayed", "In details", "Hidden frame"} {
		if strings.Contains(snap.Text, hidden) {
			t.Errorf("%q, hidden, is in the snapshot", hidden)
		}
	}
	if len(snap.Elements) != snap.Refs || snap.Title != "Snap" || snap.URL != s.url("/snap") {
		t.Errorf("%d elements for %d refs, title %q at %s", len(snap.Elements), snap.Refs, snap.Title, snap.URL)
	}
	full, err := webtarget.Take(context.Background(), p, webtarget.Options{Full: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"- tree\n", "  - treeitem \"Parent\" [ref=", "    - group\n", "      - treeitem \"Child item\" [ref="} {
		if !strings.Contains(full.Text, want) {
			t.Errorf("full snapshot has no %q:\n%s", want, full.Text)
		}
	}
}

// A snapshot stops at its bounds, saying so, with every ref it shows.
func (s *suite) limits(t *testing.T) {
	s.t = t
	p, _ := s.open("/snap")
	few, err := webtarget.Take(context.Background(), p, webtarget.Options{MaxRefs: 3})
	if err != nil {
		t.Fatal(err)
	}
	if few.Refs != 3 || len(few.Elements) != 3 || !few.Truncated || !strings.Contains(few.Text, "truncated at 3 refs") {
		t.Errorf("3 refs: %d refs, %d elements, truncated %v:\n%s", few.Refs, len(few.Elements), few.Truncated, few.Text)
	}
	short, err := webtarget.Take(context.Background(), p, webtarget.Options{MaxChars: 150})
	if err != nil {
		t.Fatal(err)
	}
	body, _, _ := strings.Cut(short.Text, "- … truncated")
	if !short.Truncated || len(body) > 150 || short.Refs != strings.Count(body, "[ref=") {
		t.Errorf("150 characters: %d, %d refs, truncated %v:\n%s", len(body), short.Refs, short.Truncated, short.Text)
	}
}

// Refs find their elements, where they show through frames; a newer
// snapshot, a reload or the element going makes them fail, saying why.
func (s *suite) refs(t *testing.T) {
	s.t = t
	p, _ := s.open("/snap")
	ctx := context.Background()
	snap, err := webtarget.Take(ctx, p, webtarget.Options{})
	if err != nil {
		t.Fatal(err)
	}
	find := func(snap webtarget.Snapshot, name string) string {
		for _, e := range snap.Elements {
			if e.Name == name {
				return e.Ref
			}
		}
		t.Fatalf("no ref named %q", name)
		return ""
	}
	inFrame := find(snap, "In frame")
	e, err := webtarget.Describe(ctx, p, inFrame)
	if err != nil {
		t.Fatal(err)
	}
	if err := snap.Verify(inFrame, e); err != nil {
		t.Error(err)
	}
	// The frame is at 300, 200, with a border of 7 and padding of 5; its
	// body's margin is 8, the button's 10.
	if math.Abs(e.Box.X-330) > 1 || math.Abs(e.Box.Y-230) > 1 || e.Box.Width < 10 {
		t.Errorf("the framed button is at %+v, want 330, 230 in the top viewport", e.Box)
	}
	newer, err := webtarget.Take(ctx, p, webtarget.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := webtarget.Describe(ctx, p, inFrame); err == nil || !strings.Contains(err.Error(), "not the latest") {
		t.Errorf("an older snapshot's ref: %v", err)
	}
	contents := find(newer, "Inside contents")
	s.eval(p, "document.getElementById('contents').remove(); return 1")
	if _, err := webtarget.Describe(ctx, p, contents); err == nil || !strings.Contains(err.Error(), "is gone") {
		t.Errorf("a removed element's ref: %v", err)
	}
	s.h.Do(p.Reload)
	s.until("the reload", func() bool {
		v, err := p.EvalContext(ctx, "return document.getElementById('contents') !== null && document.readyState")
		return err == nil && v == "complete"
	})
	s.framed(p)
	if _, err := webtarget.Describe(ctx, p, find(newer, "Fenced")); err == nil || !strings.Contains(err.Error(), "navigated or reloaded") {
		t.Errorf("a ref from before a reload: %v", err)
	}
}

// A page can't plant or replace the script's state, nor redirect a ref
// by patching the built-ins resolving it goes through after the script
// is installed. That is all this guards; a page that changed them before
// the first snapshot, or that calls the script itself, still can mislead
// it, until it runs in an isolated world (docs/browser.md).
func (s *suite) tampering(t *testing.T) {
	s.t = t
	p, _ := s.open("/snap")
	ctx := context.Background()
	snap, err := webtarget.Take(ctx, p, webtarget.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ref := snap.Elements[0].Ref
	s.eval(p, `for (const k of Object.getOwnPropertyNames(window)) {
		if (!k.startsWith("__glitched_")) continue;
		try { window[k] = { describe: () => ({ role: "button", name: "Fake" }) }; } catch {}
		try { delete window[k]; } catch {}
		try { window[k].describe = () => ({ role: "button", name: "Fake" }); } catch {}
	}
	// Without the key: every ref finds the page's own element, named Fake.
	const fake = document.createElement("button");
	fake.textContent = "Fake";
	document.body.append(fake);
	WeakRef.prototype.deref = () => fake;
	const attr = Element.prototype.getAttribute;
	Element.prototype.getAttribute = function (n) { return n === "aria-label" ? "Fake" : attr.call(this, n); };
	Element.prototype.getBoundingClientRect = () => new DOMRect(1, 2, 3, 4);
	return 1`)
	e, err := webtarget.Describe(ctx, p, ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := snap.Verify(ref, e); err != nil || e.Name == "Fake" || e.Box.X == 1 {
		t.Errorf("after the page tampered: %+v, %v", e, err)
	}
	// Patched between snapshots, WeakRef and the arrays the next one fills
	// change nothing either.
	s.eval(p, `const fake = document.querySelector("button:last-of-type");
	window.WeakRef = class { constructor() {} deref() { return fake; } };
	Array.prototype.push = function () { return 0; };
	return 1`)
	next, err := webtarget.Take(ctx, p, webtarget.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var contents string
	for _, n := range next.Elements {
		if n.Name == "Inside contents" {
			contents = n.Ref
		}
	}
	if contents == "" {
		t.Fatalf("the next snapshot lost its refs:\n%s", next.Text)
	}
	if e, err := webtarget.Describe(ctx, p, contents); err != nil || e.Name != "Inside contents" {
		t.Errorf("after the page patched WeakRef: %+v, %v", e, err)
	}
}

// fixtures are the suite's pages.
func fixtures() http.Handler {
	mux := http.NewServeMux()
	page := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, body)
		}
	}
	mux.Handle("/a", page(`<!doctype html><title>Page A</title><a href="/b">to b</a> <a id="blank" href="/c" target="_blank">new window</a>`))
	mux.Handle("/b", page(`<!doctype html><title>Page B</title><a href="/a">to a</a>`))
	mux.Handle("/c", page(`<!doctype html><title>Page C</title>`))
	mux.Handle("/opens", page(`<!doctype html><title>Opens</title><script>window.open("/b")</script>`))
	mux.Handle("/snap", page(snapFixture))
	// A page that doesn't come, as a frame still loading shows.
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(30 * time.Second):
		}
	})
	return mux
}

// closedURL is an address nothing answers at.
func closedURL() string {
	srv := httptest.NewServer(http.NotFoundHandler())
	u := srv.URL + "/"
	srv.Close()
	return u
}

const snapFixture = `<!doctype html><meta charset="utf-8"><title>Snap</title>
<h1>Fixture</h1>
<div role="heading">Untagged heading</div>
<div style="display:contents"><button id="contents">Inside contents</button></div>
<ul role="tree"><li role="treeitem">Parent<ul role="group"><li role="treeitem">Child item</li></ul></li></ul>
<select><option>One</option><option selected>Two</option></select>
<fieldset disabled><button>Fenced</button></fieldset>
<details><summary>More</summary><button>In details</button></details>
<div style="visibility:hidden"><button style="visibility:visible">Peeking</button><button>Invisible</button></div>
<div hidden><button>Hidden attr</button></div>
<div aria-hidden="true"><button>ARIA hidden</button></div>
<div style="display:none"><button>Not displayed</button></div>
<svg width="80" height="20"><a href="/b"><text x="0" y="15">svg link</text></a></svg>
<a href="/b"><img src="data:image/gif;base64,R0lGODlhAQABAAAAACw=" alt=""></a>
<div id="host"></div>
<iframe id="framed" style="position:absolute;left:300px;top:200px;width:300px;height:120px;border:7px solid #888;padding:5px"
  srcdoc="<button style='margin:10px'>In frame</button>"></iframe>
<iframe id="hidden-frame" style="visibility:hidden" srcdoc="<button>Hidden frame</button>"></iframe>
<iframe id="scripted"></iframe>
<script>
const root = document.getElementById("host").attachShadow({ mode: "open" });
root.innerHTML = '<span id="lbl">Shadow label</span><input aria-labelledby="lbl">';
// A frame with no address that a script fills, as an editor's.
document.getElementById("scripted").contentDocument.body.innerHTML = "<button>Scripted frame</button>";
</script>`
