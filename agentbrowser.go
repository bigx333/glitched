package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"glitched/internal/browse"
	"glitched/internal/browsermcp"
	"glitched/internal/webtarget"
)

// agentBrowser answers agents' browser tools (package browsermcp), which
// reach the app on its browser socket: they act on the user's tabs, in
// the pane, so the user sees what an agent does. Each tool showing a
// page shows the pane and that tab, as a tab the user opens would; a
// snapshot reads a tab where it is.
//
// Tools come on goroutines of their own. What they do to tabs runs on the
// view's thread (onView); what they run in pages (webtarget) runs on
// theirs, as browse.Page.EvalContext asks.
type agentBrowser struct {
	a *app
	// onView runs fn on the thread that builds the view.
	onView func(fn func())
	// settle is how long an action waits for a load it may have started
	// to show, and load the longest it waits for one to end.
	settle, load time.Duration

	mu sync.Mutex
	// snaps are each tab's latest snapshot, by tab id: the refs the
	// agent may act on, which name their tab by their epoch.
	snaps map[int]webtarget.Snapshot
	// pages hold each tab's page for one script at a time, so the
	// snapshot the page last took is the one with the highest epoch.
	pages map[int]*sync.Mutex
}

func newAgentBrowser(a *app, onView func(func())) *agentBrowser {
	return &agentBrowser{a: a, onView: onView, settle: 300 * time.Millisecond, load: 15 * time.Second, snaps: map[int]webtarget.Snapshot{}, pages: map[int]*sync.Mutex{}}
}

// toolTime bounds a tool call, past which the agent hears it timed out.
const toolTime = 60 * time.Second

// serveAgents listens for agents' tools on the app's browser socket,
// until the app quits.
func (a *app) serveAgents() {
	// Nothing authenticates a caller on the socket: the directory's mode
	// is its gate, as for the engine's (daemon.Start).
	if err := os.MkdirAll(a.dir, 0o700); err != nil {
		log.Print(err)
		return
	}
	// A directory made earlier may be wider; the engine, which narrows
	// it too, may not have started yet.
	if err := os.Chmod(a.dir, 0o700); err != nil {
		log.Print(err)
		return
	}
	ln, err := browsermcp.Listen(a.dir)
	if err != nil {
		log.Print(err)
		return
	}
	ab := newAgentBrowser(a, a.post)
	go func() {
		// The socket stops only as the app quits, or for an error the
		// agents can't see: they hear the app isn't open.
		if err := browsermcp.Accept(ln, ab.call); err != nil {
			log.Printf("browser socket: %v", err)
		}
	}()
	a.stopAgents = ln.Close
}

// serveBrowserMCP serves the browser to an agent on in and out, and
// reports true, when the app runs as browser-mcp (args, after the
// program's name); false otherwise, for the app to run.
func serveBrowserMCP(args []string, in io.Reader, out io.Writer) (bool, error) {
	if len(args) == 0 || args[0] != "browser-mcp" {
		return false, nil
	}
	return true, browsermcp.Main(context.Background(), args[1:], in, out)
}

// toolArgs are every tool's arguments.
type toolArgs struct {
	Action string `json:"action"`
	URL    string `json:"url"`
	Tab    *int   `json:"tab"`
	Full   bool   `json:"full"`
	Ref    string `json:"ref"`
	Text   string `json:"text"`
	Submit bool   `json:"submit"`
}

func (ab *agentBrowser) call(ctx context.Context, tool string, raw json.RawMessage) (string, error) {
	var args toolArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("%s's arguments: %v", tool, err)
	}
	ctx, cancel := context.WithTimeout(ctx, toolTime)
	defer cancel()
	switch tool {
	case "browser_tabs":
		return ab.tabs(ctx, args)
	case "browser_navigate":
		return ab.navigate(ctx, args)
	case "browser_snapshot":
		var t *browserTab
		var p browse.Page
		made := false
		err := ab.view(ctx, func() error {
			var err error
			if t, err = ab.tab(args.Tab); err == nil {
				made = t.page == nil
				p, err = ab.page(t)
			}
			return err
		})
		if err != nil {
			return "", err
		}
		// A tab never shown has just made its page, which is loading.
		if made {
			return ab.loaded(ctx, t, p, args.Full)
		}
		return ab.snapshot(ctx, t, p, args.Full)
	case "browser_click", "browser_type":
		return ab.act(ctx, tool, args)
	}
	return "", fmt.Errorf("no tool %s", tool)
}

// view runs fn on the view's thread, and returns what it did.
func (ab *agentBrowser) view(ctx context.Context, fn func() error) error {
	done := make(chan error, 1)
	ab.onView(func() {
		// A call the agent gave up on, or that timed out, does nothing
		// more, as its tab opening late would surprise the user.
		if ctx.Err() != nil {
			done <- errGaveUp
			return
		}
		done <- fn()
	})
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		// What the view did as the call ended, it did: the agent hears so.
		select {
		case err := <-done:
			return err
		default:
			return errGaveUp
		}
	}
}

// errGaveUp is why a call ends that the agent cancelled or ran out of
// time.
var errGaveUp = errors.New("The browser didn't finish in time, or the call was cancelled.")

// wait waits d, or until ctx ends.
func wait(ctx context.Context, d time.Duration) error {
	select {
	case <-time.After(d):
		return nil
	case <-ctx.Done():
		return errGaveUp
	}
}

// tab is the tab of id, or the one shown for none. On the view's thread.
func (ab *agentBrowser) tab(id *int) (*browserTab, error) {
	b := &ab.a.browser
	ab.forget()
	if id == nil {
		if len(b.tabs) == 0 {
			return nil, errors.New("The browser has no tabs: open one with browser_tabs or browser_navigate.")
		}
		return b.tabs[b.active], nil
	}
	for _, t := range b.tabs {
		if t.id == *id {
			return t, nil
		}
	}
	return nil, fmt.Errorf("No tab %d is open: browser_tabs lists them.", *id)
}

// page is t's page, made if it has none yet. On the view's thread.
func (ab *agentBrowser) page(t *browserTab) (browse.Page, error) {
	p := ab.a.tabPage(t)
	switch {
	case p != nil:
		return p, nil
	case t.err != "":
		return nil, fmt.Errorf("Tab %d's page could not start: %s", t.id, t.err)
	case t.url == "":
		return nil, fmt.Errorf("Tab %d has no address yet: load one with browser_navigate.", t.id)
	}
	return nil, errors.New("The browser has no pages here.")
}

// show shows the pane and t in it, as the user sees what an agent does.
// On the view's thread.
func (ab *agentBrowser) show(t *browserTab) {
	a := ab.a
	if i := slices.Index(a.browser.tabs, t); i >= 0 {
		a.browser.active = i
	}
	a.showPreview, a.showDiff = true, false
}

// agentURL is the address an agent asked for, as the bar takes it typed:
// http or https only.
func agentURL(typed string) (string, error) {
	u := normalizeURL(typed)
	p, err := url.Parse(u)
	if u == "" || err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.Host == "" {
		return "", fmt.Errorf("%q is not an http or https address.", typed)
	}
	return u, nil
}

func (ab *agentBrowser) tabs(ctx context.Context, args toolArgs) (string, error) {
	b := &ab.a.browser
	var opened *browserTab
	var p browse.Page
	err := ab.view(ctx, func() error {
		switch args.Action {
		case "list":
		case "new":
			if args.URL == "" {
				return errors.New("browser_tabs new needs a url.")
			}
			u, err := agentURL(args.URL)
			if err != nil {
				return err
			}
			shown, diff, active := ab.a.showPreview, ab.a.showDiff, b.active
			ab.a.newTab(u)
			opened = b.tabs[b.active]
			if p, err = ab.page(opened); err != nil {
				// A tab whose page can't start goes, and the pane is as
				// it was.
				ab.a.closeTab(b.active)
				ab.a.showPreview, ab.a.showDiff, b.active = shown, diff, min(active, max(len(b.tabs)-1, 0))
			}
			return err
		case "select":
			t, err := ab.need(args.Tab, "select")
			if err != nil {
				return err
			}
			ab.show(t)
		case "close":
			t, err := ab.need(args.Tab, "close")
			if err != nil {
				return err
			}
			ab.a.closeTab(slices.Index(b.tabs, t))
			ab.mu.Lock()
			delete(ab.snaps, t.id)
			ab.mu.Unlock()
		default:
			return fmt.Errorf("browser_tabs takes list, new, select or close, not %q.", args.Action)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if opened != nil {
		return ab.loaded(ctx, opened, p, false)
	}
	var out string
	err = ab.view(ctx, func() error {
		out = ab.list()
		return nil
	})
	// Only a view that ran has written out.
	if err != nil {
		return "", err
	}
	return out, nil
}

// need is the tab of id, which the action needs. On the view's thread.
func (ab *agentBrowser) need(id *int, action string) (*browserTab, error) {
	if id == nil {
		return nil, fmt.Errorf("browser_tabs %s needs a tab.", action)
	}
	return ab.tab(id)
}

// list lists the tabs, each its id, title and address, and which shows.
// On the view's thread.
func (ab *agentBrowser) list() string {
	ab.forget()
	a := ab.a
	b := &a.browser
	if len(b.tabs) == 0 {
		return "No tabs are open."
	}
	var s strings.Builder
	for i, t := range b.tabs {
		fmt.Fprintf(&s, "- tab %d: %q %s", t.id, tabLabel(t), t.url)
		if i == b.active {
			s.WriteString(" (shown")
			if !a.showPreview {
				s.WriteString(", the pane closed")
			}
			s.WriteString(")")
		}
		s.WriteString("\n")
	}
	return s.String()
}

func (ab *agentBrowser) navigate(ctx context.Context, args toolArgs) (string, error) {
	var u string
	switch {
	case args.URL != "" && args.Action != "":
		return "", errors.New("browser_navigate takes a url or an action, not both.")
	case args.URL != "":
		var err error
		if u, err = agentURL(args.URL); err != nil {
			return "", err
		}
	case args.Action == "":
		return "", errors.New("browser_navigate needs a url, or an action: back, forward or reload.")
	case args.Action != "back" && args.Action != "forward" && args.Action != "reload":
		return "", fmt.Errorf("browser_navigate's action is back, forward or reload, not %q.", args.Action)
	}
	var t *browserTab
	var p browse.Page
	err := ab.view(ctx, func() error {
		b := &ab.a.browser
		var err error
		switch {
		case args.Tab != nil:
			if t, err = ab.tab(args.Tab); err != nil {
				return err
			}
		case u != "":
			t = b.current() // the dev server's, where there is none
		default:
			if t, err = ab.tab(nil); err != nil {
				return err
			}
		}
		switch {
		case u != "":
			ab.a.goTo(t, u)
		case args.Action == "reload":
			// Reload tries again a page that could not start.
			ab.a.reload(t)
		}
		if p, err = ab.page(t); err != nil {
			return err
		}
		switch args.Action {
		case "back":
			if !p.CanGoBack() {
				return fmt.Errorf("Tab %d has no page to go back to.", t.id)
			}
			p.GoBack()
		case "forward":
			if !p.CanGoForward() {
				return fmt.Errorf("Tab %d has no page to go forward to.", t.id)
			}
			p.GoForward()
		}
		ab.show(t)
		return nil
	})
	if err != nil {
		return "", err
	}
	return ab.loaded(ctx, t, p, false)
}

func (ab *agentBrowser) act(ctx context.Context, tool string, args toolArgs) (string, error) {
	epoch, _, err := webtarget.ParseRef(args.Ref)
	if err != nil {
		return "", err
	}
	ab.mu.Lock()
	id, snap := 0, webtarget.Snapshot{}
	for tab, s := range ab.snaps {
		if s.Epoch == epoch {
			id, snap = tab, s
		}
	}
	ab.mu.Unlock()
	if id == 0 {
		return "", fmt.Errorf("%s is from no tab's latest snapshot; take a new snapshot.", args.Ref)
	}
	var t *browserTab
	var p browse.Page
	var before []*browserTab
	err = ab.view(ctx, func() error {
		var err error
		if t, err = ab.tab(&id); err != nil {
			return err
		}
		before = slices.Clone(ab.a.browser.tabs)
		p, err = ab.page(t)
		return err
	})
	if err != nil {
		return "", err
	}
	unlock := ab.lock(t.id)
	if tool == "browser_click" {
		err = webtarget.Click(ctx, p, snap, args.Ref)
	} else {
		err = webtarget.Type(ctx, p, snap, args.Ref, args.Text, args.Submit)
	}
	unlock()
	if err != nil {
		return "", agentError(err)
	}
	// Shown once it acted, so a refusal leaves the pane as it was.
	if err := ab.view(ctx, func() error { ab.show(t); return nil }); err != nil {
		return "", err
	}
	out, err := ab.loaded(ctx, t, p, false)
	if err != nil {
		return "", err
	}
	// The page may have opened tabs, which the agent hears of, as it
	// reads only the page it acted on.
	var opened strings.Builder
	err = ab.view(ctx, func() error {
		for _, o := range ab.a.browser.tabs {
			if !slices.Contains(before, o) {
				fmt.Fprintf(&opened, "The page opened tab %d at %s.\n", o.id, o.url)
			}
		}
		return nil
	})
	// Only a view that ran has written opened.
	if err != nil {
		return out, nil
	}
	return opened.String() + out, nil
}

// agentError is err as an agent reads it: the page's own words, without
// the engine's and the package's prefixes, and a page that stopped
// answering, as one showing a dialog does, said so.
func agentError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return errors.New("The page didn't answer in time. It may be showing a dialog (alert, confirm) the user must answer in the browser pane.")
	}
	msg := err.Error()
	for _, prefix := range []string{"mygo: javascript error: ", "Error: ", "webtarget: "} {
		msg = strings.TrimPrefix(msg, prefix)
	}
	return errors.New(msg)
}

// loaded waits for t's page to show what an action started loading, and
// then to load, for a while, and snapshots it.
func (ab *agentBrowser) loaded(ctx context.Context, t *browserTab, p browse.Page, full bool) (string, error) {
	if err := wait(ctx, ab.settle); err != nil {
		return "", err
	}
	deadline := time.Now().Add(ab.load)
	var loading, failed bool
	var why string
	for {
		err := ab.view(ctx, func() error {
			if !slices.Contains(ab.a.browser.tabs, t) {
				return fmt.Errorf("Tab %d was closed.", t.id)
			}
			loading, failed, why = p.IsLoading(), t.failed, t.url
			return nil
		})
		if err != nil {
			return "", err
		}
		if !loading || time.Now().After(deadline) {
			break
		}
		if err := wait(ctx, 100*time.Millisecond); err != nil {
			return "", err
		}
	}
	if failed {
		return "", fmt.Errorf("Tab %d couldn't load %s; the tab says why.", t.id, why)
	}
	out, err := ab.snapshot(ctx, t, p, full)
	if loading && err == nil {
		out = "The page is still loading.\n" + out
	}
	return out, err
}

// snapshot reads t's page, keeping its refs as the tab's latest.
func (ab *agentBrowser) snapshot(ctx context.Context, t *browserTab, p browse.Page, full bool) (string, error) {
	unlock := ab.lock(t.id)
	s, err := webtarget.Take(ctx, p, webtarget.Options{Full: full})
	if err == nil {
		ab.keep(t.id, s)
	}
	unlock()
	if err != nil {
		return "", fmt.Errorf("Tab %d's page couldn't be read: %w", t.id, agentError(err))
	}
	var out strings.Builder
	fmt.Fprintf(&out, "Tab %d: %q\nURL: %s\n", t.id, s.Title, s.URL)
	if s.Truncated {
		out.WriteString("The snapshot was cut short; its end says where.\n")
	}
	out.WriteString("\n")
	out.WriteString(s.Text)
	return out.String(), nil
}

// keep keeps s as tab id's latest snapshot, unless a later one, taken
// as s was, is kept already: its refs are those the page acts on.
func (ab *agentBrowser) keep(id int, s webtarget.Snapshot) {
	ab.mu.Lock()
	defer ab.mu.Unlock()
	if old, ok := ab.snaps[id]; ok && old.Epoch > s.Epoch {
		return
	}
	ab.snaps[id] = s
}

// lock holds tab id's page for a script until the func it returns.
func (ab *agentBrowser) lock(id int) func() {
	ab.mu.Lock()
	m := ab.pages[id]
	if m == nil {
		m = &sync.Mutex{}
		ab.pages[id] = m
	}
	ab.mu.Unlock()
	m.Lock()
	return m.Unlock
}

// forget drops the snapshots of tabs no longer open, however they
// closed. It runs on the view's thread, where the tabs are.
func (ab *agentBrowser) forget() {
	ab.mu.Lock()
	defer ab.mu.Unlock()
	for id := range ab.snaps {
		if !slices.ContainsFunc(ab.a.browser.tabs, func(t *browserTab) bool { return t.id == id }) {
			delete(ab.snaps, id)
		}
	}
	for id := range ab.pages {
		if !slices.ContainsFunc(ab.a.browser.tabs, func(t *browserTab) bool { return t.id == id }) {
			delete(ab.pages, id)
		}
	}
}
