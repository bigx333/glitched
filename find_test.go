package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/daemon"
	"glitched/internal/provider/fake"
)

func TestFindMatchesEntries(t *testing.T) {
	es := []entry{
		{key: "u1", kind: entryUser, text: "Check websocket reconnect logic"},
		{key: "w1", kind: entryWork, work: []client.WorkStep{{Title: "Read file", Path: "transport/server.go", Output: "ok"}}},
		{key: "a1", kind: entryAssistant, text: "Added HandshakeLimiter and exponential backoff."},
		{key: "r1", kind: entryReport, reports: []client.ReportEntry{{ChildReported: api.ChildReported{Title: "Worker", Outcome: "verified reconnect"}}}},
	}
	if got := findMatches(es, "RECONNECT"); !slices.Equal(got, []int{0, 3}) {
		t.Errorf("findMatches(RECONNECT) = %v, want [0 3]", got)
	}
	if got := findMatches(es, "server.go"); !slices.Equal(got, []int{1}) {
		t.Errorf("findMatches(server.go) = %v, want [1]", got)
	}
	if got := findMatches(es, "handshake"); !slices.Equal(got, []int{2}) {
		t.Errorf("findMatches(handshake) = %v, want [2]", got)
	}
	if got := findMatches(es, "  "); len(got) != 0 {
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
