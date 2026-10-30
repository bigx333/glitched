package main

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"glitched/internal/browse"
)

// fakePage is a tab's page in a test: it notes what the bar asked of it.
type fakePage struct {
	loaded    []string
	calls     []string
	back, fwd bool
	loading   bool
	html      string // what LoadHTML showed
	closed    bool
	events    browse.Events // what it tells its tab
}

func (p *fakePage) LoadURL(url string) error { p.loaded = append(p.loaded, url); return nil }
func (p *fakePage) Reload()                  { p.calls = append(p.calls, "reload") }
func (p *fakePage) Stop()                    { p.calls = append(p.calls, "stop") }
func (p *fakePage) GoBack()                  { p.calls = append(p.calls, "back") }
func (p *fakePage) GoForward()               { p.calls = append(p.calls, "forward") }
func (p *fakePage) CanGoBack() bool          { return p.back }
func (p *fakePage) CanGoForward() bool       { return p.fwd }
func (p *fakePage) IsLoading() bool          { return p.loading }
func (p *fakePage) LoadHTML(html string)     { p.html = html }
func (p *fakePage) Close()                   { p.closed = true }
func (p *fakePage) Show(c *ui.Context) ui.Element {
	return ui.Box(c).Label("Page")
}
func (p *fakePage) EvalContext(context.Context, string) (any, error) { return nil, nil }

// browsing is the app with its browser's pages made by the test, and the
// addresses they were made at.
func browsing(t *testing.T) (*app, *ui.Tester, *[]string) {
	a := offline(t)
	var made []string
	a.browser.open = func(url string, ev browse.Events) (browse.Page, error) {
		p := &fakePage{events: ev}
		made = append(made, url)
		return p, nil
	}
	return a, ui.NewTester(a.view, 1320, 860), &made
}

// pageOf is a tab's page, made by the test.
func pageOf(tab *browserTab) *fakePage { return tab.page.(*fakePage) }

func click(t *testing.T, tt *ui.Tester, label string) {
	t.Helper()
	if err := tt.Click(label); err != nil {
		t.Fatal(err)
	}
}

// The pane opens on the dev server; ⌘T opens a tab with the keyboard in
// an empty address, which loads what's typed there, over https.
func TestBrowserNewTab(t *testing.T) {
	a, tt, made := browsing(t)
	click(t, tt, "Toggle preview  ⇧⌘P")
	if !slices.Equal(*made, []string{defaultPreviewURL}) {
		t.Fatalf("pages made at %q, want the dev server's", *made)
	}
	if tt.HasText("Close tab") {
		t.Error("a single tab shows a tab strip")
	}
	tt.Key(ui.Cmd, ui.KeyT)
	tt.Frame()
	if len(a.browser.tabs) != 2 || a.browser.active != 1 || !tt.HasText("Type an address above") {
		t.Fatalf("%d tabs, showing %d: want a new, empty second tab", len(a.browser.tabs), a.browser.active)
	}
	if len(*made) != 1 {
		t.Errorf("an empty tab made a page")
	}
	if !tt.Focused("Address") {
		t.Error("the new tab's address doesn't have the keyboard")
	}
	tt.Type("example.com")
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	tab := a.browser.tabs[1]
	if tab.url != "https://example.com" || !slices.Equal(*made, []string{defaultPreviewURL, "https://example.com"}) {
		t.Fatalf("tab at %q, pages made at %q", tab.url, *made)
	}
	if p := pageOf(tab); len(p.loaded) != 0 {
		t.Errorf("a new page loaded %q again", p.loaded)
	}
	// Typed again, the page goes there.
	tt.Key(ui.Cmd, ui.KeyL)
	tt.Frame()
	if !tt.Focused("Address") {
		t.Fatal("⌘L didn't give the address the keyboard")
	}
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("localhost:3000")
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if p := pageOf(tab); !slices.Equal(p.loaded, []string{"http://localhost:3000"}) {
		t.Errorf("loaded %q, want the local server over http", p.loaded)
	}
}

// Tabs show in a strip once there are two; a click shows one, and closing
// moves to its neighbour. Closing the last closes the pane, which opens
// again on the dev server.
func TestBrowserTabs(t *testing.T) {
	a, tt, _ := browsing(t)
	a.newTab("https://go.dev")
	a.newTab("https://example.com")
	tt.Frame()
	b := &a.browser
	if len(b.tabs) != 3 || b.active != 2 || !a.showPreview {
		t.Fatalf("%d tabs, showing %d, pane %v", len(b.tabs), b.active, a.showPreview)
	}
	b.tabs[1].title = "The Go Programming Language"
	tt.Frame()
	for _, s := range []string{"localhost:5173", "The Go Programming Language", "example.com"} {
		if !tt.HasText(s) {
			t.Errorf("no tab %q", s)
		}
	}
	click(t, tt, "localhost:5173")
	if b.active != 0 {
		t.Fatalf("showing tab %d after clicking the first", b.active)
	}
	click(t, tt, "The Go Programming Language")
	ids := func() (ids []int) {
		for _, t := range b.tabs {
			ids = append(ids, t.id)
		}
		return ids
	}
	a.closeTab(1)
	if !slices.Equal(ids(), []int{1, 3}) || b.active != 1 {
		t.Fatalf("tabs %v showing %d after closing the second, want [1 3] showing the last", ids(), b.active)
	}
	// The tab shown closing, the last, shows the one before it.
	a.newTab("https://go.dev/doc/")
	a.closeTab(b.active)
	if !slices.Equal(ids(), []int{1, 3}) || b.active != 1 {
		t.Fatalf("tabs %v showing %d after closing the last, shown", ids(), b.active)
	}
	// Closed from the strip, the tab shown gives the pane to the next.
	b.active = 0
	tt.Frame()
	click(t, tt, "Close tab")
	tt.Frame()
	if !slices.Equal(ids(), []int{3}) || b.active != 0 || tt.HasText("Close tab") {
		t.Fatalf("tabs %v showing %d: want the last left, with no strip", ids(), b.active)
	}
	if !tt.HasText("Page") {
		t.Error("the pane doesn't show the tab left")
	}
	a.closeTab(0)
	if len(b.tabs) != 0 || a.showPreview {
		t.Fatalf("%d tabs, pane %v, after closing them all", len(b.tabs), a.showPreview)
	}
	tt.Frame()
	click(t, tt, "Toggle preview  ⇧⌘P")
	tt.Frame()
	if len(b.tabs) != 1 || b.tabs[0].url != defaultPreviewURL || b.tabs[0].id != 5 {
		t.Fatalf("reopened on %+v, want a new dev server tab", b.tabs)
	}
}

// Back and forward follow the page's history; the button stops a page
// loading, ⌘R reloads it, and reload loads the address of a page that
// failed, which a page saying so replaced.
func TestBrowserNavigation(t *testing.T) {
	a, tt, _ := browsing(t)
	click(t, tt, "Toggle preview  ⇧⌘P")
	p := pageOf(a.browser.current())
	if err := tt.Click("Back"); err != nil {
		t.Fatal(err)
	}
	if len(p.calls) != 0 {
		t.Fatalf("back went %q with no history", p.calls)
	}
	p.back = true
	tt.Frame()
	click(t, tt, "Back")
	click(t, tt, "Reload  ⌘R")
	p.loading = true
	tt.Frame()
	click(t, tt, "Stop")
	// ⌘R reloads a page loading, as one stuck on a request.
	tt.Key(ui.Cmd, ui.KeyR)
	if !slices.Equal(p.calls, []string{"back", "reload", "stop", "reload"}) {
		t.Errorf("calls %q", p.calls)
	}
	p.loading = false
	tab := a.browser.current()
	tab.failed = true
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyR)
	if !slices.Equal(p.loaded, []string{defaultPreviewURL}) {
		t.Errorf("reloading a failed page loaded %q, want its address", p.loaded)
	}
	// The page going elsewhere is its address, unless being typed over.
	a.tabNavigated(tab, "http://localhost:5173/about")
	if tab.draft != "http://localhost:5173/about" || tab.failed {
		t.Errorf("address %q, failed %v after navigating", tab.draft, tab.failed)
	}
	a.browser.typing, tab.draft = true, "exa"
	a.tabNavigated(tab, "http://localhost:5173/")
	if tab.draft != "exa" || tab.url != "http://localhost:5173/" {
		t.Errorf("address %q at %q: typing was lost", tab.draft, tab.url)
	}
}

func TestTabLabel(t *testing.T) {
	for _, tc := range []struct {
		tab  browserTab
		want string
	}{
		{browserTab{}, "New tab"},
		{browserTab{url: "https://go.dev/doc/"}, "go.dev"},
		{browserTab{url: "https://go.dev/", title: "Go"}, "Go"},
		{browserTab{url: "http://localhost:5173", title: "about:blank"}, "localhost:5173"},
		{browserTab{url: "http://localhost:5173", title: "Vite App", failed: true}, "localhost:5173"},
	} {
		if got := tabLabel(&tc.tab); got != tc.want {
			t.Errorf("tabLabel(%+v) = %q, want %q", tc.tab, got, tc.want)
		}
	}
}

// What a page tells its tab: its title names the tab, a failed load shows
// a page saying so, at the address that failed, and a window it asks for
// opens a tab, for the web only.
func TestBrowserPageEvents(t *testing.T) {
	a, tt, made := browsing(t)
	click(t, tt, "Toggle preview  ⇧⌘P")
	tab := a.browser.current()
	p := pageOf(tab)
	p.events.Titled("Vite App")
	p.events.Failed("http://localhost:5173/missing", "Connection refused")
	if !tab.failed || tab.url != "http://localhost:5173/missing" || !strings.Contains(p.html, "Nothing running at http://localhost:5173/missing") {
		t.Errorf("failed %v at %q, page %q", tab.failed, tab.url, p.html)
	}
	if tabLabel(tab) != "localhost:5173" {
		t.Errorf("a failed tab is named %q", tabLabel(tab))
	}
	p.events.Navigated("http://localhost:5173/")
	if tab.failed || tabLabel(tab) != "Vite App" {
		t.Errorf("failed %v, named %q after loading", tab.failed, tabLabel(tab))
	}
	p.events.Opened("javascript:alert(1)")
	a.newTab("https://example.com/")
	p.events.Opened("https://go.dev/")
	tt.Frame()
	// The tab opens after the page's, not after the one shown.
	b := &a.browser
	if len(b.tabs) != 3 || b.active != 1 || b.current().url != "https://go.dev/" || (*made)[len(*made)-1] != "https://go.dev/" {
		t.Fatalf("%d tabs, showing %d at %q after the page opened windows", len(b.tabs), b.active, b.current().url)
	}
	a.closeTab(0)
	if !p.closed {
		t.Error("closing a tab left its page open")
	}
}

// The terminal keeps Ctrl+T, L and R while it has the keyboard, where
// they are the browser's ⌘ keys (Windows, Linux). The drawer notes its
// focus as it builds (chat.go); here the keys meet the flag alone.
func TestBrowserKeysLeaveTheTerminal(t *testing.T) {
	a, _, _ := browsing(t)
	a.showPreview = true
	p := &fakePage{}
	a.browser.tabs = []*browserTab{{id: 1, url: defaultPreviewURL, page: p}}
	tt := ui.NewTester(a.browserShortcuts, 400, 300)
	a.terminalFocused = true
	for _, k := range []ui.Key{ui.KeyT, ui.KeyL, ui.KeyR} {
		tt.Key(ui.Cmd, k)
	}
	if len(a.browser.tabs) != 1 || a.browser.focusAddress || len(p.calls) != 0 {
		t.Fatalf("the terminal's keys reached the browser: %d tabs, address %v, calls %q", len(a.browser.tabs), a.browser.focusAddress, p.calls)
	}
	a.terminalFocused = false
	for _, k := range []ui.Key{ui.KeyT, ui.KeyL, ui.KeyR} {
		tt.Key(ui.Cmd, k)
	}
	if len(a.browser.tabs) != 2 || !a.browser.focusAddress {
		t.Errorf("out of the terminal: %d tabs, address %v", len(a.browser.tabs), a.browser.focusAddress)
	}
}

// A tab whose page the engine couldn't make says why, and tries again
// as it reloads, or as an address is typed.
func TestBrowserRetriesThePage(t *testing.T) {
	a, tt, made := browsing(t)
	fail := true
	open := a.browser.open
	a.browser.open = func(url string, ev browse.Events) (browse.Page, error) {
		if fail {
			return nil, errors.New("no WebView2 runtime")
		}
		return open(url, ev)
	}
	click(t, tt, "Toggle preview  ⇧⌘P")
	tab := a.browser.current()
	if tab.err == "" || !tt.HasText("The preview could not start: no WebView2 runtime") {
		t.Fatalf("err %q: the tab doesn't say why it has no page", tab.err)
	}
	fail = false
	click(t, tt, "Reload  ⌘R")
	if tab.page == nil || tab.err != "" || len(*made) != 1 {
		t.Fatalf("reload left the tab without a page: err %q", tab.err)
	}
	// Typed, an address tries again too.
	tab2 := a.browser.add("", 1)
	fail = true
	a.goTo(tab2, "example.com")
	if tab2.err == "" {
		t.Fatal("the engine failing left no error")
	}
	fail = false
	a.goTo(tab2, "example.org")
	if tab2.page == nil || tab2.err != "" {
		t.Errorf("a new address didn't try again: err %q", tab2.err)
	}
}

// The bar has one of reload and stop: stop while the page loads, reload
// otherwise.
func TestBrowserReloadOrStop(t *testing.T) {
	a, tt, _ := browsing(t)
	click(t, tt, "Toggle preview  ⇧⌘P")
	p := pageOf(a.browser.current())
	for _, loading := range []bool{false, true, false} {
		p.loading = loading
		tt.Frame()
		_, reload := tt.Find("Reload  ⌘R")
		_, stop := tt.Find("Stop")
		if reload == loading || stop != loading {
			t.Errorf("loading %v: reload shown %v, stop shown %v", loading, reload, stop)
		}
	}
}
