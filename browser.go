package main

import (
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/browse"
	"glitched/internal/plugins"
	"glitched/internal/plugins/widget"
)

// The preview pane is a browser: tabs, each a page of its own, made the
// first time it shows and kept while another shows, so that going back
// to one finds its page where it was. The first is the project's dev
// server; links opening windows open tabs. A tab keeps its id while it is
// open, which agents will name it by (docs/browser.md).
//
// The tabs know their pages only through package browse, the seam to the
// engines that show them, which the app gives it (main.go): the system's
// web views (package sysweb).

// browser is the pane's tabs, and the one shown.
type browser struct {
	tabs   []*browserTab
	active int
	nextID int
	// typing is set while the address has the keyboard, which a page
	// navigating then doesn't type over; focusAddress gives it the
	// keyboard in the next frame, as ⌘L and a new tab do.
	typing, focusAddress bool
	focusAddressHold     int
	// shown is whether the pane was open as the last frame began: its
	// closing lets go of the address (letGo).
	shown bool
	// open makes a tab's page: the engine's, which the app gives it once it
	// has a window; a test's; nil, where there is no window.
	open browse.Opener
	// opens are when each line of pages opened tabs, within openWindow, by
	// the id of the user's tab at its head; kept as that tab closes, so a
	// line can't start over by closing it.
	opens map[int][]time.Time
	// clock tells the time as a page opens a window, which comes before
	// the frame it asks for, so the frame's time can be long past;
	// time.Now if nil, a test's otherwise.
	clock func() time.Time
}

// browserTab is a tab: its page, made as it first shows, and what its bar
// shows of it. A tab with no address yet has no page.
type browserTab struct {
	id    int
	page  tabPage // nil before it shows, or where there is no window
	url   string  // where the page is, or was asked to go
	draft string  // the address as typed
	title string
	// failed is set while the page shows failedPage, which reload
	// replaces with the address's.
	failed bool
	// err is why the engine could not make the tab's page.
	err string
	// opener is the id of the user's tab at the head of the line of pages
	// that opened this one, 0 for a tab of the user's: a page opening
	// itself in a loop is one line, within one budget.
	opener int
}

// What pages may open, as tabs, counted by the line of pages that opened
// them from one of the user's tabs: at most maxOpenedTabs open at once, and
// openBurst within openWindow. More, as a page opening windows in a loop,
// or itself, asks for, open nothing.
const (
	maxOpenedTabs = 10
	openBurst     = 5
	openWindow    = 10 * time.Second
)

// tabPage is a tab's page, in whichever engine shows it.
type tabPage = browse.Page

// tabKey keys a part of a tab's row by the tab, which keeps its state as
// tabs before it close.
type tabKey struct {
	id   int
	part string
}

// current returns the tab shown, opening the dev server's where there
// is none.
func (b *browser) current() *browserTab {
	if len(b.tabs) == 0 {
		b.add(defaultPreviewURL, 0)
	}
	return b.tabs[b.active]
}

// add opens a tab at url before the tab at index at, and shows it.
func (b *browser) add(url string, at int) *browserTab {
	b.nextID++
	t := &browserTab{id: b.nextID, url: url, draft: url}
	b.tabs = slices.Insert(b.tabs, at, t)
	b.active = at
	return t
}

// newTab opens a tab after the one shown, or the first, and shows the
// pane: at url, or for none at an empty address, which takes the
// keyboard.
func (a *app) newTab(url string) {
	b := &a.browser
	at := 0
	if len(b.tabs) > 0 {
		at = b.active + 1
	}
	b.add(url, at)
	if url == "" {
		b.askFocusAddress()
	}
	a.showPreview, a.showDiff = true, false
}

// askFocusAddress gives the address the keyboard. It holds the request
// for a few frames: a plugin's input with it draws asynchronously.
func (b *browser) askFocusAddress() {
	b.focusAddress, b.focusAddressHold = true, 4
}

// closeTab closes the tab at index i and its page. Closing the last
// closes the pane, which opens again on the dev server.
func (a *app) closeTab(i int) {
	b := &a.browser
	if t := b.tabs[i]; t.page != nil {
		t.page.Close()
	}
	b.tabs = slices.Delete(b.tabs, i, i+1)
	switch {
	case len(b.tabs) == 0:
		b.active = 0
		a.showPreview = false
	case i < b.active || b.active == len(b.tabs):
		b.active--
	}
}

// tabPage returns the tab's page, made the first time it shows at an
// address; nil before, or where there is no window, as in tests.
func (a *app) tabPage(t *browserTab) tabPage {
	if t.page != nil || t.url == "" || t.err != "" || a.browser.open == nil {
		return t.page
	}
	p, err := a.browser.open(t.url, a.tabEvents(t))
	if err != nil {
		t.err = err.Error()
		return nil
	}
	t.page = p
	return p
}

// tabEvents follow a tab's page: where it goes, its title, its failures,
// which a page says, the windows it opens, which open as tabs, and its
// loads starting and ending.
func (a *app) tabEvents(t *browserTab) browse.Events {
	// A tab closed hears nothing more, whatever its page still tells, as
	// adapters promise (browse.Page.Close).
	open := func() bool { return slices.Contains(a.browser.tabs, t) }
	return browse.Events{
		// Nothing to note: the event comes through the window's Update,
		// which builds the view again, where the bar's stop turns back to
		// reload and the tab's spinner stops as the load ends.
		Changed: func() {},
		Navigated: func(url string) {
			if open() {
				a.tabNavigated(t, url)
			}
		},
		Titled: func(title string) {
			if open() {
				t.title = title
			}
		},
		Failed: func(url, why string) {
			if !open() {
				return
			}
			if url != "" {
				t.url = url
			}
			t.failed = true
			if t.page != nil {
				t.page.LoadHTML(failedPage(t.url, why))
			}
		},
		Opened: func(url string) {
			if !open() {
				return
			}
			if strings.HasPrefix(url, "https://") || strings.HasPrefix(url, "http://") {
				a.openFrom(t, url)
			}
		},
	}
}

// openFrom opens a tab at url, as the page of tab from asked, after it,
// within what a page may open (maxOpenedTabs). The tab shows if from was
// the tab shown; one opened from a tab in the background stays there.
func (a *app) openFrom(from *browserTab, url string) {
	b := &a.browser
	i := slices.Index(b.tabs, from)
	now := time.Now()
	if b.clock != nil {
		now = b.clock()
	}
	if i < 0 || !b.mayOpen(from, now) {
		return
	}
	line := lineOf(from)
	b.opens[line] = append(b.opens[line], now)
	was := b.active
	b.add(url, i+1).opener = line
	if was != i {
		if was > i {
			was++
		}
		b.active = was
		return
	}
	a.showPreview, a.showDiff = true, false
}

// lineOf is the id of the user's tab at the head of t's line of pages.
func lineOf(t *browserTab) int {
	if t.opener != 0 {
		return t.opener
	}
	return t.id
}

// mayOpen reports whether the page of tab from may open another tab now,
// within its line's budget.
func (b *browser) mayOpen(from *browserTab, now time.Time) bool {
	if b.opens == nil {
		b.opens = map[int][]time.Time{}
	}
	line := lineOf(from)
	b.opens[line] = slices.DeleteFunc(b.opens[line], func(at time.Time) bool { return now.Sub(at) >= openWindow })
	if len(b.opens[line]) == 0 {
		delete(b.opens, line)
	}
	if len(b.opens[line]) >= openBurst {
		return false
	}
	n := 0
	for _, t := range b.tabs {
		if t.opener == line {
			n++
		}
	}
	return n < maxOpenedTabs
}

// letGo lets go of the address as the pane closes, once, as the frame
// after begins (view): what was typed and not entered goes, and the
// address is the tab's again, which reload and "Open in browser" use. The
// pane only out of sight, as the diff covers it (⌘D, Toggle Diff) or a
// screen without the chat shows, keeps the draft for its return; "Open in
// diff" and the palette close the pane, which lets go of it.
func (b *browser) letGo(open bool) {
	closed := b.shown && !open
	b.shown = open
	if !closed {
		return
	}
	b.typing = false
	if len(b.tabs) > 0 {
		t := b.tabs[b.active]
		t.draft = t.url
	}
}

// tabNavigated notes that the tab's page went to url: its address, unless
// it is being typed in.
func (a *app) tabNavigated(t *browserTab, url string) {
	t.url, t.failed = url, false
	b := &a.browser
	if shown := len(b.tabs) > 0 && b.tabs[b.active] == t; !b.typing || !shown {
		t.draft = url
	}
}

// goTo loads what was typed in the tab's address.
func (a *app) goTo(t *browserTab, typed string) {
	u := normalizeURL(typed)
	if u == "" {
		return
	}
	t.url, t.draft, t.failed, t.err = u, u, false, ""
	// A tab's first page loads the address it's made with.
	made := t.page != nil
	if p := a.tabPage(t); p != nil && made {
		p.LoadURL(u)
	}
}

// reload loads the tab's page again, loading or not. A page that failed
// loads its address, as a server started since answers it; one the engine
// couldn't make, it tries to make again.
func (a *app) reload(t *browserTab) {
	if t.page == nil {
		t.err = ""
		a.tabPage(t)
		return
	}
	switch p := t.page; {
	case t.failed:
		p.LoadURL(t.url)
	default:
		p.Reload()
	}
}

// tabLabel names a tab by its page's title, else its address's host.
func tabLabel(t *browserTab) string {
	if t.title != "" && !t.failed && !strings.HasPrefix(t.title, "about:") {
		return t.title
	}
	if u, err := url.Parse(t.url); err == nil && u.Host != "" {
		return u.Host
	}
	if t.url != "" {
		return t.url
	}
	return "New tab"
}

type previewTabState struct {
	ID         int    `json:"id"`
	URL        string `json:"url"`
	Draft      string `json:"draft"`
	Title      string `json:"title"`
	Label      string `json:"label"`
	Active     bool   `json:"active"`
	Loading    bool   `json:"loading"`
	Failed     bool   `json:"failed,omitempty"`
	Error      string `json:"error,omitempty"`
	CanBack    bool   `json:"canBack"`
	CanForward bool   `json:"canForward"`
}

type previewPanelState struct {
	Part       string            `json:"part"`
	Tabs       []previewTabState `json:"tabs"`
	Active     int               `json:"active"`
	URL        string            `json:"url"`
	Draft      string            `json:"draft"`
	Loading    bool              `json:"loading"`
	Failed     bool              `json:"failed,omitempty"`
	Error      string            `json:"error,omitempty"`
	Page       bool              `json:"page"`
	CanBack    bool              `json:"canBack"`
	CanForward bool              `json:"canForward"`
	Focus      bool              `json:"focus,omitempty"`
	DevURL     string            `json:"devUrl"`
	BarHeight  float32           `json:"barHeight"`
	BarRight   float32           `json:"barRight"`
}

// previewState is what the preview panel's component copies are given:
// every tab, and the state of the one shown.
func (a *app) previewState(bar ui.TitleBar, part string) previewPanelState {
	b := &a.browser
	t := b.current()
	p := a.tabPage(t)
	out := previewPanelState{
		Part: part, Active: t.id, URL: t.url, Draft: t.draft, Loading: p != nil && p.IsLoading(),
		Failed: t.failed, Error: t.err, Page: p != nil, CanBack: p != nil && p.CanGoBack(), CanForward: p != nil && p.CanGoForward(),
		Focus: b.focusAddress, DevURL: defaultPreviewURL, BarHeight: max(bar.Height, headerHeight), BarRight: bar.Right,
	}
	for i, tab := range b.tabs {
		tp := tab.page
		out.Tabs = append(out.Tabs, previewTabState{
			ID: tab.id, URL: tab.url, Draft: tab.draft, Title: tab.title, Label: tabLabel(tab), Active: i == b.active,
			Loading: tp != nil && tp.IsLoading(), Failed: tab.failed, Error: tab.err,
			CanBack: tp != nil && tp.CanGoBack(), CanForward: tp != nil && tp.CanGoForward(),
		})
	}
	return out
}

// browserTabNamed is the tab a plugin's browser call names.
func (a *app) browserTabNamed(id float64) (*browserTab, int, error) {
	for i, t := range a.browser.tabs {
		if float64(t.id) == id {
			return t, i, nil
		}
	}
	return nil, -1, errors.New("no such preview tab")
}

// browserEffect is the preview panel's navigation and tab calls. They act
// only on the panel in this window; browse.Page and the agent browser stay
// Glitched's own.
func (a *app) browserEffect(e plugins.Effect) (any, error) {
	var in struct {
		Tab  float64
		URL  string
		Text string
	}
	if err := json.Unmarshal(e.Args, &in); err != nil {
		return nil, errors.New(e.Type + " needs a tab")
	}
	if e.Type == "browser.newTab" {
		u := strings.TrimSpace(in.URL)
		if u != "" {
			u = normalizeURL(u)
			if u == "" {
				return nil, errors.New("browser.newTab needs an http or https address")
			}
		}
		a.newTab(u)
		return nil, nil
	}
	t, i, err := a.browserTabNamed(in.Tab)
	if err != nil {
		return nil, err
	}
	switch e.Type {
	case "browser.navigate":
		u := normalizeURL(in.URL)
		if u == "" {
			return nil, errors.New("browser.navigate needs an http or https address")
		}
		a.goTo(t, in.URL)
	case "browser.back":
		if p := a.tabPage(t); p == nil || !p.CanGoBack() {
			return nil, errors.New("the tab can't go back")
		} else {
			p.GoBack()
		}
	case "browser.forward":
		if p := a.tabPage(t); p == nil || !p.CanGoForward() {
			return nil, errors.New("the tab can't go forward")
		} else {
			p.GoForward()
		}
	case "browser.reload":
		a.reload(t)
	case "browser.stop":
		if p := a.tabPage(t); p != nil {
			p.Stop()
		}
	case "browser.draft":
		if len(in.Text) > 2048 {
			return nil, errors.New("an address is at most 2048 bytes")
		}
		t.draft = in.Text
		a.browser.typing = true
	case "browser.closeTab":
		a.closeTab(i)
	case "browser.activateTab":
		a.browser.active = i
	default:
		return nil, errors.New("unknown browser call " + e.Type)
	}
	return nil, nil
}

func (a *app) previewPanel(c *ui.Context) {
	ui.Column(c).Width(a.previewWidth()).FillHeight().Children(func() {
		bar := c.TitleBar()
		ctx := func(part string) func(plugins.ThreadRecord) any {
			return func(plugins.ThreadRecord) any { return a.previewState(bar, part) }
		}
		a.pluginComponent(c, "thread.panel.preview", nil, componentCopy{
			key: "preview/header", box: func() ui.Element { return ui.Column(c).Key("component:thread.panel.preview:header").Shrink(0) }, context: ctx("header"),
			native: func(*widget.Node) { a.previewHeader(c, bar) },
		})
		if len(a.browser.tabs) > 1 {
			a.pluginComponent(c, "thread.panel.preview", nil, componentCopy{
				key: "preview/tabs", box: func() ui.Element { return ui.Column(c).Key("component:thread.panel.preview:tabs").Shrink(0) }, context: ctx("tabs"),
				native: func(*widget.Node) { a.tabStrip(c) },
			})
		} else {
			// No copy is drawn: the plugin hears its strip went.
			a.pluginComponent(c, "thread.panel.preview", nil)
		}
		a.pluginComponent(c, "thread.panel.preview", nil, componentCopy{
			key: "preview/body", box: func() ui.Element {
				return ui.Column(c).Key("component:thread.panel.preview:body").Grow(1).Shrink(1).Basis(0)
			}, context: ctx("body"),
			native: func(*widget.Node) { a.previewBody(c) },
		})
		// Last, over what is built before it; beside the page, not over it,
		// as the page's web view takes the pointer over its bounds.
		a.previewEdge(c)
	})
	// The flag is for a few frames' address, native or a plugin's: the
	// plugin's drawing answers asynchronously. Focus itself then belongs
	// to the input, so the flag doesn't focus it again.
	if b := &a.browser; b.focusAddressHold > 0 {
		b.focusAddressHold--
		if b.focusAddressHold == 0 {
			b.focusAddress = false
		}
	}
}

// previewHeader is the panel's navigation bar as Glitched draws it without
// a plugin: back and forward, reload or stop, the address, a new tab, the
// system browser and Close.
func (a *app) previewHeader(c *ui.Context, bar ui.TitleBar) {
	b := &a.browser
	t := b.current()
	p := a.tabPage(t)
	// Clear of the window's buttons on Windows and Linux, at its right.
	head := ui.Row(c).Height(max(bar.Height, headerHeight)).Padding(0, 8+bar.Right, 0, 10).Gap(4).AlignItems(ui.Center)
	head.DragWindow().Children(func() {
		back := a.iconButton(c, "back", icArrowLeft, "Back", false).Padding(5)
		fwd := a.iconButton(c, "forward", icArrowRight, "Forward", false).Padding(5)
		// One button, which stops a page loading and reloads one that
		// isn't: each call of iconButton builds one.
		loading := p != nil && p.IsLoading()
		var reload ui.Element
		if loading {
			reload = a.iconButton(c, "stop", icX, "Stop", false).Padding(5)
		} else {
			reload = a.iconButton(c, "reload", icRotate, "Reload  ⌘R", false).Padding(5)
		}
		back.Disabled(p == nil || !p.CanGoBack())
		fwd.Disabled(p == nil || !p.CanGoForward())
		reload.Disabled(p == nil && t.err == "")
		if p != nil {
			if back.Clicked() {
				p.GoBack()
			}
			if fwd.Clicked() {
				p.GoForward()
			}
		}
		if reload.Clicked() {
			if loading {
				p.Stop()
			} else {
				a.reload(t)
			}
		}
		// The address: Enter goes there; one without a scheme is
		// https, or http on this machine and the local network.
		in := ui.TextInput(c.Key(tabKey{t.id, "url"}), &t.draft).Grow(1).FontSize(12).Font("monospace").
			Padding(5, 10).Radius(8).Label("Address").Placeholder("Type an address")
		if b.focusAddress {
			in.Focus()
			b.focusAddress, b.focusAddressHold = false, 0
		}
		b.typing = in.Focused()
		if in.Submitted() && strings.TrimSpace(t.draft) != "" {
			a.goTo(t, t.draft)
		}
		if a.iconButton(c, "new-tab", icPlus, "New tab  ⌘T", false).Padding(5).Clicked() {
			a.newTab("")
		}
		if a.iconButton(c, "external", icExternal, "Open in browser", false).Padding(5).Disabled(t.url == "").Clicked() {
			c.OpenURL(t.url)
		}
		if a.iconButton(c, "close-preview", icX, "Close preview", false).Padding(5).Clicked() {
			a.showPreview = false
		}
	})
}

// previewBody is the current tab's page, its error, or its empty page.
// As the native browser.webview it is asked for only once the plugin knows
// there is a page, so the web view itself is what shows.
func (a *app) previewBody(c *ui.Context) {
	b := &a.browser
	t := b.current()
	p := a.tabPage(t)
	// Below its glass header, the panel on the sidebar's color.
	ui.Column(c).Grow(1).Background(palette.sidebar).BorderWidth(1, 0, 0, 1).BorderColor(palette.border).Children(func() {
		switch {
		case p != nil:
			p.Show(c).Grow(1)
		case t.err != "":
			ui.Text(c, "The preview could not start: "+t.err).TextColor(palette.muted).Margin(ui.Auto).Padding(24)
		case t.url == "":
			a.emptyTab(c, t)
		default:
			ui.Text(c, "The preview shows here, in the app's window.").TextColor(palette.muted).Margin(ui.Auto)
		}
	})
}

// tabStrip is the row of tabs under the bar, shown once there are two:
// each its page's title, with a spinner while it loads, and a close
// button.
func (a *app) tabStrip(c *ui.Context) {
	b := &a.browser
	// A tab shows or closes once the row is built, which goes through the
	// tabs, each drawn as the one shown or not as the row began.
	picked, closing := -1, -1
	defer func() {
		if picked >= 0 {
			b.active = picked
		}
		if closing >= 0 {
			a.closeTab(closing)
		}
	}()
	ui.Row(c).Padding(0, 8, 6, 8).Gap(4).Children(func() {
		for i, t := range b.tabs {
			on := i == b.active
			label := tabLabel(t)
			cell := ui.Row(c.Key(tabKey{t.id, "tab"})).Grow(1).Shrink(1).MinWidth(0).MaxWidth(200).Height(28).
				Padding(0, 3, 0, 0).Radius(7).AlignItems(ui.Center).Transition(hoverFade)
			switch {
			case on:
				cell.Background(palette.active)
			case cell.Hovered():
				cell.Background(palette.hover)
			}
			cell.Children(func() {
				pick := a.tip(c, ui.ButtonBase(c.Key(tabKey{t.id, "pick"})).Grow(1).Shrink(1).MinWidth(0).FillHeight().
					Gap(6).Padding(0, 4, 0, 10).AlignItems(ui.Center).Cursor(ui.CursorPointer).Label(label), label)
				pick.Children(func() {
					if t.page != nil && t.page.IsLoading() {
						spin := ui.Icon(c, icLoader).FontSize(11).TextColor(palette.blue)
						spin.Rotate(spin.Loop("spin", 800*time.Millisecond, ui.Linear) * 360)
					}
					text := ui.Text(c, label).FontSize(12).SingleLine().Shrink(1).MinWidth(0)
					if on {
						text.TextColor(palette.text)
					} else {
						text.TextColor(palette.muted)
					}
				})
				if pick.Clicked() {
					picked = i
				}
				if a.tip(c, ghost(c, tabKey{t.id, "close"}, false).Padding(4).Label("Close tab"), "Close tab").Children(func() {
					ui.Icon(c, icX).FontSize(11)
				}).Clicked() {
					closing = i
				}
			})
		}
	})
}

// emptyTab is a new tab's page before it has an address: what to type,
// and the dev server, a click away.
func (a *app) emptyTab(c *ui.Context, t *browserTab) {
	ui.Column(c).Margin(ui.Auto).Gap(10).AlignItems(ui.Center).Children(func() {
		ui.Icon(c, icGlobe).FontSize(28).TextColor(palette.muted)
		ui.Text(c, "Type an address above").TextColor(palette.muted)
		if headerButton(c, "dev-server").Label("Open " + defaultPreviewURL).Children(func() {
			ui.Text(c, strings.TrimPrefix(defaultPreviewURL, "http://")).FontSize(12).Font("monospace")
		}).Clicked() {
			a.goTo(t, defaultPreviewURL)
		}
	})
}

// browserShortcuts are the browser's keys while the pane shows: ⌘T opens
// a tab, ⌘L takes the address, ⌘R reloads. The terminal keeps them while
// it has the keyboard: on Windows and Linux they are Ctrl+T, L and R, a
// shell's.
func (a *app) browserShortcuts(c *ui.Context) {
	if a.terminalFocused {
		return
	}
	if c.Shortcut(ui.Cmd, ui.KeyT) {
		a.newTab("")
	}
	if !a.showPreview {
		return
	}
	if c.Shortcut(ui.Cmd, ui.KeyL) {
		a.browser.askFocusAddress()
	}
	if c.Shortcut(ui.Cmd, ui.KeyR) {
		a.reload(a.browser.current())
	}
}
