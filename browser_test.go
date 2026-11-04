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
	a.browser.current() // the dev server's
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

// The pane closing while the address has the keyboard lets go of what
// was typed: opened again, the bar shows the tab's address, the one reload
// and "Open in browser" use, and a page navigating sets it.
func TestBrowserAddressLetGoAsThePaneCloses(t *testing.T) {
	for _, how := range []string{"click", "keys"} {
		a, tt, _ := browsing(t)
		click(t, tt, "Toggle preview  ⇧⌘P")
		tt.Key(ui.Cmd, ui.KeyL)
		tt.Frame()
		if !tt.Focused("Address") {
			t.Fatalf("%s: ⌘L didn't give the address the keyboard", how)
		}
		tt.Key(ui.Cmd, ui.KeyA)
		tt.Type("exa")
		tt.Frame()
		tab := a.browser.current()
		if tab.draft != "exa" || !a.browser.typing {
			t.Fatalf("%s: address %q, typing %v", how, tab.draft, a.browser.typing)
		}
		if how == "click" {
			click(t, tt, "Close preview")
		} else {
			tt.Key(ui.Cmd|ui.Shift, ui.KeyP)
		}
		tt.Frame()
		tt.Frame()
		if a.showPreview || a.browser.typing || tab.draft != defaultPreviewURL {
			t.Errorf("%s: pane %v, typing %v, address %q after it closed", how, a.showPreview, a.browser.typing, tab.draft)
		}
		a.tabNavigated(tab, "http://localhost:5173/about")
		click(t, tt, "Toggle preview  ⇧⌘P")
		tt.Frame()
		if tab.draft != "http://localhost:5173/about" || a.browser.typing {
			t.Errorf("%s: opened again, address %q, typing %v", how, tab.draft, a.browser.typing)
		}
	}
}

// The pane only out of sight, as the diff takes its place, keeps a draft
// not entered: the diff closed, the bar shows what was typed.
func TestBrowserAddressKeptBehindTheDiff(t *testing.T) {
	a, tt, _ := browsing(t)
	click(t, tt, "Toggle preview  ⇧⌘P")
	tt.Key(ui.Cmd, ui.KeyL)
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("exa")
	tt.Frame()
	tab := a.browser.current()
	if tab.draft != "exa" {
		t.Fatalf("address %q, want what was typed", tab.draft)
	}
	// As the menu's Toggle Diff, the diff takes the pane's place.
	a.showDiff = true
	tt.Frame()
	tt.Frame()
	if !a.showPreview || tab.draft != "exa" {
		t.Fatalf("behind the diff: pane %v, address %q", a.showPreview, tab.draft)
	}
	a.showDiff = false
	tt.Frame()
	if tab.draft != "exa" || tab.url != defaultPreviewURL {
		t.Errorf("the diff closed: address %q at %q, want the draft kept", tab.draft, tab.url)
	}
	tt.Key(ui.Cmd|ui.Shift, ui.KeyP)
	tt.Frame()
	tt.Frame()
	if a.showPreview || tab.draft != defaultPreviewURL {
		t.Errorf("the pane closed: pane %v, address %q, want the tab's", a.showPreview, tab.draft)
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
	b := &a.browser
	if len(b.tabs) != 1 {
		t.Fatalf("%d tabs after a page asked for a javascript: window", len(b.tabs))
	}
	// Opened by the tab shown, the tab shows.
	p.events.Opened("https://example.com/")
	tt.Frame()
	if len(b.tabs) != 2 || b.active != 1 || b.current().url != "https://example.com/" || (*made)[len(*made)-1] != "https://example.com/" {
		t.Fatalf("%d tabs, showing %d at %q after the page shown opened a window", len(b.tabs), b.active, b.current().url)
	}
	// Opened by a tab in the background, the tab opens after it, and the
	// one shown stays, with the keyboard it was given (⌘L).
	b.focusAddress = true
	p.events.Opened("https://go.dev/")
	if len(b.tabs) != 3 || b.tabs[1].url != "https://go.dev/" || b.active != 2 || b.current().url != "https://example.com/" || !b.focusAddress {
		t.Fatalf("%d tabs, showing %d at %q, address %v after a page in the background opened a window", len(b.tabs), b.active, b.current().url, b.focusAddress)
	}
	tt.Frame()
	if (*made)[len(*made)-1] != "https://example.com/" {
		t.Errorf("pages made at %q: the tab in the background made one before it shows", *made)
	}
	a.closeTab(0)
	if !p.closed {
		t.Error("closing a tab left its page open")
	}
}

// A tab closed hears nothing its page still tells: no failure page, no
// address, no tab opened.
func TestBrowserClosedTabHearsNothing(t *testing.T) {
	a, tt, _ := browsing(t)
	click(t, tt, "Toggle preview  ⇧⌘P")
	tab := a.browser.current()
	p := pageOf(tab)
	a.newTab("https://example.com/")
	tt.Frame()
	a.closeTab(0)
	p.events.Failed("http://localhost:5173/missing", "Connection refused")
	p.events.Opened("https://go.dev/")
	p.events.Navigated("http://localhost:5173/about")
	p.events.Titled("Vite App")
	deliver(tt, p.events.Changed)
	b := &a.browser
	if len(b.tabs) != 1 || b.current().url != "https://example.com/" {
		t.Errorf("%d tabs, showing %q: a closed tab's page opened one", len(b.tabs), b.current().url)
	}
	if p.html != "" || tab.failed || tab.url != defaultPreviewURL || tab.title != "" {
		t.Errorf("a closed tab heard its page: failed %v at %q, title %q, page %q", tab.failed, tab.url, tab.title, p.html)
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

// ⌘T with no tab open opens the new tab alone, not the dev server's too.
func TestBrowserNewTabWithNoneOpen(t *testing.T) {
	a, tt, made := browsing(t)
	tt.Key(ui.Cmd, ui.KeyT)
	tt.Frame()
	b := &a.browser
	if len(b.tabs) != 1 || b.tabs[0].url != "" || b.active != 0 || !a.showPreview || len(*made) != 0 {
		t.Fatalf("%d tabs %v, pane %v, pages made at %q: want one new, empty tab", len(b.tabs), urls(b.tabs), a.showPreview, *made)
	}
	if !tt.Focused("Address") {
		t.Error("the new tab's address doesn't have the keyboard")
	}
}

// Leaving the chat with the terminal focused, which then doesn't build,
// gives the browser its keys back.
func TestBrowserKeysOutOfTheChat(t *testing.T) {
	a, tt, _ := browsing(t)
	a.terminalFocused = true
	a.router.Push("/settings/general")
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyT)
	tt.Frame()
	if len(a.browser.tabs) == 0 || !a.showPreview {
		t.Errorf("⌘T on the settings page, the terminal focused before: %d tabs, pane %v", len(a.browser.tabs), a.showPreview)
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

// deliver tells the page's event as an adapter does: through the window's
// Update, which builds a frame after it (sysweb), or not at all, where the
// tab doesn't follow the event.
func deliver(tt *ui.Tester, event func()) {
	if event != nil {
		event()
		tt.Frame()
	}
}

// A load ending shows as it ends: the bar's stop turns back to reload with
// no input after it.
func TestBrowserRedrawsAsALoadEnds(t *testing.T) {
	a, tt, _ := browsing(t)
	click(t, tt, "Toggle preview  ⇧⌘P")
	p := pageOf(a.browser.current())
	p.loading = true
	tt.Frame()
	if _, ok := tt.Find("Stop"); !ok {
		t.Fatal("no stop while the page loads")
	}
	p.loading = false
	deliver(tt, p.events.Changed)
	if _, ok := tt.Find("Reload  ⌘R"); !ok {
		t.Error("the load ended and the bar still shows stop")
	}
}

// A page opens at most openBurst tabs within openWindow, and has at most
// maxOpenedTabs of those it opened open at once: a page opening windows in
// a loop opens no more.
func TestBrowserCapsTabsAPageOpens(t *testing.T) {
	a, tt, _ := browsing(t)
	click(t, tt, "Toggle preview  ⇧⌘P")
	b := &a.browser
	from := b.current()
	p := pageOf(from)
	opened := func() (n int) {
		for _, t := range b.tabs {
			if t.opener == from.id {
				n++
			}
		}
		return n
	}
	for range 2 * openBurst {
		p.events.Opened("https://example.com/")
	}
	if n := opened(); n != openBurst {
		t.Fatalf("%d tabs opened by a burst of %d, want %d", n, 2*openBurst, openBurst)
	}
	// Each window later, as many again, up to the cap.
	for range 2 * maxOpenedTabs / openBurst {
		tt.Advance(openWindow)
		tt.Frame()
		for range openBurst {
			p.events.Opened("https://example.com/")
		}
	}
	if n := opened(); n != maxOpenedTabs {
		t.Fatalf("%d tabs opened, want the cap, %d", n, maxOpenedTabs)
	}
	// A tab it opened closing makes room for one.
	a.closeTab(slices.IndexFunc(b.tabs, func(t *browserTab) bool { return t.opener == from.id }))
	tt.Advance(openWindow)
	tt.Frame()
	p.events.Opened("https://go.dev/")
	p.events.Opened("https://go.dev/")
	if n := opened(); n != maxOpenedTabs {
		t.Errorf("%d tabs opened after one closed, want %d", n, maxOpenedTabs)
	}
	// A page it opened is in its line, and opens nothing past the cap.
	other := b.tabs[len(b.tabs)-1]
	b.active = len(b.tabs) - 1
	tt.Advance(openWindow)
	tt.Frame()
	pageOf(other).events.Opened("https://go.dev/")
	if len(b.tabs) != 1+maxOpenedTabs {
		t.Errorf("%d tabs: a page the line opened opened one past its cap", len(b.tabs))
	}
	// A tab of the user's opens its own.
	a.newTab("https://go.dev/")
	tt.Frame()
	mine := b.current()
	pageOf(mine).events.Opened("https://go.dev/doc/")
	if len(b.tabs) != 1+maxOpenedTabs+2 {
		t.Errorf("%d tabs: the user's own tab couldn't open one", len(b.tabs))
	}
}

// A page that opens itself as it loads, each copy shown in turn, is one
// line: it stops at the burst, as one page opening many would.
func TestBrowserCapsALineOfPagesOpeningThemselves(t *testing.T) {
	a, tt, _ := browsing(t)
	click(t, tt, "Toggle preview  ⇧⌘P")
	b := &a.browser
	for range 3 * openBurst {
		from := b.current()
		pageOf(from).events.Opened("https://example.com/")
		tt.Frame()
	}
	if n := len(b.tabs); n != 1+openBurst {
		t.Fatalf("%d tabs after a page opened itself in a loop, want %d", n, 1+openBurst)
	}
}

// urls are the tabs' addresses, for a failure's message.
func urls(ts []*browserTab) []string {
	var out []string
	for _, t := range ts {
		out = append(out, t.url)
	}
	return out
}
