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
	"strconv"
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
		v, err := p.EvalContext(context.Background(), `return ["framed", "hidden-frame"].every((id) => !!document.getElementById(id).contentDocument?.querySelector("button")) &&
			document.getElementById("editable").contentDocument?.readyState === "complete" &&
			document.getElementById("editable").contentDocument.body?.hasAttribute("contenteditable") === true`)
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
		{"MixedCase", s.mixedCase},
		{"Limits", s.limits},
		{"Refs", s.refs},
		{"PageCache", s.pageCache},
		{"ScaledFrames", s.scaledFrames},
		{"FractionalFrames", s.fractionalFrames},
		{"Tampering", s.tampering},
		{"Actions", s.actions},
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
		`- textbox "Editable body"` + ref,
		`- textbox "Design mode"` + ref,
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

// An HTML element named in mixed case, as XHTML keeps <Input/>, is no
// input but an unknown element: the page still has a snapshot, and the
// element only the role its attributes give it.
func (s *suite) mixedCase(t *testing.T) {
	s.t = t
	p, _ := s.open("/xhtml")
	snap, err := webtarget.Take(context.Background(), p, webtarget.Options{Full: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("snapshot of %s:\n%s", snap.URL, snap.Text)
	ref := fmt.Sprintf(" [ref=s%de", snap.Epoch)
	for _, want := range []string{
		`- textbox "Real input"` + ref,
		`- button "Real button"` + ref,
		`- combobox` + ref,
		`- button "Mixed role"` + ref,
	} {
		if !strings.Contains(snap.Text, want) {
			t.Errorf("no %q", want)
		}
	}
	for _, generic := range []string{"Mixed input", "Mixed button", "Mixed option", "Shouted"} {
		if strings.Contains(snap.Text, generic) {
			t.Errorf("%q, an unknown element, has a role", generic)
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
// snapshot, a reload, the element going or its frame navigating makes
// them fail, saying why.
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
	// Only the engine's pagehide, as the page leaves, drops the refs: a
	// page firing its own doesn't.
	for _, c := range []struct{ how, js string }{
		{"an Event on window", `dispatchEvent(new Event("pagehide"))`},
		{"a PageTransitionEvent on window", `dispatchEvent(new PageTransitionEvent("pagehide", {persisted: true}))`},
		{"an Event bubbling from the body", `document.body.dispatchEvent(new Event("pagehide", {bubbles: true}))`},
		{"a CustomEvent on an element", `document.getElementById("contents").dispatchEvent(new CustomEvent("pagehide"))`},
	} {
		fired, err := webtarget.Take(ctx, p, webtarget.Options{})
		if err != nil {
			t.Fatal(err)
		}
		s.eval(p, c.js+"; return 1")
		if _, err := webtarget.Describe(ctx, p, find(fired, "In frame")); err != nil {
			t.Errorf("a ref after the page fired pagehide, %s: %v", c.how, err)
		}
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
	// A frame that navigated shows another document: a ref into the one it
	// left fails, though its element still reads as connected there.
	inFrames, err := webtarget.Take(ctx, p, webtarget.Options{})
	if err != nil {
		t.Fatal(err)
	}
	left := find(inFrames, "In frame")
	s.eval(p, `document.getElementById("framed").srcdoc = "<button>Moved on</button>"; return 1`)
	s.until("the frame's next page", func() bool {
		v, err := p.EvalContext(ctx, `return document.getElementById("framed").contentDocument?.body?.textContent`)
		return err == nil && v == "Moved on"
	})
	if e, err := webtarget.Describe(ctx, p, left); err == nil || !strings.Contains(err.Error(), "its frame navigated") {
		t.Errorf("a ref into a frame's page it left: %+v, %v", e, err)
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

// A page gone back to from the back/forward cache is the document it was,
// script state and all, yet it navigated: its refs fail, as a reload's do.
func (s *suite) pageCache(t *testing.T) {
	s.t = t
	ctx := context.Background()
	cached, r := s.open("/a")
	before, err := webtarget.Take(ctx, cached, webtarget.Options{})
	if err != nil {
		t.Fatal(err)
	}
	toB := refNamed(before, "to b", 0)
	if toB == "" {
		t.Fatalf("no ref named \"to b\":\n%s", before.Text)
	}
	s.eval(cached, "window.cached = true; return 1")
	s.h.Do(func() { cached.LoadURL(s.url("/b")) })
	s.until("/b", func() bool { return r.last(&r.navigated) == s.url("/b") })
	s.ready(cached)
	if _, err := webtarget.Take(ctx, cached, webtarget.Options{}); err != nil {
		t.Fatal(err)
	}
	s.h.Do(cached.GoBack)
	s.until("back at /a", func() bool { return r.last(&r.navigated) == s.url("/a") })
	s.ready(cached)
	if s.eval(cached, "return window.cached === true") != true {
		t.Skip("the engine loaded /a again, not from its back/forward cache: nothing to check")
	}
	if e, err := webtarget.Describe(ctx, cached, toB); err == nil || !strings.Contains(err.Error(), "navigated or reloaded") {
		t.Errorf("a ref from before the page went to /b and back: %+v, %v", e, err)
	}
}

// A ref's box is where its element shows in the top viewport through
// frames that transforms scale, the frame's or an ancestor's, each axis on
// its own, and frames in them: the box a click at whose centre lands on
// the element.
func (s *suite) scaledFrames(t *testing.T) {
	s.t = t
	p, _ := s.open("/scaled")
	s.until("the scaled frames", func() bool {
		v, err := p.EvalContext(context.Background(), `const doc = (id, d = document) => d.getElementById(id)?.contentDocument;
			return !!doc("outer")?.querySelector("button") && !!doc("nested", doc("outer"))?.querySelector("button") && !!doc("grown")?.querySelector("button")`)
		return err == nil && v == true
	})
	ctx := context.Background()
	snap, err := webtarget.Take(ctx, p, webtarget.Options{})
	if err != nil {
		t.Fatal(err)
	}
	// Each button is 80 by 40 with a margin of 10 in a page with none. The
	// frame outer is at 40, 30 with a border of 4 and padding of 6, scaled
	// by 0.5 and 0.75; the frame nested, in it, at 100, 100 with a border
	// of 2 and padding of 3, scaled by 0.5; the frame grown, with a border
	// of 2 and padding of 3, is in a box at 300, 30 scaled by 2.
	for _, c := range []struct {
		name       string
		x, y, w, h float64
	}{
		{"Scaled", 40 + 0.5*(10+10), 30 + 0.75*(10+10), 80 * 0.5, 40 * 0.75},
		{"Nested", 40 + 0.5*(10+100+0.5*(5+10)), 30 + 0.75*(10+100+0.5*(5+10)), 80 * 0.25, 40 * 0.375},
		{"Grown", 300 + 2*(5+10), 30 + 2*(5+10), 80 * 2, 40 * 2},
	} {
		ref := refNamed(snap, c.name, 0)
		if ref == "" {
			t.Errorf("no ref named %q:\n%s", c.name, snap.Text)
			continue
		}
		e, err := webtarget.Describe(ctx, p, ref)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		b := e.Box
		if math.Abs(b.X-c.x) > 1 || math.Abs(b.Y-c.y) > 1 || math.Abs(b.Width-c.w) > 1 || math.Abs(b.Height-c.h) > 1 {
			t.Errorf("%s is at %+v, want {X:%g Y:%g Width:%g Height:%g} in the top viewport", c.name, b, c.x, c.y, c.w, c.h)
		}
		if cx, cy := b.X+b.Width/2, b.Y+b.Height/2; cx < c.x || cx > c.x+c.w || cy < c.y || cy > c.y+c.h {
			t.Errorf("a click at %s's centre, %g, %g, misses it", c.name, cx, cy)
		}
	}
}

// A ref's box through a frame whose layout size is a fraction of a pixel:
// a frame no transform scales gives the box it would with no scale to
// apply, exactly, and a scaled one the box a click at whose centre lands on
// a button half a pixel wide.
func (s *suite) fractionalFrames(t *testing.T) {
	s.t = t
	p, _ := s.open("/fractional")
	s.until("the fractional frames", func() bool {
		v, err := p.EvalContext(context.Background(), `return ["fraction", "percent", "scaled"].every(id => document.getElementById(id)?.contentDocument?.querySelector("button"))`)
		return err == nil && v == true
	})
	ctx := context.Background()
	snap, err := webtarget.Take(ctx, p, webtarget.Options{})
	if err != nil {
		t.Fatal(err)
	}
	// Each button is in a frame with no border or padding, half a pixel
	// wide, at its left and top in it. The frame fraction is 400.4 wide at
	// 40, 300; percent, 33.33% of a box 300 wide at 480, 300; scaled, 10.4
	// by 4 at 40, 380, scaled by 50.
	for _, c := range []struct {
		name, step string
		exact      bool
		x, y, w, h float64
	}{
		{"Fraction", "Unscaled", true, 40 + 399, 300 + 10, 0.5, 40},
		{"Percent", "UnscaledPercent", true, 480 + 99, 300 + 10, 0.5, 40},
		{"Scaled fraction", "Scaled", false, 40 + 50*9, 380 + 50*1, 50 * 0.5, 50 * 1},
	} {
		t.Run(c.step, func(t *testing.T) {
			ref := refNamed(snap, c.name, 0)
			if ref == "" {
				t.Fatalf("no ref named %q:\n%s", c.name, snap.Text)
			}
			e, err := webtarget.Describe(ctx, p, ref)
			if err != nil {
				t.Fatal(err)
			}
			b := e.Box
			if c.exact && (b.X != c.x || b.Y != c.y || b.Width != c.w || b.Height != c.h) {
				t.Errorf("%s is at %+v, want exactly {X:%g Y:%g Width:%g Height:%g} in the top viewport", c.name, b, c.x, c.y, c.w, c.h)
			}
			if math.Abs(b.X-c.x) > 1 || math.Abs(b.Y-c.y) > 1 || math.Abs(b.Width-c.w) > 1 || math.Abs(b.Height-c.h) > 1 {
				t.Errorf("%s is at %+v, want {X:%g Y:%g Width:%g Height:%g} in the top viewport", c.name, b, c.x, c.y, c.w, c.h)
			}
			if cx, cy := b.X+b.Width/2, b.Y+b.Height/2; cx < c.x || cx > c.x+c.w || cy < c.y || cy > c.y+c.h {
				t.Errorf("a click at %s's centre, %g, %g, misses it", c.name, cx, cy)
			}
		})
	}
}

// A page can't plant or replace the script's state, nor redirect a ref or
// change what one finds by patching, after the script is installed,
// anything resolving and describing go through: the built-ins it calls
// and the getters it reads, a regular expression's exec, an index setter
// on Array.prototype catching what it stores, a toJSON on Array.prototype
// rewriting what it answers. Each way is tried on a page of its own, on
// refs taken before it and on a snapshot taken after. That is all this
// guards; a page that changed them before the first snapshot, or that
// calls the script itself, still can mislead it, until it runs in an
// isolated world (docs/browser.md).
func (s *suite) tampering(t *testing.T) {
	s.t = t
	ctx := context.Background()
	names := []string{"Inside contents", "Twin", "In frame", "Shadow label"}
	for _, c := range []struct{ how, js string }{
		{"replacing the script's state and patching DOM methods", tamperState},
		{"patching RegExp's exec to move a ref to its twin", tamperExec},
		{"an index setter on Array.prototype", tamperIndex},
		{"patching the getters and methods names and boxes come from", tamperGetters},
		{"a toJSON on Array.prototype", tamperToJSON},
	} {
		p, _ := s.open("/snap")
		snap, err := webtarget.Take(ctx, p, webtarget.Options{})
		if err != nil {
			t.Fatal(err)
		}
		was := map[string]webtarget.Element{}
		for _, n := range names {
			if was[n], err = webtarget.Describe(ctx, p, refNamed(snap, n, 0)); err != nil {
				t.Fatalf("%s: %v", n, err)
			}
		}
		_, first, _ := webtarget.ParseRef(refNamed(snap, "Twin", 0))
		_, second, _ := webtarget.ParseRef(refNamed(snap, "Twin", 1))
		s.eval(p, strings.NewReplacer("FIRST", strconv.Itoa(first), "SECOND", strconv.Itoa(second)).Replace(c.js)+"\nreturn 1")
		same := func(snap webtarget.Snapshot, when string) {
			for _, n := range names {
				ref := refNamed(snap, n, 0)
				if ref == "" {
					t.Errorf("%s, %s: no ref named %q:\n%s", c.how, when, n, snap.Text)
					continue
				}
				e, err := webtarget.Describe(ctx, p, ref)
				if err == nil {
					err = snap.Verify(ref, e)
				}
				if err != nil || e != was[n] {
					t.Errorf("%s, %s: %s (%s) found %+v, was %+v (%v)", c.how, when, ref, n, e, was[n], err)
				}
			}
		}
		same(snap, "a ref from before")
		next, err := webtarget.Take(ctx, p, webtarget.Options{})
		if err != nil {
			t.Errorf("%s: the next snapshot: %v", c.how, err)
			continue
		}
		if want := strings.ReplaceAll(snap.Text, fmt.Sprintf("s%de", snap.Epoch), fmt.Sprintf("s%de", next.Epoch)); next.Text != want {
			t.Errorf("%s: the next snapshot says\n%s\nnot\n%s", c.how, next.Text, want)
		}
		same(next, "a ref from after")
	}
}

// Actions: a ref's element clicked, with the events a mouse fires, in a
// frame too; text typed into a field, a text area and an editor, as
// frameworks hear it; Enter submitting a form unless the page takes it;
// and what an action refuses: a disabled element, typing into a button,
// an element no longer what the snapshot named, and an older snapshot's
// ref.
func (s *suite) actions(t *testing.T) {
	s.t = t
	p, r := s.open("/act")
	s.until("the frame", func() bool {
		v, err := p.EvalContext(context.Background(), `return !!document.getElementById("fr").contentDocument?.querySelector("button")`)
		return err == nil && v == true
	})
	ctx := context.Background()
	snap, err := webtarget.Take(ctx, p, webtarget.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ref := func(name string) string {
		t.Helper()
		r := refNamed(snap, name, 0)
		if r == "" {
			t.Fatalf("no ref named %q in\n%s", name, snap.Text)
		}
		return r
	}
	if err := webtarget.Click(ctx, p, snap, ref("Press")); err != nil {
		t.Fatal(err)
	}
	if got := s.eval(p, `return seen.join(" ")`); got != "pointerdown mousedown pointerup mouseup click" {
		t.Errorf("a click fired %q", got)
	}
	if err := webtarget.Click(ctx, p, snap, ref("Agree")); err != nil {
		t.Fatal(err)
	}
	if got := s.eval(p, `return document.getElementById("c").checked`); got != true {
		t.Errorf("a clicked checkbox is checked: %v", got)
	}
	if err := webtarget.Click(ctx, p, snap, ref("Framed press")); err != nil {
		t.Fatal(err)
	}
	if got := s.eval(p, `return window.framed === 1`); got != true {
		t.Error("the button in the frame wasn't clicked")
	}
	if err := webtarget.Click(ctx, p, snap, ref("Off")); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Errorf("clicking a disabled button: %v", err)
	}
	if err := webtarget.Type(ctx, p, snap, ref("Notes"), "hello\nthere", false); err != nil {
		t.Fatal(err)
	}
	if got := s.eval(p, `return document.querySelector("textarea").value + "|" + notes`); got != "hello\nthere|hello\nthere" {
		t.Errorf("typing into a text area: %q", got)
	}
	if err := webtarget.Type(ctx, p, snap, ref("Editor"), "new text", false); err != nil {
		t.Fatal(err)
	}
	if got := s.eval(p, `return document.getElementById("ed").textContent`); got != "new text" {
		t.Errorf("typing into an editor: %q", got)
	}
	if err := webtarget.Type(ctx, p, snap, ref("Press"), "x", false); err == nil || !strings.Contains(err.Error(), "not a text box") {
		t.Errorf("typing into a button: %v", err)
	}
	if err := webtarget.Type(ctx, p, snap, ref("Taken"), "kept", true); err != nil {
		t.Fatal(err)
	}
	if got := s.eval(p, `return location.pathname + " " + window.enter`); got != "/act true" {
		t.Errorf("Enter the page took: %q", got)
	}
	s.eval(p, `document.getElementById("b").textContent = "Renamed"; return 1`)
	if err := webtarget.Click(ctx, p, snap, ref("Press")); err == nil || !strings.Contains(err.Error(), "now button") {
		t.Errorf("clicking an element renamed since: %v", err)
	}
	newer, err := webtarget.Take(ctx, p, webtarget.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := webtarget.Click(ctx, p, snap, ref("Agree")); err == nil || !strings.Contains(err.Error(), "not the latest") {
		t.Errorf("clicking an older snapshot's ref: %v", err)
	}
	snap = newer
	if err := webtarget.Type(ctx, p, snap, ref("Query"), "glitched", false); err != nil {
		t.Fatal(err)
	}
	if got := s.eval(p, `return inputs`); got != float64(1) {
		t.Errorf("the field heard %v inputs, want 1", got)
	}
	if err := webtarget.Type(ctx, p, snap, ref("Query"), "glitched", true); err != nil {
		t.Fatal(err)
	}
	want := s.url("/act/sent?q=glitched")
	s.until("the form's page", func() bool { return r.last(&r.navigated) == want })
}

// refNamed is the ref of the i-th element named name in a snapshot, or "".
func refNamed(snap webtarget.Snapshot, name string, i int) string {
	for _, e := range snap.Elements {
		if e.Name == name {
			if i == 0 {
				return e.Ref
			}
			i--
		}
	}
	return ""
}

// The ways a page tampers, in the tampering test. FIRST and SECOND are
// the numbers of the two buttons named Twin.
const (
	// Its state replaced or deleted, and without the key, every ref finds
	// the page's own element, named Fake, in the WeakRef and arrays a
	// snapshot fills.
	tamperState = `for (const k of Object.getOwnPropertyNames(window)) {
	if (!k.startsWith("__glitched_")) continue;
	try { window[k] = { describe: () => ({ role: "button", name: "Fake" }) }; } catch {}
	try { delete window[k]; } catch {}
	try { window[k].describe = () => ({ role: "button", name: "Fake" }); } catch {}
}
const fake = document.createElement("button");
fake.textContent = "Fake";
document.querySelector("[hidden]").append(fake);
WeakRef.prototype.deref = () => fake;
const attr = Element.prototype.getAttribute;
Element.prototype.getAttribute = function (n) { return n === "aria-label" ? "Fake" : attr.call(this, n); };
Element.prototype.getBoundingClientRect = () => new DOMRect(1, 2, 3, 4);
window.WeakRef = class { constructor() {} deref() { return fake; } };
Array.prototype.push = function () { return 0; };`
	// A ref parsed names the other twin, which Verify passes: both are a
	// button "Twin".
	tamperExec = `const exec = RegExp.prototype.exec;
RegExp.prototype.exec = function (s) {
	const m = exec.call(this, s);
	if (m && m[2] === "FIRST") m[2] = "SECOND";
	return m;
};`
	// The WeakRefs a snapshot stores are caught, and reading them back
	// finds another button.
	tamperIndex = `const decoy = new WeakRef([...document.querySelectorAll("button")].find((b) => b.textContent === "Fenced"));
const caught = new WeakMap();
for (let i = 0; i < 64; i++) {
	Object.defineProperty(Array.prototype, i, {
		configurable: true,
		get() { return caught.get(this)?.has(i) ? decoy : undefined; },
		set(v) {
			if (Object.prototype.toString.call(v) === "[object WeakRef]") {
				if (!caught.has(this)) caught.set(this, new Set());
				caught.get(this).add(i);
				return;
			}
			Object.defineProperty(this, i, { value: v, writable: true, enumerable: true, configurable: true });
		},
	});
}`
	// What roles, names, boxes and frames are read through, and the then
	// a promise resolved with an answer would look up.
	tamperGetters = `const fake = (C, k, get) => Object.defineProperty(C.prototype, k, { configurable: true, get });
Set.prototype.has = () => true;
Map.prototype.get = () => undefined;
String.prototype.toUpperCase = () => "BUTTON";
String.prototype.slice = () => "Fake";
String.prototype.trim = () => "Fake";
String.prototype.split = () => ["Fake"];
String.prototype.replace = () => "Fake";
Array.prototype.join = () => "Fake";
Array.prototype.map = () => ["Fake"];
fake(Node, "textContent", () => "Fake");
fake(HTMLElement, "innerText", () => "Fake");
fake(Element, "tagName", () => "BUTTON");
fake(Node, "isConnected", () => true);
fake(DOMRectReadOnly, "x", () => 1);
fake(DOMRect, "x", () => 1);
fake(HTMLIFrameElement, "contentDocument", () => null);
fake(Object, "then", function () {
	return this && this.box ? (done) => done({ role: this.role, name: "Fake", box: { x: 1, y: 2, width: 3, height: 4 } }) : undefined;
});`
	// A toJSON on Array.prototype, which the wrapper's JSON.stringify
	// would call on the elements a snapshot answers with.
	tamperToJSON = `Array.prototype.toJSON = () => [{ ref: "s1e1", role: "button", name: "Fake" }];`
)

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
	mux.Handle("/act", page(actFixture))
	mux.Handle("/act/sent", page(`<!doctype html><title>Sent</title>`))
	mux.Handle("/scaled", page(scaledFixture))
	mux.Handle("/scaled/outer", page(scaledPage("Scaled", `<iframe id="nested" style="left:100px;top:100px;width:200px;height:120px;border:2px solid #888;padding:3px;transform:scale(0.5)" src="/scaled/nested"></iframe>`)))
	mux.Handle("/scaled/nested", page(scaledPage("Nested", "")))
	mux.Handle("/scaled/grown", page(scaledPage("Grown", "")))
	mux.Handle("/fractional", page(fractionalFixture))
	mux.Handle("/fractional/fraction", page(thinPage("Fraction", "left:399px;top:10px;width:.5px;height:40px")))
	mux.Handle("/fractional/percent", page(thinPage("Percent", "left:99px;top:10px;width:.5px;height:40px")))
	mux.Handle("/fractional/scaled", page(thinPage("Scaled fraction", "left:9px;top:1px;width:.5px;height:1px")))
	mux.HandleFunc("/xhtml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xhtml+xml; charset=utf-8")
		fmt.Fprint(w, xhtmlFixture)
	})
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

// actFixture is the page actions act on. The query field notes its input
// events as React does, through the value setter of its own instance,
// which a value set on the element instead of through the prototype's
// setter goes past.
const actFixture = `<!doctype html><meta charset="utf-8"><title>Act</title>
<button id="b">Press</button>
<button disabled>Off</button>
<input type="checkbox" id="c" aria-label="Agree">
<form action="/act/sent"><input name="q" aria-label="Query"></form>
<form action="/act/sent"><input id="taken" name="t" aria-label="Taken"></form>
<textarea aria-label="Notes"></textarea>
<div id="ed" contenteditable="true" aria-label="Editor">old</div>
<iframe id="fr" srcdoc="<button onclick='parent.framed = 1'>Framed press</button>"></iframe>
<script>
window.seen = [];
for (const type of ["pointerdown", "mousedown", "pointerup", "mouseup", "click"])
  b.addEventListener(type, () => seen.push(type));
const q = document.querySelector("[name=q]");
let tracked = "";
Object.defineProperty(q, "value", {
  get() { return Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value").get.call(this); },
  set(v) { tracked = v; Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value").set.call(this, v); },
});
window.inputs = 0;
q.addEventListener("input", () => { if (q.value !== tracked) inputs++; });
document.querySelector("textarea").addEventListener("input", (e) => { window.notes = e.target.value; });
taken.addEventListener("keydown", (e) => { if (e.key === "Enter") { e.preventDefault(); window.enter = true; } });
</script>`

const snapFixture = `<!doctype html><meta charset="utf-8"><title>Snap</title>
<h1>Fixture</h1>
<div role="heading">Untagged heading</div>
<div style="display:contents"><button id="contents">Inside contents</button></div>
<button>Twin</button><button>Twin</button>
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
<iframe id="editable" srcdoc="<body contenteditable aria-label='Editable body'><p>Draft</p></body>"></iframe>
<iframe id="designed"></iframe>
<script>
const root = document.getElementById("host").attachShadow({ mode: "open" });
root.innerHTML = '<span id="lbl">Shadow label</span><input aria-labelledby="lbl">';
// A frame with no address that a script fills, as an editor's.
document.getElementById("scripted").contentDocument.body.innerHTML = "<button>Scripted frame</button>";
// An editor's frame, its body edited whole: by contenteditable, and in
// design mode.
const designed = document.getElementById("designed").contentDocument;
designed.body.setAttribute("aria-label", "Design mode");
designed.designMode = "on";
</script>`

// scaledFixture places frames scaled by their own transforms and by an
// ancestor's, from their top left corners.
const scaledFixture = `<!doctype html><meta charset="utf-8"><title>Scaled</title>
<style>body { margin: 0 } iframe, div { position: absolute; transform-origin: 0 0 } div iframe { position: static; display: block }</style>
<iframe id="outer" style="left:40px;top:30px;width:400px;height:300px;border:4px solid #888;padding:6px;transform:scale(0.5, 0.75)" src="/scaled/outer"></iframe>
<div style="left:300px;top:30px;transform:scale(2)"><iframe id="grown" style="width:100px;height:60px;border:2px solid #888;padding:3px" src="/scaled/grown"></iframe></div>`

// scaledPage is a scaled frame's page: a button named name, then more.
func scaledPage(name, more string) string {
	return `<!doctype html><meta charset="utf-8"><style>body { margin: 0 } button { margin: 10px; width: 80px; height: 40px; padding: 0 } iframe { position: absolute; transform-origin: 0 0 }</style>
<button>` + name + `</button>` + more
}

// fractionalFixture places frames whose layout sizes are fractions of a
// pixel, two unscaled and one scaled from its top left corner.
const fractionalFixture = `<!doctype html><meta charset="utf-8"><title>Fractional</title>
<style>body { margin: 0 } iframe, div { position: absolute; border: 0; padding: 0; transform-origin: 0 0 } div iframe { position: static; display: block }</style>
<iframe id="fraction" style="left:40px;top:300px;width:400.4px;height:60px" src="/fractional/fraction"></iframe>
<div style="left:480px;top:300px;width:300px"><iframe id="percent" style="width:33.33%;height:60px" src="/fractional/percent"></iframe></div>
<iframe id="scaled" style="left:40px;top:380px;width:10.4px;height:4px;transform:scale(50)" src="/fractional/scaled"></iframe>`

// thinPage is a fractional frame's page: a button named name, where at
// places it, with no text to show.
func thinPage(name, at string) string {
	return `<!doctype html><html style="overflow:hidden"><meta charset="utf-8"><body style="margin:0"><button style="position:absolute;border:0;padding:0;font-size:0;` + at + `">` + name + `</button>`
}

// In XHTML a name keeps its case: of these, only input, button and
// select, with its option, are form controls.
const xhtmlFixture = `<?xml version="1.0" encoding="utf-8"?>
<html xmlns="http://www.w3.org/1999/xhtml"><head><title>XHTML</title></head><body>
<input aria-label="Real input"/>
<button>Real button</button>
<select><option>Real option</option></select>
<Input aria-label="Mixed input"/>
<Input role="button" aria-label="Mixed role"/>
<Button>Mixed button</Button>
<SELECT aria-label="Shouted select"><OPTION>Shouted option</OPTION></SELECT>
<Option>Mixed option</Option>
<TEXTAREA aria-label="Shouted area"></TEXTAREA>
</body></html>`
