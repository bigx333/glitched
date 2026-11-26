package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/daemon"
	"glitched/internal/plugins/bundled"
	"glitched/internal/plugins/pack"
	"glitched/internal/provider/fake"
)

func TestFindMatchesEntries(t *testing.T) {
	es := []entry{
		{key: "u1", kind: entryUser, text: "Check websocket reconnect logic"},
		{key: "w1", kind: entryWork, work: []client.WorkStep{{Title: "Read file", Path: "transport/server.go", Output: "ok"}}},
		{key: "a1", kind: entryAssistant, text: "Added HandshakeLimiter and exponential backoff."},
		{key: "r1", kind: entryReport, reports: []client.ReportEntry{{ChildReported: api.ChildReported{Title: "Worker", Outcome: "verified reconnect"}}}},
	}
	if got := findMatches(es, "RECONNECT", false); !slices.Equal(got, []int{0, 3}) {
		t.Errorf("findMatches(RECONNECT) = %v, want [0 3]", got)
	}
	if got := findMatches(es, "server.go", false); !slices.Equal(got, []int{1}) {
		t.Errorf("findMatches(server.go) = %v, want [1]", got)
	}
	if got := findMatches(es, "handshake", false); !slices.Equal(got, []int{2}) {
		t.Errorf("findMatches(handshake) = %v, want [2]", got)
	}
	if got := findMatches(es, "  ", false); len(got) != 0 {
		t.Errorf("blank query matched %v", got)
	}
}

func TestFindBarOpensStepsAndCloses(t *testing.T) {
	dir := tempDir(t)
	h := startWith(t, dir, func(c *daemon.Config) {
		c.Providers = daemon.FakeProviders(fake.New(fake.Turn(false)))
	})

	id := h.a.backend.Create(h.local(), dir, "Find Thread", api.ModelRef{Provider: "claude", Model: "claude-opus-5-5"}, api.AccessAsk, nil)
	h.until("thread view", func() bool { return h.local().Threads[id] != nil })
	th := h.thread("Find Thread")
	h.a.select_(th)
	h.a.backend.Send(h.local(), id, "first needle in user prompt", api.StartNow)
	h.a.backend.Send(h.local(), id, "middle unrelated text", api.AfterActive)
	h.a.backend.Send(h.local(), id, "second needle in follow up", api.AfterActive)
	h.until("entries present", func() bool {
		return th.Transcript != nil && len(h.a.rowsOf(th).es) >= 3
	})

	h.tt.Frame()
	h.tt.Key(ui.Cmd, ui.KeyF)
	h.tt.Frame()
	if !h.a.findOpen {
		t.Fatal("⌘F did not open find bar")
	}
	h.tt.Type("needle")
	h.tt.Frame()
	if !h.tt.HasText("1 of 2") {
		t.Errorf("expected '1 of 2', texts=%q", h.tt.Texts())
	}
	h.tt.Key(ui.Cmd, ui.KeyG)
	h.tt.Frame()
	if !h.tt.HasText("2 of 2") {
		t.Errorf("after ⌘G expected '2 of 2', texts=%q", h.tt.Texts())
	}
	h.tt.Key(ui.Cmd|ui.Shift, ui.KeyG)
	h.tt.Frame()
	if !h.tt.HasText("1 of 2") {
		t.Errorf("after ⇧⌘G expected '1 of 2', texts=%q", h.tt.Texts())
	}
	h.tt.Key(0, ui.KeyEscape)
	h.tt.Frame()
	if h.a.findOpen {
		t.Error("Esc did not close find bar")
	}
}

func TestCmdKMessageSearchOpensFindBar(t *testing.T) {
	dir := tempDir(t)
	h := startWith(t, dir, func(c *daemon.Config) {
		c.Providers = daemon.FakeProviders(fake.New(fake.Turn(false)))
	})
	id1 := h.a.backend.Create(h.local(), dir, "Unrelated Title One", api.ModelRef{Provider: "claude", Model: "claude-opus-5-5"}, api.AccessAsk, nil)
	h.until("thread 1", func() bool { return h.local().Threads[id1] != nil })
	h.a.backend.Send(h.local(), id1, "secretphrase987 inside body", api.StartNow)
	h.until("message committed", func() bool {
		th := h.local().Threads[id1]
		return th != nil && th.View.Revision >= 2 && len(th.Pending) == 0
	})

	h.a.openCommandPalette()
	h.a.cmdkQuery = "secretphrase987"
	h.a.triggerCmdkSearch("secretphrase987")
	h.until("cmdk hits", func() bool { return len(h.a.cmdkHits) > 0 })
	found := h.a.cmdkHits
	if len(found) != 1 || found[0].Hit.Thread != id1 {
		t.Fatalf("cmdkHits = %+v, want hit for %s", found, id1)
	}

	cmds := h.a.commands()
	idx := slices.IndexFunc(cmds, func(c command) bool { return c.section == "Messages" && c.t != nil && c.t.id() == id1 })
	if idx < 0 {
		t.Fatalf("Messages command missing from %+v", cmds)
	}
	if !strings.Contains(cmds[idx].hint, "secretphrase987") {
		t.Errorf("message command hint = %q, want snippet", cmds[idx].hint)
	}
	cmds[idx].run()
	if !h.a.findOpen || h.a.findQuery != "secretphrase987" || h.a.selected != id1 {
		t.Errorf("running message hit: findOpen=%v query=%q selected=%s", h.a.findOpen, h.a.findQuery, h.a.selected)
	}
}

// The find bar finds the words of every row it shows: a plan's steps, a
// subagent's text and steps, and the thread a fork goes on from, as well
// as each row's text.
func TestFindMatchesEveryRowItShows(t *testing.T) {
	es := []entry{
		{key: "p", kind: entryPlan, plan: []api.PlanStep{{Text: "Rewrite the tokenizer"}}},
		{key: "w", kind: entryWork, work: []client.WorkStep{{Title: "Task", Subagent: &client.Subagent{Parts: []client.SubagentPart{
			{Text: "Reading the lexer"},
			{Step: &client.WorkStep{Title: "Grep for yylex"}},
		}}}}},
		{key: "f", kind: entryFailed, text: "Couldn't send: offline"},
		{key: "e", kind: entryError, text: "rate limited"},
		{key: "k", kind: entryCompact, text: "Compacted the context"},
		{key: "fork", kind: entryFork, fork: &client.ForkEntry{Forked: api.Forked{Title: "Parser rewrite"}}},
	}
	for q, want := range map[string]int{"tokenizer": 0, "lexer": 1, "yylex": 1, "offline": 2, "rate limited": 3, "compacted": 4, "parser rewrite": 5} {
		if got := findMatches(es, q, false); !slices.Equal(got, []int{want}) {
			t.Errorf("findMatches(%q) = %v, want [%d]", q, got, want)
		}
	}
}

// The find bar reads past accents and case, as search does.
func TestFindFoldsAccents(t *testing.T) {
	es := []entry{
		{key: "u", kind: entryUser, text: "Meet at the Café Zoë"},
		{key: "a", kind: entryAssistant, text: "See you at the cafe"},
	}
	if got := findMatches(es, "cafe", false); !slices.Equal(got, []int{0, 1}) {
		t.Errorf("findMatches(cafe) = %v, want [0 1]", got)
	}
	if got := findMatches(es, "ZOE", false); !slices.Equal(got, []int{0}) {
		t.Errorf("findMatches(ZOE) = %v, want [0]", got)
	}
}

// answered is a thread titled title, on the harness's fake agent, that was
// sent each of texts, every turn of them ended.
func (h *harness) answered(dir, title string, texts ...string) thread {
	h.t.Helper()
	id := h.a.backend.Create(h.local(), dir, title, api.ModelRef{Provider: "claude", Model: "claude-opus-5-5"}, api.AccessAsk, nil)
	h.until("the thread "+title, func() bool { return h.local().Threads[id] != nil })
	h.a.select_(h.thread(title))
	for i, text := range texts {
		mode := api.AfterActive
		if i == 0 {
			mode = api.StartNow
		}
		h.a.backend.Send(h.local(), id, text, mode)
	}
	h.until("every turn of "+title, func() bool {
		th := h.thread(title)
		if th.Transcript == nil || th.View.Live() || th.View.Queued > 0 || len(h.local().Threads[id].Pending) > 0 {
			return false
		}
		n := 0
		for _, e := range h.a.rowsOf(th).es {
			if e.kind == entryAssistant && strings.Contains(e.text, "Done.") {
				n++
			}
		}
		return n == len(texts)
	})
	return h.thread(title)
}

func fakeTurns(t *testing.T) (*harness, string) {
	dir := tempDir(t)
	return startWith(t, dir, func(c *daemon.Config) {
		c.Providers = daemon.FakeProviders(fake.New(fake.Turn(false)))
	}), dir
}

// findIn opens the find bar on th and types query.
func (h *harness) findIn(th thread, query string) {
	h.t.Helper()
	h.a.select_(th)
	h.tt.Frame()
	h.tt.Key(ui.Cmd, ui.KeyF)
	h.tt.Frame()
	h.tt.Type(query)
	h.tt.Frame()
}

func (h *harness) wantFound(label string) {
	h.t.Helper()
	if !h.tt.HasText(label) {
		h.t.Errorf("the find bar doesn't say %q: %q", label, h.tt.Texts())
	}
}

// ⇧↩ steps back through the matches, as ⇧⌘G does.
func TestFindBarStepsBackWithShiftEnter(t *testing.T) {
	h, dir := fakeTurns(t)
	h.findIn(h.answered(dir, "Steps", "needle one", "needle two", "needle three"), "needle")
	h.wantFound("1 of 3")
	h.tt.Key(ui.Shift, ui.KeyEnter)
	h.tt.Frame()
	h.wantFound("3 of 3")
	h.tt.Key(ui.Shift, ui.KeyEnter)
	h.tt.Frame()
	h.wantFound("2 of 3")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	h.wantFound("3 of 3")
}

// ⌘F on another thread starts at that thread's first match.
func TestFindBarReopenedOnAnotherThreadStartsAtItsFirstMatch(t *testing.T) {
	h, dir := fakeTurns(t)
	first := h.answered(dir, "First", "needle one", "needle two")
	second := h.answered(dir, "Second", "needle a", "needle b", "needle c")
	h.findIn(first, "needle")
	h.tt.Key(ui.Cmd, ui.KeyG)
	h.tt.Frame()
	h.wantFound("2 of 2")
	h.tt.Key(0, ui.KeyEscape)
	h.tt.Frame()

	h.a.select_(second)
	h.tt.Frame()
	h.tt.Key(ui.Cmd, ui.KeyF)
	h.tt.Frame()
	h.wantFound("1 of 3")
}

// The find bar floats over the timeline, so a card's lines under it are
// not read.
func TestFindBarCoversWhatItFloatsOver(t *testing.T) {
	h, dir := fakeTurns(t)
	h.findIn(h.answered(dir, "Covered", "needle"), "needle")
	h.tt.Frame()
	r, ok := h.tt.Find("1 of 1")
	if !ok {
		t.Fatalf("no find bar: %q", h.tt.Texts())
	}
	if !h.a.hidden(r) {
		t.Errorf("the find bar at %+v covers nothing: %+v", r, h.a.over)
	}
}

// A message the palette found opens its thread with the find bar on that
// message, matched as search matched it: past accents, word by word.
func TestCmdKHitOpensFindAtItsMessage(t *testing.T) {
	h, dir := fakeTurns(t)
	// The last matches best, holding the word twice.
	needles := h.answered(dir, "Needles", "needle", "needle", "needle needle")
	cafe := h.answered(dir, "Cafe", "the café pipeline check")

	for _, c := range []struct {
		query string
		th    thread
		found string
	}{
		{"needle", needles, "3 of 3"},
		{"cafe check", cafe, "1 of 1"},
	} {
		h.a.openCommandPalette()
		h.a.cmdkQuery = c.query
		h.a.triggerCmdkSearch(c.query)
		var hit *command
		h.until("a message found for "+c.query, func() bool {
			for _, cmd := range h.a.commands() {
				if cmd.section == "Messages" && cmd.t != nil && cmd.t.id() == c.th.id() {
					hit = &cmd
					return true
				}
			}
			return false
		})
		hit.run()
		h.a.cmdkOpen = false
		h.tt.Frame()
		h.tt.Frame()
		if h.a.selected != c.th.id() || !h.a.findOpen {
			t.Fatalf("%q: selected %v, find bar open %v", c.query, h.a.selected, h.a.findOpen)
		}
		h.wantFound(c.found)
	}
}

// The palette lists no messages found for what the query said before.
func TestCmdKDropsHitsOfAnEarlierQuery(t *testing.T) {
	h, dir := fakeTurns(t)
	h.answered(dir, "Stale", "alphaneedle")
	h.a.openCommandPalette()
	h.a.cmdkQuery = "alphaneedle"
	h.a.triggerCmdkSearch("alphaneedle")
	h.until("the hit", func() bool { return len(h.a.cmdkHits) > 0 })

	h.a.cmdkQuery = "zzz"
	h.a.triggerCmdkSearch("zzz")
	for _, cmd := range h.a.commands() {
		if cmd.section == "Messages" {
			t.Errorf("for zzz the palette lists %q, found for alphaneedle", cmd.hint)
		}
	}
}

// In Zen mode the find bar scrolls to the line that shows the match.
func TestFindInZenScrollsToTheMatchsLine(t *testing.T) {
	h, dir := fakeTurns(t)
	h.a.settings.zen = true
	var texts []string
	for i := range 12 {
		texts = append(texts, fmt.Sprintf("turn %d", i))
	}
	th := h.answered(dir, "Zen", texts...)
	h.findIn(th, "Done.")
	h.tt.Advance(2 * time.Second)
	h.tt.Frame()
	h.wantFound("1 of 12")
	rs := h.a.rowsOf(th)
	v := h.a.viewFor(th.id(), rs)
	at := v.pos(findMatches(rs.es, "Done.", false)[0])
	if first, last := h.a.listFor(th.id()).Visible(); at < first || at > last {
		t.Errorf("the first match's line, %d, is out of view: %d to %d", at, first, last)
	}
}

// signedBuiltIn has the app run the built-in plugin name as this tree has
// it, signed by a key of the test's: the release key signs it only once it
// is pushed.
func signedBuiltIn(t *testing.T, name string) {
	t.Helper()
	root := t.TempDir()
	if err := os.CopyFS(root, bundled.FS); err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(root, name)
	os.Remove(filepath.Join(folder, "signature.json"))
	key, err := pack.LoadKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := pack.Sign(folder, key); err != nil {
		t.Fatal(err)
	}
	was, wasKeys := bundled.FS, releaseKeys
	bundled.FS, releaseKeys = os.DirFS(root), []string{bundled.ReleaseKey, key.Public()}
	t.Cleanup(func() { bundled.FS, releaseKeys = was, wasKeys })
}

// The built-in palette lists the messages it finds, and opens one's thread
// with the find bar on it.
func TestThePalettesBuiltInFindsMessages(t *testing.T) {
	signedBuiltIn(t, "palette")
	h, dir := fakeTurns(t)
	th := h.answered(dir, "Haystack", "needle", "needle", "needle needle")
	h.a.openCommandPalette()
	h.tt.Frame()
	h.tt.Type("needle")
	settlePlugins(t, h.a, h.tt, h.p)
	if !h.tt.HasText("Messages") || !h.tt.HasText("needle needle") {
		t.Fatalf("the palette lists no message: %q", h.tt.Texts())
	}
	h.tt.Key(0, ui.KeyEnter)
	settlePlugins(t, h.a, h.tt, h.p)
	h.tt.Frame()
	if h.a.cmdkOpen || h.a.selected != th.id() || !h.a.findOpen || h.a.findQuery != "needle" {
		t.Fatalf("Enter on the message: palette open %v, selected %v, find bar open %v with %q", h.a.cmdkOpen, h.a.selected, h.a.findOpen, h.a.findQuery)
	}
	h.wantFound("3 of 3")
}
