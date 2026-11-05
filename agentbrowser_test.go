package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"glitched/internal/browse"
)

// agentPage is a tab's page that answers the page script as it would: a
// snapshot of a link "Docs" and a text box "Search", and actions on them.
type agentPage struct {
	*fakePage
	mu    sync.Mutex
	acts  []map[string]any // the actions asked, with their args
	title string
	// onAct, if set, is what the page does as it acts, or why it won't.
	onAct func() error
}

func (p *agentPage) EvalContext(_ context.Context, code string) (any, error) {
	head, _, _ := strings.Cut(code, ";\n")
	var args map[string]any
	if err := json.Unmarshal([]byte(strings.TrimPrefix(head, "const args = ")), &args); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	switch args["op"] {
	case "snapshot":
		epoch := uint64(args["epoch"].(float64))
		docs, search := fmt.Sprintf("s%de1", epoch), fmt.Sprintf("s%de2", epoch)
		return map[string]any{"epoch": epoch, "url": "https://go.dev/", "title": p.title, "refs": 2,
			"text": `- link "Docs" [ref=` + docs + "]\n" + `- textbox "Search" [ref=` + search + "]",
			"elements": []any{
				map[string]any{"ref": docs, "role": "link", "name": "Docs"},
				map[string]any{"ref": search, "role": "textbox", "name": "Search"},
			}}, nil
	case "act":
		p.acts = append(p.acts, args)
		if p.onAct != nil {
			if err := p.onAct(); err != nil {
				return nil, err
			}
		}
		return map[string]any{"done": true}, nil
	}
	return nil, fmt.Errorf("op %v", args["op"])
}

// agents is the app with pages the test makes, and its agents' browser,
// which runs what the view would on the test's goroutine.
func agents(t *testing.T) (*app, *agentBrowser, *[]*agentPage) {
	a := offline(t)
	var made []*agentPage
	a.browser.open = func(url string, ev browse.Events) (browse.Page, error) {
		p := &agentPage{fakePage: &fakePage{events: ev}, title: "Page " + fmt.Sprint(len(made)+1)}
		p.loaded = append(p.loaded, url)
		made = append(made, p)
		return p, nil
	}
	var mu sync.Mutex
	ab := newAgentBrowser(a, func(fn func()) { mu.Lock(); defer mu.Unlock(); fn() })
	ab.settle, ab.load = 0, time.Second
	return a, ab, &made
}

func callTool(t *testing.T, ab *agentBrowser, tool, args string) (string, error) {
	t.Helper()
	return ab.call(context.Background(), tool, json.RawMessage(args))
}

// An agent loading an address shows the pane on a tab at it, and reads
// the page it loaded, with refs to act on.
func TestAnAgentNavigatesTheBrowser(t *testing.T) {
	a, ab, made := agents(t)
	out, err := callTool(t, ab, "browser_navigate", `{"url":"takealot.com"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !a.showPreview || len(a.browser.tabs) != 1 || a.browser.tabs[0].url != "https://takealot.com" {
		t.Errorf("the pane shows %v, tabs %+v", a.showPreview, a.browser.tabs)
	}
	if len(*made) != 1 || (*made)[0].loaded[0] != "https://takealot.com" {
		t.Errorf("pages made at %v", made)
	}
	if !strings.Contains(out, `Tab 1: "Page 1"`) || !strings.Contains(out, `- link "Docs" [ref=s`) {
		t.Errorf("navigate answered\n%s", out)
	}
	// Another address loads in the same tab's page.
	if _, err := callTool(t, ab, "browser_navigate", `{"url":"https://go.dev/doc"}`); err != nil {
		t.Fatal(err)
	}
	if p := (*made)[0]; len(*made) != 1 || p.loaded[len(p.loaded)-1] != "https://go.dev/doc" {
		t.Errorf("the second address loaded %v", p.loaded)
	}
	if _, err := callTool(t, ab, "browser_navigate", `{"action":"back"}`); err == nil || !strings.Contains(err.Error(), "no page to go back to") {
		t.Errorf("back with nowhere to go: %v", err)
	}
	(*made)[0].back = true
	if _, err := callTool(t, ab, "browser_navigate", `{"action":"back"}`); err != nil {
		t.Fatal(err)
	}
	if calls := (*made)[0].calls; len(calls) != 1 || calls[0] != "back" {
		t.Errorf("back called %v", calls)
	}
}

// Agents load the web, and only the web: no file, script or app address.
func TestAnAgentLoadsOnlyWebAddresses(t *testing.T) {
	a, ab, made := agents(t)
	for _, u := range []string{"javascript:alert(1)", "file:///etc/passwd", "data:text/html,hi", "about:blank", "https://", " "} {
		if _, err := callTool(t, ab, "browser_navigate", `{"url":`+fmt.Sprintf("%q", u)+`}`); err == nil {
			t.Errorf("an agent loaded %q", u)
		}
		if _, err := callTool(t, ab, "browser_tabs", `{"action":"new","url":`+fmt.Sprintf("%q", u)+`}`); err == nil {
			t.Errorf("an agent opened a tab at %q", u)
		}
	}
	if len(*made) != 0 || len(a.browser.tabs) != 0 {
		t.Errorf("pages made: %d, tabs %d", len(*made), len(a.browser.tabs))
	}
}

// A ref names its tab, by its snapshot: acting on it shows that tab and
// asks its page to act as the snapshot named the element.
func TestAnAgentActsByRef(t *testing.T) {
	a, ab, made := agents(t)
	first, err := callTool(t, ab, "browser_navigate", `{"url":"go.dev"}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := callTool(t, ab, "browser_tabs", `{"action":"new","url":"example.com"}`); err != nil {
		t.Fatal(err)
	}
	if a.browser.active != 1 {
		t.Fatalf("the new tab isn't shown: %d", a.browser.active)
	}
	search := refIn(t, first, "Search")
	out, err := callTool(t, ab, "browser_type", `{"ref":"`+search+`","text":"generics","submit":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if a.browser.active != 0 {
		t.Errorf("acting on tab 1's ref shows tab %d", a.browser.tabs[a.browser.active].id)
	}
	acts := (*made)[0].acts
	if len(acts) != 1 || acts[0]["action"] != "type" || acts[0]["ref"] != search || acts[0]["role"] != "textbox" ||
		acts[0]["name"] != "Search" || acts[0]["text"] != "generics" || acts[0]["submit"] != true || len((*made)[1].acts) != 0 {
		t.Errorf("the pages were asked %v and %v", acts, (*made)[1].acts)
	}
	// The answer is the page's next snapshot, whose refs are the tab's
	// latest: the first snapshot's no longer are.
	docs := refIn(t, out, "Docs")
	if _, err := callTool(t, ab, "browser_click", `{"ref":"`+docs+`"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := callTool(t, ab, "browser_click", `{"ref":"`+refIn(t, first, "Docs")+`"}`); err == nil || !strings.Contains(err.Error(), "no tab's latest snapshot") {
		t.Errorf("a ref of an older snapshot: %v", err)
	}
	if _, err := callTool(t, ab, "browser_click", `{"ref":"docs"}`); err == nil || !strings.Contains(err.Error(), "refs look like") {
		t.Errorf("not a ref: %v", err)
	}
	// A tab closed takes its refs with it.
	if _, err := callTool(t, ab, "browser_tabs", `{"action":"close","tab":1}`); err != nil {
		t.Fatal(err)
	}
	if _, err := callTool(t, ab, "browser_click", `{"ref":"`+docs+`"}`); err == nil {
		t.Error("clicked a ref of a closed tab")
	}
}

// refIn is the ref of the element named name in a snapshot's text.
func refIn(t *testing.T, snapshot, name string) string {
	t.Helper()
	_, rest, ok := strings.Cut(snapshot, `"`+name+`" [ref=`)
	ref, _, _ := strings.Cut(rest, "]")
	if !ok || ref == "" {
		t.Fatalf("no ref named %q in\n%s", name, snapshot)
	}
	return ref
}

func TestAnAgentListsShowsAndClosesTabs(t *testing.T) {
	a, ab, _ := agents(t)
	if out, err := callTool(t, ab, "browser_tabs", `{"action":"list"}`); err != nil || out != "No tabs are open." {
		t.Errorf("no tabs: %q, %v", out, err)
	}
	callTool(t, ab, "browser_navigate", `{"url":"go.dev"}`)
	callTool(t, ab, "browser_tabs", `{"action":"new","url":"localhost:5173"}`)
	a.showPreview = false
	out, err := callTool(t, ab, "browser_tabs", `{"action":"list"}`)
	want := "- tab 1: \"go.dev\" https://go.dev\n- tab 2: \"localhost:5173\" http://localhost:5173 (shown, the pane closed)\n"
	if err != nil || out != want {
		t.Errorf("list:\n%s\nwant\n%s", out, want)
	}
	if _, err := callTool(t, ab, "browser_tabs", `{"action":"select","tab":1}`); err != nil {
		t.Fatal(err)
	}
	if !a.showPreview || a.browser.active != 0 {
		t.Errorf("select showed %v, tab index %d", a.showPreview, a.browser.active)
	}
	if _, err := callTool(t, ab, "browser_tabs", `{"action":"select","tab":9}`); err == nil {
		t.Error("selected a tab that isn't open")
	}
	if _, err := callTool(t, ab, "browser_tabs", `{"action":"close"}`); err == nil {
		t.Error("closed no tab")
	}
	out, err = callTool(t, ab, "browser_tabs", `{"action":"close","tab":1}`)
	if err != nil || out != "- tab 2: \"localhost:5173\" http://localhost:5173 (shown)\n" {
		t.Errorf("after closing tab 1: %q, %v", out, err)
	}
	if _, err := callTool(t, ab, "browser_snapshot", `{"tab":1}`); err == nil {
		t.Error("read a closed tab")
	}
}

// An agent's load waits for the page to load, and a page that fails says
// so.
func TestAnAgentWaitsForTheLoad(t *testing.T) {
	a, ab, made := agents(t)
	ab.load = 5 * time.Second
	a.browser.open = func(url string, ev browse.Events) (browse.Page, error) {
		p := &agentPage{fakePage: &fakePage{events: ev, loading: true}, title: "Slow"}
		*made = append(*made, p)
		go func() {
			time.Sleep(300 * time.Millisecond)
			ab.onView(func() { p.loading = false })
		}()
		return p, nil
	}
	start := time.Now()
	if _, err := callTool(t, ab, "browser_navigate", `{"url":"go.dev"}`); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 300*time.Millisecond {
		t.Errorf("navigate answered before the page loaded")
	}
	ab.load = 0
	ab.onView(func() { (*made)[0].loading = true })
	if out, _ := callTool(t, ab, "browser_snapshot", `{}`); strings.Contains(out, "still loading") {
		t.Error("a snapshot waited on a load")
	}
	out, err := callTool(t, ab, "browser_navigate", `{"action":"reload"}`)
	if err != nil || !strings.HasPrefix(out, "The page is still loading.") {
		t.Errorf("a load past the wait: %q, %v", out, err)
	}
	// A page that failed stays failed as its address loads again, until
	// the page goes there.
	ab.onView(func() { (*made)[0].loading = false; (*made)[0].events.Failed("https://go.dev", "the host is down") })
	if _, err := callTool(t, ab, "browser_navigate", `{"action":"reload"}`); err == nil || !strings.Contains(err.Error(), "couldn't load https://go.dev") {
		t.Errorf("a page that failed: %v", err)
	}
}

// What an agent asks that fails shows nothing: the pane stays as the user
// left it.
func TestAnAgentsFailedNavigationShowsNothing(t *testing.T) {
	a, ab, made := agents(t)
	callTool(t, ab, "browser_navigate", `{"url":"go.dev"}`)
	a.showPreview = false
	for _, args := range []string{`{"action":"zoom"}`, `{"action":"back"}`, `{"action":"forward"}`, `{"url":"go.dev","action":"back"}`} {
		if _, err := callTool(t, ab, "browser_navigate", args); err == nil {
			t.Errorf("%s went", args)
		}
		if a.showPreview {
			t.Errorf("%s showed the pane", args)
		}
	}
	if calls := (*made)[0].calls; len(calls) != 0 {
		t.Errorf("the page was asked %v", calls)
	}
}

// Reload tries again a tab whose page could not start, as the bar's does.
func TestAnAgentsReloadRetriesAPageThatCouldNotStart(t *testing.T) {
	a, ab, _ := agents(t)
	open := a.browser.open
	a.browser.open = func(string, browse.Events) (browse.Page, error) { return nil, fmt.Errorf("no web view") }
	if _, err := callTool(t, ab, "browser_navigate", `{"url":"go.dev"}`); err == nil || !strings.Contains(err.Error(), "no web view") {
		t.Fatalf("a page that can't start: %v", err)
	}
	a.browser.open = open
	if out, err := callTool(t, ab, "browser_navigate", `{"action":"reload"}`); err != nil || !strings.Contains(out, "Tab 1") {
		t.Errorf("reload of a page that couldn't start: %q, %v", out, err)
	}
}

// A call the agent gives up on before the view runs it does nothing.
func TestAnAgentsCallGivenUpOnDoesNothing(t *testing.T) {
	a, ab, made := agents(t)
	var later []func()
	ab.onView = func(fn func()) { later = append(later, fn) }
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	if _, err := ab.call(ctx, "browser_tabs", json.RawMessage(`{"action":"new","url":"go.dev"}`)); err != errGaveUp {
		t.Errorf("a call given up on: %v", err)
	}
	for _, fn := range later {
		fn()
	}
	if len(a.browser.tabs) != 0 || len(*made) != 0 || a.showPreview {
		t.Errorf("a call given up on opened %d tabs", len(a.browser.tabs))
	}
}

// A snapshot of a tab that never showed waits for the page it makes to
// load.
func TestAnAgentsSnapshotWaitsForANewPage(t *testing.T) {
	a, ab, made := agents(t)
	a.browser.add("https://go.dev", 0)
	open := a.browser.open
	a.browser.open = func(u string, ev browse.Events) (browse.Page, error) {
		p, err := open(u, ev)
		p.(*agentPage).loading = true
		go func() {
			time.Sleep(200 * time.Millisecond)
			ab.onView(func() { p.(*agentPage).loading = false })
		}()
		return p, err
	}
	start := time.Now()
	out, err := callTool(t, ab, "browser_snapshot", `{}`)
	if err != nil || time.Since(start) < 200*time.Millisecond || strings.Contains(out, "still loading") {
		t.Errorf("a new page's snapshot after %v: %q, %v", time.Since(start), out, err)
	}
	if len(*made) != 1 {
		t.Errorf("%d pages made", len(*made))
	}
}

// An action the page refuses leaves the pane as it was, and the agent
// reads why in the page's words.
func TestAnAgentsRefusedActionShowsNothing(t *testing.T) {
	a, ab, made := agents(t)
	first, _ := callTool(t, ab, "browser_navigate", `{"url":"go.dev"}`)
	callTool(t, ab, "browser_tabs", `{"action":"new","url":"example.com"}`)
	(*made)[0].onAct = func() error { return errors.New("mygo: javascript error: Error: s1e2 is disabled") }
	_, err := callTool(t, ab, "browser_click", `{"ref":"`+refIn(t, first, "Docs")+`"}`)
	if err == nil || err.Error() != "s1e2 is disabled" {
		t.Errorf("a refused click: %v", err)
	}
	if a.browser.active != 1 {
		t.Errorf("a refused click showed tab %d", a.browser.tabs[a.browser.active].id)
	}
	// A page that stops answering, as one showing a dialog does, says so.
	(*made)[0].onAct = func() error { return context.DeadlineExceeded }
	if _, err := callTool(t, ab, "browser_click", `{"ref":"`+refIn(t, first, "Docs")+`"}`); err == nil || !strings.Contains(err.Error(), "dialog") {
		t.Errorf("a page that stopped answering: %v", err)
	}
}

// A tab whose page can't start closes again, leaving the pane as it was.
func TestAnAgentsTabThatCannotStartGoes(t *testing.T) {
	a, ab, _ := agents(t)
	callTool(t, ab, "browser_navigate", `{"url":"go.dev"}`)
	a.showPreview = false
	a.browser.open = func(string, browse.Events) (browse.Page, error) { return nil, errors.New("no web view") }
	if _, err := callTool(t, ab, "browser_tabs", `{"action":"new","url":"example.com"}`); err == nil {
		t.Fatal("a tab whose page can't start opened")
	}
	if len(a.browser.tabs) != 1 || a.browser.active != 0 || a.showPreview {
		t.Errorf("after a tab that couldn't start: %d tabs, tab %d shown, pane %v", len(a.browser.tabs), a.browser.active, a.showPreview)
	}
}

// A tab a page opens as it acts, the agent hears of.
func TestAnAgentHearsOfTabsAPageOpens(t *testing.T) {
	a, ab, made := agents(t)
	first, _ := callTool(t, ab, "browser_navigate", `{"url":"go.dev"}`)
	(*made)[0].onAct = func() error {
		ab.onView(func() { (*made)[0].events.Opened("https://go.dev/play") })
		return nil
	}
	out, err := callTool(t, ab, "browser_click", `{"ref":"`+refIn(t, first, "Docs")+`"}`)
	if err != nil || !strings.HasPrefix(out, "The page opened tab 2 at https://go.dev/play.\n") || len(a.browser.tabs) != 2 {
		t.Errorf("a click opening a tab answered %q, %v", out, err)
	}
}

// A full snapshot of a tab that never showed is full.
func TestAnAgentsFullSnapshotOfANewPage(t *testing.T) {
	a, ab, _ := agents(t)
	a.browser.add("https://go.dev", 0)
	var asked bool
	open := a.browser.open
	a.browser.open = func(u string, ev browse.Events) (browse.Page, error) {
		pg, err := open(u, ev)
		return &fullPage{pg.(*agentPage), &asked}, err
	}
	if _, err := callTool(t, ab, "browser_snapshot", `{"full":true}`); err != nil || !asked {
		t.Errorf("a new page's full snapshot: full %v, %v", asked, err)
	}
}

// fullPage notes whether a snapshot asked for the page's structure.
type fullPage struct {
	*agentPage
	full *bool
}

func (p *fullPage) EvalContext(ctx context.Context, code string) (any, error) {
	if strings.Contains(code, `"interactive":false`) {
		*p.full = true
	}
	return p.agentPage.EvalContext(ctx, code)
}
