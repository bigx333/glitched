package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/daemon"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
)

func forkRemote(t *testing.T, h *harness) *daemon.Daemon {
	t.Helper()
	dir := tempDir(t)
	token, err := daemon.Pair(dir, "gui")
	if err != nil {
		t.Fatal(err)
	}
	d, err := startEngine(context.Background(), daemon.Config{Dir: dir, Hub: true, Listen: []string{"127.0.0.1:0"},
		Providers: provider.Mux{"claude": fake.New(fake.Turn(false))}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := h.addServer(client.Server{Label: "claude-only-box", Addr: d.Addrs()[0].String(), Token: token}); err != nil {
		t.Fatal(err)
	}
	return d
}

func forkRemoteCreate(t *testing.T, d *daemon.Daemon, id api.ID, body api.Intent) {
	t.Helper()
	if _, err := d.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: id, Body: body}); err != nil {
		t.Fatal(err)
	}
}

// Continue on is offered only for a thread on this machine, whose engine
// serves the providers the picker lists.
func TestContinueOnIsOnlyForThisMachinesThreads(t *testing.T) {
	h := startWith(t, tempDir(t), engineReports("codex", provider.Model{ID: "only-local", Name: "Only local"}))
	d := forkRemote(t, h)
	const title = "Remote source"
	forkRemoteCreate(t, d, api.NewID(), api.ThreadCreate{Workspace: tempDir(t), Title: title, Model: api.ModelRef{Provider: "claude"}, Text: "first"})
	th := h.thread(title)
	h.until("the remote turn", func() bool { return th.View.LastTurn != nil })
	h.until("the local Codex catalog", func() bool {
		return len(h.a.models.reported["codex"]) > 0 && h.a.models.reported["codex"][0].ID == "only-local"
	})
	menu := ui.NewTester(func(c *ui.Context) {
		ui.Box(c).Width(300).Height(40).Label(title).ContextMenu(func(m *ui.Menu) { h.a.threadMenu(c, m, th, catInbox) })
	}, 400, 200)
	menu.Frame()
	if err := menu.RightClick(title); err != nil {
		t.Fatal(err)
	}
	if err := menu.ChooseMenuItem("Continue on", "Codex"); err != nil {
		return
	}
	dst := h.thread(title + " on Codex")
	h.until("the fork opened", func() bool { return h.a.current().id() == dst.id() && forkEntry(h.a.current()) != nil })
	h.a.draft = "go on"
	h.click("Send")
	h.until("the offered provider's first run", func() bool {
		v := h.a.current().View
		return v.LastTurn != nil && !v.Live()
	})
	h.tt.Advance(time.Second)
	var failure string
	for _, e := range h.a.current().Transcript.Entries {
		if e, ok := e.(*client.ErrorEntry); ok {
			failure += e.Error
		}
	}
	for _, msg := range h.tt.Texts() {
		if strings.Contains(msg, "no adapter") {
			failure = msg
		}
	}
	t.Fatalf("Continue on offered local-only Codex for a Claude-only remote; first outcome=%v; screen error=%q", h.a.current().View.LastTurn.Outcome, failure)
}

// A fork's first row opens its source only on the fork's own engine.
func TestAForksSourceLinkStaysOnItsEngine(t *testing.T) {
	dir := tempDir(t)
	local := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(false)))
	h := start(t, dir)
	remote := forkRemote(t, h)
	src, dst, ws := api.NewID(), api.NewID(), tempDir(t)
	forkRemoteCreate(t, local, src, api.ThreadCreate{Workspace: ws, Title: "Local duplicate", Model: api.ModelRef{Provider: "claude"}})
	forkRemoteCreate(t, remote, src, api.ThreadCreate{Workspace: ws, Title: "Remote original", Model: api.ModelRef{Provider: "claude"}, Text: "first"})
	th := h.thread("Remote original")
	h.until("the remote turn", func() bool { return th.View.LastTurn != nil })
	forkRemoteCreate(t, remote, dst, api.ThreadCreate{Workspace: ws, Title: "Remote fork", Model: api.ModelRef{Provider: "claude"},
		Fork: &api.Fork{Thread: src}})
	forkRemoteCreate(t, remote, src, api.ThreadDelete{})
	h.thread("Local duplicate")
	fork := h.thread("Remote fork")
	h.a.choose(fork)
	h.a.follow()
	h.until("the fork seam", func() bool {
		_, exists := fork.env.Threads[src]
		return !exists && forkEntry(h.a.current()) != nil
	})
	h.tt.Advance(time.Second)
	if err := h.tt.Click("Open Remote original"); err != nil {
		return
	}
	h.tt.Advance(time.Second)
	if got := h.a.current(); got.env != fork.env {
		t.Fatalf("clicking Open Remote original on a fork of a deleted remote source opened %q on this Mac", got.View.Title)
	}
}
