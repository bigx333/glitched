package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
}

func newAgentBrowser(a *app, onView func(func())) *agentBrowser {
	return &agentBrowser{a: a, onView: onView, settle: 300 * time.Millisecond, load: 15 * time.Second, snaps: map[int]webtarget.Snapshot{}}
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
	ln, err := browsermcp.Listen(a.dir)
	if err != nil {
		log.Print(err)
		return
	}
	ab := newAgentBrowser(a, a.post)
	go browsermcp.Accept(ln, ab.call)
	a.stopAgents = ln.Close
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
		err := ab.view(ctx, func() error {
			var err error
			if t, err = ab.tab(args.Tab); err == nil {
				p, err = ab.page(t)
			}
			return err
		})
		if err != nil {
			return "", err
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
	ab.onView(func() { done <- fn() })
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return errors.New("The Glitched app didn't answer in time.")
	}
}

// tab is the tab of id, or the one shown for none. On the view's thread.
func (ab *agentBrowser) tab(id *int) (*browserTab, error) {
	b := &ab.a.browser
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
			u, err := agentURL(args.URL)
			if err != nil {
				return err
			}
			ab.a.newTab(u)
			opened = b.tabs[b.active]
			p, err = ab.page(opened)
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
		return ab.loaded(ctx, opened, p)
	}
	var out string
	err = ab.view(ctx, func() error {
		out = ab.list()
		return nil
	})
	return out, err
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
		ab.show(t)
		if u != "" {
			ab.a.goTo(t, u)
			p, err = ab.page(t)
			return err
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
		case "reload":
			ab.a.reload(t)
		default:
			return fmt.Errorf("browser_navigate's action is back, forward or reload, not %q.", args.Action)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return ab.loaded(ctx, t, p)
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
	err = ab.view(ctx, func() error {
		var err error
		if t, err = ab.tab(&id); err != nil {
			return err
		}
		ab.show(t)
		p, err = ab.page(t)
		return err
	})
	if err != nil {
		return "", err
	}
	if tool == "browser_click" {
		err = webtarget.Click(ctx, p, snap, args.Ref)
	} else {
		err = webtarget.Type(ctx, p, snap, args.Ref, args.Text, args.Submit)
	}
	if err != nil {
		return "", errors.New(strings.TrimPrefix(err.Error(), "webtarget: "))
	}
	return ab.loaded(ctx, t, p)
}

// loaded waits for t's page to show what an action started loading, and
// then to load, for a while, and snapshots it.
func (ab *agentBrowser) loaded(ctx context.Context, t *browserTab, p browse.Page) (string, error) {
	select {
	case <-time.After(ab.settle):
	case <-ctx.Done():
		return "", ctx.Err()
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
		select {
		case <-time.After(100 * time.Millisecond):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if failed {
		return "", fmt.Errorf("Tab %d couldn't load %s; the tab says why.", t.id, why)
	}
	out, err := ab.snapshot(ctx, t, p, false)
	if loading && err == nil {
		out = "The page is still loading.\n" + out
	}
	return out, err
}

// snapshot reads t's page, keeping its refs as the tab's latest.
func (ab *agentBrowser) snapshot(ctx context.Context, t *browserTab, p browse.Page, full bool) (string, error) {
	s, err := webtarget.Take(ctx, p, webtarget.Options{Full: full})
	if err != nil {
		return "", fmt.Errorf("Tab %d's page couldn't be read: %s", t.id, strings.TrimPrefix(err.Error(), "webtarget: "))
	}
	ab.mu.Lock()
	ab.snaps[t.id] = s
	ab.mu.Unlock()
	var out strings.Builder
	fmt.Fprintf(&out, "Tab %d: %q\nURL: %s\n", t.id, s.Title, s.URL)
	if s.Truncated {
		out.WriteString("The snapshot was cut short; its end says where.\n")
	}
	out.WriteString("\n")
	out.WriteString(s.Text)
	return out.String(), nil
}
