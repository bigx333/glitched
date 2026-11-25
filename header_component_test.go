package main

import (
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// childThreadsShown is tree3 with the thread titled title shown, once the
// plugins have drawn it.
func childThreadsShown(t *testing.T, title string) *harness {
	t.Helper()
	h := tree3(t)
	h.a.select_(h.thread(title))
	h.until("the runs' tokens", func() bool { h.tt.Frame(); return h.a.usage(h.a.current()) > 0 })
	settlePlugins(t, h.a, h.tt, h.p)
	return h
}

// drawnBy is who drew the component name's only copy in the last frame.
func drawnBy(h *harness, name string) string {
	d := h.a.partsDrawn[name]
	switch {
	case d == nil || len(d.copies) != 1:
		return "none"
	case d.copies[0].glitched:
		return "Glitched"
	}
	return d.copies[0].plugin
}

// A child's breadcrumb and the header's usage pill are the built-in Child
// threads', each way back the native thread.crumb, which goes back up; the
// pill says what each part of it is on hover.
func TestTheHeadersChildThreadPartsAreTheBuiltIns(t *testing.T) {
	h := childThreadsShown(t, "Run them")
	for _, name := range []string{"thread.header.breadcrumb", "thread.header.usage"} {
		if by := drawnBy(h, name); by != "child-threads" {
			t.Fatalf("%s is drawn by %s, want the built-in Child threads", name, by)
		}
	}
	for _, back := range []string{"Back to Ship the sidebar sort", "Back to Write the tests"} {
		if _, ok := h.tt.Find(back); !ok {
			t.Fatalf("no %q in the header: %q", back, h.tt.Texts())
		}
	}
	for _, s := range h.tt.Texts() {
		if strings.Contains(s, " draws only in ") {
			t.Fatalf("the built-in placed a native it may not: %q", s)
		}
	}
	h.a.showDiff = true
	h.tt.SetSize(1100, 860)
	h.tt.Frame()
	if _, ok := h.tt.Find("Back to Ship the sidebar sort"); ok {
		t.Error("in its first frame narrow, the breadcrumb still leads to the grandparent")
	}
	settlePlugins(t, h.a, h.tt, h.p)
	if _, ok := h.tt.Find("Back to Ship the sidebar sort"); ok {
		t.Error("narrow, the breadcrumb still leads to the grandparent")
	}
	h.click("Back to Write the tests")
	h.tt.Frame()
	if got := h.a.current().View.Title; got != "Write the tests" {
		t.Fatalf("the crumb went to %q", got)
	}
	h.a.showDiff = false
	h.a.select_(h.thread("Ship the sidebar sort"))
	h.tt.Frame()
	settlePlugins(t, h.a, h.tt, h.p)
	label := ""
	for _, s := range h.tt.Texts() {
		if strings.HasSuffix(s, " with children") {
			label = s
		}
	}
	r, ok := h.tt.Find(label)
	if !ok {
		t.Fatalf("no usage pill with children: %q", h.tt.Texts())
	}
	h.tt.Move(r.X+r.W/2, r.Y+r.H/2)
	h.tt.Advance(2 * time.Second)
	if !slices.ContainsFunc(h.tt.Texts(), func(s string) bool {
		return strings.HasPrefix(s, "This thread ") && strings.Contains(s, ", its child threads ")
	}) {
		t.Fatalf("the pill's tooltip doesn't show: %q", h.tt.Texts())
	}
}

const yourBreadcrumbModule = `export function place({ items }) {
	return items.map(({ context }) => ({ type: "row", gap: 4, children: [
		{ type: "text", text: "Up:" },
		...context.ancestors.map((a) => ({ type: "native", id: "c-" + a.id, name: "thread.crumb", props: { thread: a.id } })),
		{ type: "native", id: "self", name: "thread.crumb", props: { thread: context.thread.id } },
		{ type: "native", id: "nobody", name: "thread.crumb", props: { thread: "nobody" } },
		{ type: "native", id: "title", name: "thread.heading" },
	] }))
}`

// A plugin of yours draws the breadcrumb in place of the built-in, with
// thread.crumb going back only to the threads above the one shown; one
// that fails gives way to the built-in's drawing, and Settings says why.
func TestYourBreadcrumbReplacesTheBuiltIns(t *testing.T) {
	h := childThreadsShown(t, "Run them")
	manifest := strings.Replace(replacing("your-crumbs", "thread.header.breadcrumb"), `"ui.replace"]`, `"ui.replace", "threads:read"]`, 1)
	writeOwnPlugin(t, h.a, "your-crumbs", manifest, yourBreadcrumbModule)
	for _, c := range []string{"ui.replace", "threads:read"} {
		h.a.plugins.SetGranted("your-crumbs", c, true)
	}
	h.a.plugins.Reload()
	settlePlugins(t, h.a, h.tt, h.p)
	if by := drawnBy(h, "thread.header.breadcrumb"); by != "your-crumbs" {
		t.Fatalf("the breadcrumb is drawn by %s, want yours: %v", by, h.a.plugins.Failed("your-crumbs"))
	}
	if !h.tt.HasText("Up:") {
		t.Fatalf("your breadcrumb doesn't show: %q", h.tt.Texts())
	}
	for _, back := range []string{"Back to Ship the sidebar sort", "Back to Write the tests"} {
		if _, ok := h.tt.Find(back); !ok {
			t.Fatalf("no %q in your breadcrumb: %q", back, h.tt.Texts())
		}
	}
	if _, ok := h.tt.Find("Back to Run them"); ok {
		t.Error("thread.crumb drew a way back to the thread shown")
	}
	h.click("Back to Ship the sidebar sort")
	h.tt.Frame()
	if got := h.a.current().View.Title; got != "Ship the sidebar sort" {
		t.Fatalf("your crumb went to %q", got)
	}
	h.a.select_(h.thread("Run them"))
	h.tt.Frame()
	writeOwnPlugin(t, h.a, "your-crumbs", manifest, brokenModule)
	h.a.plugins.Reload()
	settlePlugins(t, h.a, h.tt, h.p)
	if by := drawnBy(h, "thread.header.breadcrumb"); by != "child-threads" {
		t.Fatalf("with yours broken, the breadcrumb is drawn by %s, want the built-in", by)
	}
	if st := h.a.partState(installed(h.a, "your-crumbs"), "thread.header.breadcrumb"); st.word != "Failed" || !strings.Contains(st.why, "broken on purpose") {
		t.Fatalf("Settings says %q: %q", st.word, st.why)
	}
	if err := h.a.plugins.SetEnabled("child-threads", false); err != nil {
		t.Fatal(err)
	}
	settlePlugins(t, h.a, h.tt, h.p)
	if by := drawnBy(h, "thread.header.breadcrumb"); by != "Glitched" {
		t.Fatalf("with yours broken and the built-in off, the breadcrumb is drawn by %s, want Glitched", by)
	}
	if _, ok := h.tt.Find("Back to Write the tests"); !ok {
		t.Fatalf("no way back once yours failed: %q", h.tt.Texts())
	}
}

// A plugin of yours draws the usage pill in place of the built-in, from
// the thread's tokens and its children's.
func TestYourUsagePillReplacesTheBuiltIns(t *testing.T) {
	h := childThreadsShown(t, "Ship the sidebar sort")
	manifest := strings.Replace(replacing("your-usage", "thread.header.usage"), `"ui.replace"]`, `"ui.replace", "threads:read"]`, 1)
	writeOwnPlugin(t, h.a, "your-usage", manifest, `export function place({ items }) {
	return items.map(({ context }) => ({ type: "badge", style: "pill", text: "Σ " + context.total + " of which " + context.tokens, tooltip: "Tokens" }))
}`)
	for _, c := range []string{"ui.replace", "threads:read"} {
		h.a.plugins.SetGranted("your-usage", c, true)
	}
	h.a.plugins.Reload()
	settlePlugins(t, h.a, h.tt, h.p)
	th := h.a.current()
	want := "Σ " + strconv.FormatInt(h.a.usage(th), 10) + " of which " + strconv.FormatInt(th.View.Tokens, 10)
	if by := drawnBy(h, "thread.header.usage"); by != "your-usage" || !h.tt.HasText(want) {
		t.Fatalf("the usage pill is drawn by %s, want yours saying %q: %q", by, want, h.tt.Texts())
	}
}

// A breadcrumb of yours must place the title, thread.heading, once: one
// that leaves it out fails, says why in Settings › Plugins, and gives way
// to the built-in's, which shows it.
func TestYourBreadcrumbMustPlaceTheTitle(t *testing.T) {
	h := childThreadsShown(t, "Run them")
	manifest := strings.Replace(replacing("your-crumbs", "thread.header.breadcrumb"), `"ui.replace"]`, `"ui.replace", "threads:read"]`, 1)
	writeOwnPlugin(t, h.a, "your-crumbs", manifest, `export function place({ items }) {
	return items.map(({ context }) => ({ type: "row", children: context.ancestors.map((a) => ({ type: "native", id: "c-" + a.id, name: "thread.crumb", props: { thread: a.id } })) }))
}`)
	for _, c := range []string{"ui.replace", "threads:read"} {
		h.a.plugins.SetGranted("your-crumbs", c, true)
	}
	h.a.plugins.Reload()
	settlePlugins(t, h.a, h.tt, h.p)
	if by := drawnBy(h, "thread.header.breadcrumb"); by != "child-threads" {
		t.Fatalf("the breadcrumb is drawn by %s, want the built-in", by)
	}
	if st := h.a.partState(installed(h.a, "your-crumbs"), "thread.header.breadcrumb"); st.word != "Failed" || !strings.Contains(st.why, "thread.heading") {
		t.Fatalf("Settings says %q: %q", st.word, st.why)
	}
	if !h.tt.HasText("Run them") {
		t.Fatalf("the title doesn't show: %q", h.tt.Texts())
	}
}

// A copy of the built-in breadcrumb shows, from the first frame the header
// is narrow, only the parent, as the built-in does, never its drawing of
// the header wide.
func TestACopiedBreadcrumbIsNarrowFromItsFirstFrame(t *testing.T) {
	h := childThreadsShown(t, "Run them")
	js, err := os.ReadFile("internal/plugins/bundled/child-threads/main.ts")
	if err != nil {
		t.Fatal(err)
	}
	manifest := strings.Replace(strings.Replace(replacing("your-crumbs", "thread.header.breadcrumb"), `"index.js"`, `"index.ts"`, 1), `"ui.replace"]`, `"ui.replace", "threads:read"]`, 1)
	writeOwnPlugin(t, h.a, "your-crumbs", manifest, string(js))
	for _, c := range []string{"ui.replace", "threads:read"} {
		h.a.plugins.SetGranted("your-crumbs", c, true)
	}
	h.a.plugins.Reload()
	settlePlugins(t, h.a, h.tt, h.p)
	if by := drawnBy(h, "thread.header.breadcrumb"); by != "your-crumbs" {
		t.Fatalf("the breadcrumb is drawn by %s, want your copy: %v", by, h.a.plugins.Failed("your-crumbs"))
	}
	h.a.showDiff = true
	h.tt.SetSize(1100, 860)
	h.tt.Frame()
	if _, ok := h.tt.Find("Back to Ship the sidebar sort"); ok {
		t.Errorf("in its first frame narrow, your copy still leads to the grandparent (drawn by %s)", drawnBy(h, "thread.header.breadcrumb"))
	}
	if _, ok := h.tt.Find("Back to Write the tests"); !ok {
		t.Errorf("in its first frame narrow, no way back to the parent: %q", h.tt.Texts())
	}
}

// A copy may draw a way back twice; each is a button of its own, and
// each goes back.
func TestACopyMayDrawACrumbTwice(t *testing.T) {
	h := childThreadsShown(t, "Run them")
	manifest := strings.Replace(replacing("twice", "thread.header.breadcrumb"), `"ui.replace"]`, `"ui.replace", "threads:read"]`, 1)
	writeOwnPlugin(t, h.a, "twice", manifest, `export function place({ items }) {
  return items.map(({ context }) => {
    const parent = { type: "native", name: "thread.crumb", props: { thread: context.ancestors.at(-1).id } }
    return { type: "row", gap: 8, children: [{ ...parent, id: "a" }, { ...parent, id: "b" }, { type: "native", name: "thread.heading" }] }
  })
}
`)
	for _, c := range []string{"ui.replace", "threads:read"} {
		h.a.plugins.SetGranted("twice", c, true)
	}
	h.a.plugins.Reload()
	settlePlugins(t, h.a, h.tt, h.p)
	if by := drawnBy(h, "thread.header.breadcrumb"); by != "twice" {
		t.Fatalf("the breadcrumb is drawn by %s, want yours: %v", by, h.a.plugins.Failed("twice"))
	}
	n := 0
	for _, s := range h.tt.Texts() {
		if s == "Back to Write the tests" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("%d ways back to the parent, want 2: %q", n, h.tt.Texts())
	}
	h.click("Back to Write the tests")
	h.tt.Frame()
	if got := h.a.current().View.Title; got != "Write the tests" {
		t.Fatalf("the crumb went to %q", got)
	}
}
