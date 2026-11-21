package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/daemon"
	"glitched/internal/provider/fake"
)

// The sidebar sorts its threads into sections as they change, not each
// frame: each kind of change below moves a thread at once, and a frame
// with none sorts nothing again.

// in is whether the thread shows in the section.
func in(h *harness, cat category, t thread) bool {
	return slices.ContainsFunc(h.a.itemsIn(cat), func(it inboxItem) bool { return it.t.id() == t.id() })
}

// A change the engine posts, as another window's settle, moves the thread.
func TestTheSidebarSortsAThreadTheEngineMoves(t *testing.T) {
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(false)))
	id := seed(t, d, "/work/sorted", "Settled elsewhere")
	h := start(t, dir)
	th := h.thread("Settled elsewhere")
	if !in(h, catInbox, th) {
		t.Fatal("the thread isn't in the inbox")
	}
	body := api.InboxSet{Disposition: api.Disposition{State: api.Settled}}
	if _, err := d.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: id, Body: body}); err != nil {
		t.Fatal(err)
	}
	h.until("the thread in Settled", func() bool { return in(h, catSettled, th) && !in(h, catInbox, th) })
}

// A thread read here leaves the inbox's count of unread at once.
func TestTheSidebarCountsAThreadReadHereAtOnce(t *testing.T) {
	h := start(t, inboxDir(t))
	var unread thread
	for _, it := range h.a.itemsIn(catInbox) {
		if h.a.backend.Unread(it.t.Thread) {
			unread = it.t
		}
	}
	if unread.Thread == nil {
		t.Fatal("no thread in the inbox is unread")
	}
	n := h.a.unreadCount()
	h.a.backend.MarkRead(unread.Thread)
	if got := h.a.unreadCount(); got != n-1 {
		t.Fatalf("read, the inbox counts %d unread, want %d", got, n-1)
	}
}

// Filtered to a project, the sections show its threads alone at once.
func TestTheSidebarSortsAsTheProjectsAreFiltered(t *testing.T) {
	h := start(t, inboxDir(t))
	th := h.thread("SIP re-INVITE handling")
	p := h.a.projectOf(th)
	raw, _ := json.Marshal(map[string][]string{"keys": {p.key.String()}})
	if _, err := h.a.filterEffect(raw); err != nil {
		t.Fatal(err)
	}
	for _, cat := range []category{catInbox, catWorking, catSnoozed, catSettled} {
		for _, it := range h.a.itemsIn(cat) {
			if it.p.key != p.key {
				t.Fatalf("filtered to %s, the sidebar shows %q of %s", p.name(), it.t.View.Title, it.p.name())
			}
		}
	}
	if !in(h, catInbox, th) {
		t.Fatal("filtered to its project, the thread is gone")
	}
}

// A row's joke keeps the thread in the section it leaves while it plays,
// from the frame it starts, though the thread was sorted into its new
// section before; once over, the thread moves with no other change.
func TestTheSidebarSortsAsARowsJokeStartsAndEnds(t *testing.T) {
	h := start(t, inboxDir(t))
	th := h.thread("SIP re-INVITE handling")
	raw, _ := json.Marshal(map[string]string{"thread": th.id().String(), "state": "settled", "animation": "settle"})
	if _, err := h.a.setDispositionEffect(raw); err != nil {
		t.Fatal(err)
	}
	if !in(h, catSettled, th) {
		t.Fatal("settled, the thread isn't in Settled before its joke")
	}
	h.tt.Frame()
	if !in(h, catInbox, th) || in(h, catSettled, th) {
		t.Fatal("its joke playing, the thread left the inbox")
	}
	h.tt.Advance(settleLife + time.Second)
	if !in(h, catSettled, th) || in(h, catInbox, th) {
		t.Fatal("its joke over, the thread is still in the inbox")
	}
}

// A snoozed thread comes back to the inbox as its time comes, with no
// other change.
func TestTheSidebarSortsASnoozedThreadBackAsItWakes(t *testing.T) {
	h := start(t, inboxDir(t))
	th := h.thread("SIP re-INVITE handling")
	h.a.backend.SetDisposition(th.env, th.id(), api.Disposition{State: api.Snoozed, Until: h.tt.Now().Add(time.Minute)})
	h.until("the snooze to land", func() bool { return disposition(th).State == api.Snoozed && len(th.Pending) == 0 })
	h.tt.Frame()
	if !in(h, catSnoozed, th) {
		t.Fatal("the thread isn't in Snoozed")
	}
	posts, edits := h.a.posts, h.a.backend.Edits()
	h.tt.Advance(2 * time.Minute)
	if h.a.posts != posts || h.a.backend.Edits() != edits {
		t.Fatal("something besides the time changed")
	}
	if !in(h, catInbox, th) || in(h, catSnoozed, th) {
		t.Fatal("its time come, the thread is still in Snoozed")
	}
}

// A command retried here shows its change again at once, as it did sent.
func TestTheSidebarSortsAThreadWhoseSettleIsRetried(t *testing.T) {
	h := start(t, inboxDir(t))
	th := h.thread("SIP re-INVITE handling")
	cmd := api.NewID()
	th.Pending = append(th.Pending, client.Pending{Command: cmd, Body: api.InboxSet{Disposition: api.Disposition{State: api.Settled}}, Err: errors.New("lost connection")})
	h.a.posts++ // as the failure came
	if !in(h, catInbox, th) {
		t.Fatal("its settle failed, the thread isn't in the inbox")
	}
	h.a.backend.Retry(th.env, th.id(), cmd)
	if !in(h, catSettled, th) || in(h, catInbox, th) {
		t.Fatal("its settle retried, the thread is still in the inbox")
	}
}

// A thread kept past its section's pages, as the one shown, keeps its
// place though another machine has a thread of the same id elsewhere.
func TestTheSidebarKeepsAThreadPastItsPagesBesideAnotherMachinesOfItsID(t *testing.T) {
	rdir := tempDir(t)
	token, err := daemon.Pair(rdir, "gui")
	if err != nil {
		t.Fatal(err)
	}
	remote := engine(t, daemon.Config{Dir: rdir, Listen: []string{"127.0.0.1:0"}}, fake.New(fake.Turn(false)))
	dir := tempDir(t)
	local := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(false)))
	id := api.NewID()
	submit := func(d *daemon.Daemon, body api.Intent) {
		t.Helper()
		if _, err := d.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: id, Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	submit(remote, api.ThreadCreate{Workspace: "/work/remote", Title: "Remote of the same id", Model: fakeModel})
	submit(remote, api.InboxSet{Disposition: api.Disposition{State: api.Settled}})
	submit(local, api.ThreadCreate{Workspace: "/work/local", Title: "Shown, past the first page", Model: fakeModel})
	for i := range sidebarPage + 1 {
		seed(t, local, "/work/local", fmt.Sprintf("Newer %03d", i))
	}
	h := start(t, dir)
	if err := h.addServer(client.Server{Label: "box", Addr: remote.Addrs()[0].String(), Token: token}); err != nil {
		t.Fatal(err)
	}
	th := h.thread("Shown, past the first page")
	h.thread("Remote of the same id")
	h.a.select_(th)
	h.tt.Frame()
	body := h.a.sidebarBodyOf("inbox", 0, catInbox)
	if !slices.ContainsFunc(body.Sections[0].Rows, func(r sidebarRow) bool { return r.Title == th.View.Title }) {
		t.Fatal("the thread shown isn't listed past its section's page")
	}
}
