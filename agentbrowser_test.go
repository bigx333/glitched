package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"glitched/internal/browse"
	"glitched/internal/browsermcp"
	"glitched/internal/webtarget"
)

// agentPage is a tab's page that answers the page script as it would: a
// snapshot of a link "Docs", a text box "Search", and a dropdown "Size",
// plus capture support when png is set.
type agentPage struct {
	*fakePage
	mu    sync.Mutex
	acts  []map[string]any // the actions asked, with their args
	title string
	url   string
	png   []byte
	// onAct, if set, is what the page does as it acts, or why it won't.
	onAct func() error
	// onEval, if set, runs as each script does, outside the page's lock.
	onEval func()
	// onWait, if set, evaluates wait_check scripts.
	onWait func(args map[string]any) (bool, error)
}

func (p *agentPage) CapturePage(context.Context) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.png) == 0 {
		return nil, errors.New("no image")
	}
	return p.png, nil
}

func (p *agentPage) EvalContext(_ context.Context, code string) (any, error) {
	if p.onEval != nil {
		p.onEval()
	}
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
		docs, search, sel := fmt.Sprintf("s%de1", epoch), fmt.Sprintf("s%de2", epoch), fmt.Sprintf("s%de3", epoch)
		pageURL := p.url
		if pageURL == "" {
			pageURL = "https://go.dev/"
		}
		return map[string]any{"epoch": epoch, "url": pageURL, "title": p.title, "refs": 3,
			"text": `- link "Docs" [ref=` + docs + "]\n" + `- textbox "Search" [ref=` + search + "]\n" + `- combobox "Size" [ref=` + sel + "]",
			"elements": []any{
				map[string]any{"ref": docs, "role": "link", "name": "Docs"},
				map[string]any{"ref": search, "role": "textbox", "name": "Search"},
				map[string]any{"ref": sel, "role": "combobox", "name": "Size"},
			}}, nil
	case "act", "scroll":
		p.acts = append(p.acts, args)
		if p.onAct != nil {
			if err := p.onAct(); err != nil {
				return nil, err
			}
		}
		return map[string]any{"done": true}, nil
	case "wait_check":
		p.acts = append(p.acts, args)
		if p.onWait != nil {
			ok, err := p.onWait(args)
			if err != nil {
				return nil, err
			}
			return map[string]any{"matched": ok}, nil
		}
		return map[string]any{"matched": true}, nil
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
	r, err := ab.call(context.Background(), tool, json.RawMessage(args))
	return r.Text, err
}

func callToolReply(t *testing.T, ab *agentBrowser, thread, tool, args string) (browsermcp.Reply, error) {
	t.Helper()
	ctx := context.Background()
	if thread != "" {
		ctx = browsermcp.WithThread(ctx, thread)
	}
	return ab.call(ctx, tool, json.RawMessage(args))
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
	if _, err := callTool(t, ab, "browser_tabs", `{"action":"new"}`); err == nil || err.Error() != "browser_tabs new needs a url." {
		t.Errorf("a new tab at no address: %v", err)
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

// A click the page made succeeds, even if what follows it doesn't finish:
// an agent told it failed would click again, and submit a form twice.
func TestAnAgentsActionThatWentThroughSucceeds(t *testing.T) {
	a, ab, made := agents(t)
	first, _ := callTool(t, ab, "browser_navigate", `{"url":"go.dev"}`)
	callTool(t, ab, "browser_tabs", `{"action":"new","url":"example.com"}`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	(*made)[0].onAct = func() error { cancel(); return nil }
	out, err := ab.call(ctx, "browser_click", json.RawMessage(`{"ref":"`+refIn(t, first, "Docs")+`"}`))
	if err != nil || !strings.HasPrefix(out.Text, "The click went through, but what followed didn't finish") {
		t.Errorf("a click whose call ended as it acted answered %+v, %v", out, err)
	}
	if n := len((*made)[0].acts); n != 1 {
		t.Errorf("the page acted %d times", n)
	}
	// What the agent gave up on didn't show the tab.
	if a.browser.active != 1 {
		t.Errorf("tab %d shows", a.browser.tabs[a.browser.active].id)
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

// A call that times out as the view runs it reads nothing the view is
// still writing (go test -race).
func TestAnAgentsCallTimingOutReadsNothingHalfWritten(t *testing.T) {
	_, ab, _ := agents(t)
	callTool(t, ab, "browser_navigate", `{"url":"go.dev"}`)
	ab.onView = func(fn func()) { go fn() }
	for i := range 200 {
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(i%20)*time.Microsecond)
		ab.call(ctx, "browser_tabs", json.RawMessage(`{"action":"list"}`))
		cancel()
	}
}

// The app runs as browser-mcp when Claude Code starts it so, and as the
// app otherwise.
func TestTheAppServesBrowserMCP(t *testing.T) {
	if served, err := serveBrowserMCP(nil, nil, nil); served || err != nil {
		t.Errorf("with no arguments the app served browser-mcp: %v", err)
	}
	dir, err := os.MkdirTemp("", "gl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	ln, err := browsermcp.Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go browsermcp.Accept(ln, func(_ context.Context, tool string, _ json.RawMessage) (browsermcp.Reply, error) {
		return browsermcp.Text("the app's " + tool), nil
	})
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	type result struct {
		served bool
		err    error
	}
	done := make(chan result, 1)
	go func() {
		served, err := serveBrowserMCP([]string{"browser-mcp", "-dir", dir}, inR, outW)
		done <- result{served, err}
		outW.Close()
	}()
	io.WriteString(inW, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"browser_tabs","arguments":{"action":"list"}}}`+"\n")
	line, _ := bufio.NewReader(outR).ReadString('\n')
	inW.Close()
	r := <-done
	if !r.served || r.err != nil || !strings.Contains(line, `"text":"the app's browser_tabs"`) {
		t.Errorf("browser-mcp served %v, %v:\n%s", r.served, r.err, line)
	}
}

// A call whose work the view did as the call ended says it was done:
// the tab it opened is there.
func TestAnAgentsCallDoneAsItEndsSaysSo(t *testing.T) {
	_, ab, _ := agents(t)
	for range 200 {
		ctx, cancel := context.WithCancel(context.Background())
		if err := ab.view(ctx, func() error { cancel(); return nil }); err != nil {
			t.Fatalf("work done as the call ended: %v", err)
		}
	}
}

// Of two snapshots of a tab, the later stays the tab's, whichever ends
// first: its refs are those the page acts on.
func TestAnAgentsLaterSnapshotStays(t *testing.T) {
	_, ab, _ := agents(t)
	ab.keep(1, webtarget.Snapshot{Epoch: 8})
	ab.keep(1, webtarget.Snapshot{Epoch: 7})
	if got := ab.snaps[1].Epoch; got != 8 {
		t.Errorf("the tab's snapshot is s%d, want the later s8", got)
	}
}

// A tab the user closes takes its snapshot with it.
func TestAnAgentForgetsTabsTheUserCloses(t *testing.T) {
	a, ab, _ := agents(t)
	callTool(t, ab, "browser_navigate", `{"url":"go.dev"}`)
	if len(ab.snaps) != 1 {
		t.Fatalf("snapshots %v", ab.snaps)
	}
	a.closeTab(0)
	if _, err := callTool(t, ab, "browser_tabs", `{"action":"list"}`); err != nil {
		t.Fatal(err)
	}
	if len(ab.snaps) != 0 {
		t.Errorf("a closed tab's snapshot is kept: %v", ab.snaps)
	}
}

// The browser socket's directory is the user's alone, however wide it
// was made before.
func TestTheBrowserSocketIsTheUsers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no modes")
	}
	dir, err := os.MkdirTemp("", "gl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	os.Chmod(dir, 0o755)
	a := testApp(t, dir, nil, func(func()) {})
	a.serveAgents()
	if a.stopAgents == nil {
		t.Fatal("the app serves no browser socket")
	}
	defer a.stopAgents()
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("the state directory: %v %v, want 0700", fi.Mode().Perm(), err)
	}
}

// A call whose context ended before the view ran it says it gave up,
// in words, not the context's.
func TestAnAgentsLateCallSaysItGaveUp(t *testing.T) {
	_, ab, _ := agents(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for range 100 {
		if err := ab.view(ctx, func() error { return nil }); err != errGaveUp {
			t.Fatalf("a call ended before the view ran it: %v", err)
		}
	}
}

// A tab's page runs one script of the agents' at a time, so the
// snapshot it holds is the one kept.
func TestAnAgentsScriptsOnATabTakeTurns(t *testing.T) {
	_, ab, made := agents(t)
	callTool(t, ab, "browser_navigate", `{"url":"go.dev"}`)
	p := (*made)[0]
	var running, most atomic.Int32
	p.onEval = func() {
		n := running.Add(1)
		for m := most.Load(); n > m && !most.CompareAndSwap(m, n); m = most.Load() {
		}
		time.Sleep(5 * time.Millisecond)
		running.Add(-1)
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() { callTool(t, ab, "browser_snapshot", `{}`) })
		wg.Go(func() { callTool(t, ab, "browser_click", `{"ref":"`+refOf(ab, 1)+`"}`) })
	}
	wg.Wait()
	if m := most.Load(); m != 1 {
		t.Errorf("%d scripts ran on one tab at once", m)
	}
}

// refOf is a ref of tab id's latest snapshot.
func refOf(ab *agentBrowser, id int) string {
	ab.mu.Lock()
	defer ab.mu.Unlock()
	return ab.snaps[id].Elements[0].Ref
}

// A tab that can't start leaves the diff open, as the pane was.
func TestAnAgentsTabThatCannotStartLeavesTheDiff(t *testing.T) {
	a, ab, _ := agents(t)
	a.showPreview, a.showDiff = false, true
	a.browser.open = func(string, browse.Events) (browse.Page, error) { return nil, errors.New("no web view") }
	if _, err := callTool(t, ab, "browser_tabs", `{"action":"new","url":"example.com"}`); err == nil {
		t.Fatal("a tab whose page can't start opened")
	}
	if a.showPreview || !a.showDiff {
		t.Errorf("after a tab that couldn't start: pane %v, diff %v", a.showPreview, a.showDiff)
	}
}

// A tab closing as a script runs on it holds the next script back still.
func TestAnAgentsScriptHoldsAClosingTab(t *testing.T) {
	a, ab, _ := agents(t)
	callTool(t, ab, "browser_navigate", `{"url":"go.dev"}`)
	unlock, _ := ab.lock(context.Background(), 1)
	a.closeTab(0)
	callTool(t, ab, "browser_tabs", `{"action":"list"}`)
	next := make(chan struct{})
	go func() { u, _ := ab.lock(context.Background(), 1); u(); close(next) }()
	select {
	case <-next:
		t.Fatal("a script ran on a tab another held, as the tab closed")
	case <-time.After(50 * time.Millisecond):
	}
	unlock()
	<-next
}

// A call waiting on a tab another runs a script on gives up as the
// agent does, not as the other ends.
func TestAnAgentsCallWaitingOnATabGivesUp(t *testing.T) {
	_, ab, _ := agents(t)
	unlock, _ := ab.lock(context.Background(), 1)
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := ab.lock(ctx, 1)
		done <- err
	}()
	select {
	case err := <-done:
		if err != errGaveUp {
			t.Errorf("a call waiting on a held tab: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a call waiting on a held tab went on after the agent gave up")
	}
}

func TestAgentBrowserScreenshot(t *testing.T) {
	a, ab, made := agents(t)
	if _, err := callTool(t, ab, "browser_navigate", `{"url":"example.com"}`); err != nil {
		t.Fatal(err)
	}
	wantPNG := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 1, 2, 3}
	(*made)[0].png = wantPNG
	r, err := callToolReply(t, ab, "", "browser_screenshot", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if r.MimeType != "image/png" || !strings.Contains(r.Text, "Screenshot of tab 1") || string(r.Image) != string(wantPNG) {
		t.Errorf("screenshot reply = %+v", r)
	}

	// Engine without Capturer reports a clear unsupported error.
	a.newTab("https://go.dev")
	a.browser.tabs[a.browser.active].page = &fakePage{}
	if _, err := callToolReply(t, ab, "", "browser_screenshot", `{}`); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Errorf("non-capturer error = %v", err)
	}
}

func TestAgentBrowserSelectScrollAndWaitFor(t *testing.T) {
	_, ab, made := agents(t)
	if _, err := callTool(t, ab, "browser_navigate", `{"url":"example.com"}`); err != nil {
		t.Fatal(err)
	}
	p := (*made)[0]
	selRef := fmt.Sprintf("s%de3", ab.snaps[1].Epoch)
	if out, err := callTool(t, ab, "browser_select", `{"ref":"`+selRef+`","label":"Large"}`); err != nil || !strings.Contains(out, `URL:`) {
		t.Fatalf("browser_select: %v\n%s", err, out)
	}
	if out, err := callTool(t, ab, "browser_scroll", `{"direction":"down","amount":300}`); err != nil || !strings.Contains(out, `Tab 1:`) {
		t.Fatalf("browser_scroll: %v\n%s", err, out)
	}

	var polls int
	p.onWait = func(args map[string]any) (bool, error) {
		polls++
		return polls >= 2, nil
	}
	if out, err := callTool(t, ab, "browser_wait_for", `{"text":"Ready","timeout_ms":1000}`); err != nil || !strings.Contains(out, `Tab 1:`) {
		t.Fatalf("browser_wait_for match: %v\n%s", err, out)
	}
	if polls != 2 {
		t.Errorf("polls = %d, want 2", polls)
	}

	p.onWait = func(map[string]any) (bool, error) { return false, nil }
	if _, err := callTool(t, ab, "browser_wait_for", `{"selector":".spinner","gone":true,"timeout_ms":120}`); err == nil || !strings.Contains(err.Error(), "Timed out") {
		t.Errorf("browser_wait_for timeout = %v", err)
	}
}

func TestAgentBrowserTabLeasing(t *testing.T) {
	a, ab, _ := agents(t)
	now := time.Unix(1_700_000_000, 0)
	ab.clock = func() time.Time { return now }

	// Thread A opens tab 1 and holds its lease.
	if _, err := callToolReply(t, ab, "threadAAAAAAA", "browser_tabs", `{"action":"new","url":"example.com"}`); err != nil {
		t.Fatal(err)
	}
	// Thread B cannot navigate or close Thread A's leased tab explicitly.
	if _, err := callToolReply(t, ab, "threadBBBBBBB", "browser_navigate", `{"tab":1,"url":"go.dev"}`); err == nil || !strings.Contains(err.Error(), "leased by another thread") {
		t.Errorf("thread B explicit navigate = %v", err)
	}
	if _, err := callToolReply(t, ab, "threadBBBBBBB", "browser_tabs", `{"action":"close","tab":1}`); err == nil || !strings.Contains(err.Error(), "leased by another thread") {
		t.Errorf("thread B close = %v", err)
	}
	// Thread B navigating without a tab opens its own tab 2 instead of clobbering tab 1.
	if _, err := callToolReply(t, ab, "threadBBBBBBB", "browser_navigate", `{"url":"go.dev"}`); err != nil {
		t.Fatal(err)
	}
	if len(a.browser.tabs) != 2 || a.browser.tabs[0].url != "https://example.com" || a.browser.tabs[1].url != "https://go.dev" {
		t.Fatalf("tabs after B navigate = %+v", a.browser.tabs)
	}
	// List shows lease tags.
	listReply, err := callToolReply(t, ab, "threadAAAAAAA", "browser_tabs", `{"action":"list"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(listReply.Text, "tab 1") || !strings.Contains(listReply.Text, "[leased]") || !strings.Contains(listReply.Text, "[leased by threadBB]") {
		t.Errorf("list output missing lease labels:\n%s", listReply.Text)
	}
	// Thread A releases its lease, then Thread B can select tab 1.
	if _, err := callToolReply(t, ab, "threadAAAAAAA", "browser_tabs", `{"action":"release","tab":1}`); err != nil {
		t.Fatal(err)
	}
	if _, err := callToolReply(t, ab, "threadBBBBBBB", "browser_tabs", `{"action":"select","tab":1}`); err != nil {
		t.Fatalf("thread B select after release = %v", err)
	}
	// Advancing clock past leaseTTL expires Thread B's lease so Thread A can take tab 1.
	now = now.Add(ab.leaseTTL + time.Second)
	if _, err := callToolReply(t, ab, "threadAAAAAAA", "browser_tabs", `{"action":"select","tab":1}`); err != nil {
		t.Fatalf("thread A select after expiry = %v", err)
	}
}

func TestAgentBrowserSyncsSPAURL(t *testing.T) {
	a, ab, made := agents(t)
	if _, err := callTool(t, ab, "browser_navigate", `{"url":"https://app.example/start"}`); err != nil {
		t.Fatal(err)
	}
	(*made)[0].url = "https://app.example/settings/profile"
	(*made)[0].title = "Profile Settings"
	if _, err := callTool(t, ab, "browser_snapshot", `{}`); err != nil {
		t.Fatal(err)
	}
	if got := a.browser.tabs[0].url; got != "https://app.example/settings/profile" {
		t.Errorf("tab url = %q, want SPA route URL", got)
	}
	if got := a.browser.tabs[0].title; got != "Profile Settings" {
		t.Errorf("tab title = %q, want Profile Settings", got)
	}
}

// A thread can't read another thread's leased tab: a snapshot would
// replace the refs the owner acts on.
func TestALeaseKeepsItsThreadsRefs(t *testing.T) {
	_, ab, _ := agents(t)
	if _, err := callToolReply(t, ab, "threadAAAAAAA", "browser_tabs", `{"action":"new","url":"example.com"}`); err != nil {
		t.Fatal(err)
	}
	ref := refOf(ab, 1)
	for _, call := range [][2]string{
		{"browser_snapshot", `{"tab":1}`},
		{"browser_wait_for", `{"tab":1,"text":"Docs","timeout_ms":100}`},
	} {
		if _, err := callToolReply(t, ab, "threadBBBBBBB", call[0], call[1]); err == nil || !strings.Contains(err.Error(), "leased by another thread") {
			t.Errorf("thread B %s of A's tab = %v", call[0], err)
		}
	}
	if _, err := callToolReply(t, ab, "threadAAAAAAA", "browser_click", `{"ref":"`+ref+`"}`); err != nil {
		t.Errorf("A's ref after B's attempts: %v", err)
	}
}

// One thread's screenshot neither shows another thread's leased tab nor
// moves where that thread's calls without a tab go.
func TestAScreenshotLeavesOtherThreadsTabs(t *testing.T) {
	a, ab, made := agents(t)
	if _, err := callToolReply(t, ab, "threadAAAAAAA", "browser_tabs", `{"action":"new","url":"example.com"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := callTool(t, ab, "browser_tabs", `{"action":"new","url":"go.dev"}`); err != nil {
		t.Fatal(err)
	}
	for _, p := range *made {
		p.png = []byte("png")
	}
	if _, err := callToolReply(t, ab, "threadBBBBBBB", "browser_screenshot", `{"tab":1}`); err == nil || !strings.Contains(err.Error(), "leased by another thread") {
		t.Errorf("thread B screenshot of A's tab = %v", err)
	}
	if _, err := callToolReply(t, ab, "threadBBBBBBB", "browser_screenshot", `{"tab":2}`); err != nil {
		t.Fatal(err)
	}
	if _, err := callToolReply(t, ab, "threadAAAAAAA", "browser_navigate", `{"url":"example.org"}`); err != nil {
		t.Fatal(err)
	}
	if got := []string{a.browser.tabs[0].url, a.browser.tabs[1].url}; got[0] != "https://example.org" || got[1] != "https://go.dev" {
		t.Errorf("A's navigate after B's screenshot: tabs at %v, want A's tab 1 moved and tab 2 left", got)
	}
	out, err := callToolReply(t, ab, "threadAAAAAAA", "browser_snapshot", `{}`)
	if err != nil || !strings.HasPrefix(out.Text, "Tab 1:") {
		t.Errorf("A's snapshot without a tab = %v\n%s", err, out.Text)
	}
}

// A call naming no thread can't use or release a thread's leased tab, and
// one thread's release without a tab drops only its own leases.
func TestALeaseHoldsAgainstCallsWithoutAThread(t *testing.T) {
	a, ab, _ := agents(t)
	if _, err := callToolReply(t, ab, "threadAAAAAAA", "browser_tabs", `{"action":"new","url":"example.com"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := callToolReply(t, ab, "threadBBBBBBB", "browser_tabs", `{"action":"new","url":"go.dev"}`); err != nil {
		t.Fatal(err)
	}
	for _, call := range [][2]string{
		{"browser_snapshot", `{"tab":1}`},
		{"browser_navigate", `{"tab":1,"url":"example.org"}`},
		{"browser_tabs", `{"action":"close","tab":1}`},
		{"browser_tabs", `{"action":"release","tab":1}`},
	} {
		if _, err := callTool(t, ab, call[0], call[1]); err == nil || !strings.Contains(err.Error(), "leased by another thread") {
			t.Errorf("%s %s without a thread = %v", call[0], call[1], err)
		}
	}
	// Tab 2, B's, shows: a navigate without a thread opens a tab of its own.
	if _, err := callTool(t, ab, "browser_navigate", `{"url":"example.org"}`); err != nil {
		t.Fatal(err)
	}
	if len(a.browser.tabs) != 3 || a.browser.tabs[1].url != "https://go.dev" {
		t.Errorf("tabs after a navigate without a thread: %d, tab 2 at %s", len(a.browser.tabs), a.browser.tabs[1].url)
	}
	if _, err := callTool(t, ab, "browser_tabs", `{"action":"release"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := callToolReply(t, ab, "threadBBBBBBB", "browser_tabs", `{"action":"release"}`); err != nil {
		t.Fatal(err)
	}
	if owner := ab.leaseOwner(1); owner != "threadAAAAAAA" {
		t.Errorf("tab 1's lease after others released theirs = %q", owner)
	}
	if owner := ab.leaseOwner(2); owner != "" {
		t.Errorf("tab 2's lease after B released its own = %q", owner)
	}
}
