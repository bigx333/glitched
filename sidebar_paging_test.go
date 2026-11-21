package main

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/daemon"
	engines "glitched/internal/engine"
	"glitched/internal/plugins"
	"glitched/internal/plugins/widget"
	"glitched/internal/provider/fake"
)

// pagedBatch is n threads of pagedDir's, titled by name and their index,
// settled or not, children of the thread titled parent if it's set.
type pagedBatch struct {
	name    string
	n       int
	settled bool
	parent  string
}

// pagedDir is a state directory whose engine holds the batches' threads,
// made one after another, the newest last.
func pagedDir(t *testing.T, batches ...pagedBatch) string {
	t.Helper()
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir, Children: engines.Limits{All: 200}}, fake.New(fake.Turn(false)))
	ep := d.Endpoint()
	ids := map[string]api.ID{}
	for _, b := range batches {
		for i := range b.n {
			title := fmt.Sprintf("%s %03d", b.name, i)
			if b.parent != "" {
				ids[title] = seedChild(t, d, ids[b.parent], title)
				continue
			}
			id := api.NewID()
			body := api.ThreadCreate{Workspace: "/work/paged", Title: title, Model: fakeModel}
			if _, err := ep.Submit(context.Background(), api.Command{ID: api.NewID(), Target: id, Body: body}); err != nil {
				t.Fatal(err)
			}
			if b.settled {
				if _, err := ep.Submit(context.Background(), api.Command{ID: api.NewID(), Target: id, Body: api.InboxSet{Disposition: api.Disposition{State: api.Settled}}}); err != nil {
					t.Fatal(err)
				}
			}
			ids[title] = id
		}
	}
	return dir
}

// listed is what the built-in's list in the part shows: its threads'
// titles, in order, and its show-more rows' names.
func listed(t *testing.T, h *harness, part string) (titles, more []string) {
	t.Helper()
	settlePlugins(t, h.a, h.tt, h.p)
	app := h.a.plugins.App("sidebar")
	if app == nil || app.Err != nil || app.PlaceView("body", part) == nil {
		t.Fatalf("the built-in doesn't draw the sidebar's %s", part)
	}
	var items []widget.ListItem
	app.PlaceView("body", part).Walk(func(n *widget.Node) {
		if n.Type == "list" {
			items = n.Items
		}
	})
	for _, it := range items {
		switch {
		case strings.HasPrefix(it.Key, "m:"):
			more = append(more, it.Label)
		case it.Thread != "":
			id, _ := api.ParseID(it.Thread)
			th, _ := h.a.find(id)
			titles = append(titles, th.View.Title)
		}
	}
	return titles, more
}

func (h *harness) titled(title string) thread {
	h.t.Helper()
	return h.thread(title)
}

// scrollDown scrolls the sidebar's inbox as far down as it goes.
func scrollDown(h *harness) {
	h.tt.Scroll(120, 500, 0, 100000)
	h.tt.Advance(time.Second)
	settlePlugins(h.t, h.a, h.tt, h.p)
}

// A section shows its newest 100 threads, then a row that shows the next
// 100, or the rest; the rows in view stay where they are as a page loads,
// and the keys reach the row, which is named with the count.
func TestTheSidebarShowsAPageOfThreadsAndTheNextOnAsk(t *testing.T) {
	h := start(t, pagedDir(t, pagedBatch{name: "Paged", n: 250}))
	h.until("the threads", func() bool { return len(h.a.itemsIn(catInbox)) == 250 })
	titles, more := listed(t, h, "inbox")
	if len(titles) != 100 || titles[0] != "Paged 249" || titles[99] != "Paged 150" || !slices.Equal(more, []string{"Show 100 more, of 150 not shown"}) {
		t.Fatalf("the inbox lists %d threads, %q to %q, and %q", len(titles), titles[0], titles[len(titles)-1], more)
	}
	scrollDown(h)
	const name = "Show 100 more, of 150 not shown"
	if !h.tt.HasText("Show 100 more") {
		t.Fatalf("no row shows the next page: %q", h.tt.Texts())
	}
	if _, ok := h.tt.Access(name); !ok {
		t.Fatalf("no row is named %q", name)
	}
	for range 80 {
		if h.tt.Focused(name) {
			break
		}
		h.tt.Key(0, ui.KeyTab)
	}
	if !h.tt.Focused(name) {
		t.Fatal("Tab never reaches the row that shows more")
	}
	last, ok := h.tt.Find("Paged 150")
	if !ok {
		t.Fatalf("the inbox's last loaded thread isn't in view at its end: %q", h.tt.Texts())
	}
	h.tt.Key(0, ui.KeySpace)
	titles, more = listed(t, h, "inbox")
	if len(titles) != 200 || titles[199] != "Paged 050" || !slices.Equal(more, []string{"Show 50 more, of 50 not shown"}) {
		t.Fatalf("the next page lists %d threads, to %q, and %q", len(titles), titles[len(titles)-1], more)
	}
	if now, ok := h.tt.Find("Paged 150"); !ok || now.Y != last.Y {
		t.Fatalf("the thread in view moved from %v to %v (%v) as the next page loaded", last.Y, now.Y, ok)
	}
	if err := h.tt.Click("Paged 150"); err != nil {
		t.Fatal(err)
	}
	scrollDown(h)
	if err := h.tt.Click("Show 50 more"); err != nil {
		t.Fatalf("no row shows the rest: %v, %q", err, h.tt.Texts())
	}
	if titles, more = listed(t, h, "inbox"); len(titles) != 250 || len(more) != 0 {
		t.Fatalf("with every page loaded the inbox lists %d threads and %q", len(titles), more)
	}
}

// A thread the user chose shows in its section wherever it sorts, past
// the pages loaded, and stays as the user goes to another; the palette
// finds every thread, shown or not.
func TestTheSidebarShowsAChosenThreadPastItsPages(t *testing.T) {
	h := start(t, pagedDir(t, pagedBatch{name: "Paged", n: 150}))
	h.until("the threads", func() bool { return len(h.a.itemsIn(catInbox)) == 150 })
	h.a.openCommandPalette()
	paletteShows(t, h)
	h.tt.Type("Paged 007")
	paletteShows(t, h)
	h.tt.Key(0, ui.KeyEnter)
	settlePlugins(t, h.a, h.tt, h.p)
	if h.a.current().View.Title != "Paged 007" {
		t.Fatalf("the palette didn't find and open a thread past the inbox's page: %q", h.a.current().View.Title)
	}
	titles, more := listed(t, h, "inbox")
	if len(titles) != 101 || titles[100] != "Paged 007" || !slices.Equal(more, []string{"Show 49 more, of 49 not shown"}) {
		t.Fatalf("with Paged 007 chosen the inbox lists %d threads, to %q, and %q", len(titles), titles[len(titles)-1], more)
	}
	h.a.choose(h.titled("Paged 149"))
	if titles, _ = listed(t, h, "inbox"); !slices.Contains(titles, "Paged 007") {
		t.Fatal("a thread chosen before left the inbox as another was chosen")
	}
}

// A thread that moves section shows in its new one's loaded part, where it
// sorts, though that's past the section's page.
func TestTheSidebarShowsAThreadThatMovedPastItsPages(t *testing.T) {
	h := start(t, pagedDir(t, pagedBatch{name: "Settled old", n: 20, settled: true}, pagedBatch{name: "Mover", n: 1}, pagedBatch{name: "Settled new", n: 120, settled: true}))
	h.until("the threads", func() bool { return len(h.a.itemsIn(catSettled)) == 140 })
	h.a.sectionClosed[catSettled] = false
	titles, _ := listed(t, h, "bottom")
	if len(titles) != 100 || slices.Contains(titles, "Mover 000") {
		t.Fatalf("Settled lists %d threads", len(titles))
	}
	mover := h.titled("Mover 000")
	h.a.backend.SetDisposition(mover.env, mover.id(), api.Disposition{State: api.Settled})
	h.until("the mover settled", func() bool { return mover.View.Disposition.State == api.Settled && len(mover.Pending) == 0 })
	h.until("the mover's row in Settled", func() bool {
		titles, _ = listed(t, h, "bottom")
		return slices.Contains(titles, "Mover 000")
	})
	var sorted []string
	for _, it := range h.a.itemsIn(catSettled) {
		if slices.Contains(titles, it.t.View.Title) {
			sorted = append(sorted, it.t.View.Title)
		}
	}
	if !slices.Equal(titles, sorted) {
		t.Errorf("Settled's rows aren't where they sort: %q, want %q", titles, sorted)
	}
}

// The pages a section loaded stay loaded as it closes and opens, and as
// the user goes from thread to thread; a thread's children page as a
// section's threads do.
func TestTheSidebarsPagesStayAsSectionsCloseAndChildrenPage(t *testing.T) {
	h := start(t, pagedDir(t, pagedBatch{name: "Settled", n: 150, settled: true}, pagedBatch{name: "Parent", n: 1}, pagedBatch{name: "Child", n: 120, parent: "Parent 000"}))
	h.until("the threads", func() bool {
		return len(h.a.itemsIn(catSettled)) == 150 && len(h.a.kids[h.titled("Parent 000").id()]) == 120
	})
	h.a.sectionClosed[catSettled] = false
	h.a.showMore("settled")
	if titles, more := listed(t, h, "bottom"); len(titles) != 150 || len(more) != 0 {
		t.Fatalf("Settled's next page lists %d threads and %q", len(titles), more)
	}
	h.a.sectionClosed[catSettled] = true
	listed(t, h, "bottom")
	h.a.sectionClosed[catSettled] = false
	h.a.choose(h.titled("Settled 003"))
	h.a.choose(h.titled("Parent 000"))
	if titles, _ := listed(t, h, "bottom"); len(titles) != 150 {
		t.Fatalf("Settled closed and opened, and threads chosen, lists %d threads", len(titles))
	}

	parent := h.titled("Parent 000")
	h.a.childrenOpen[parent.id()] = true
	titles, more := listed(t, h, "inbox")
	kids := slices.DeleteFunc(slices.Clone(titles), func(s string) bool { return !strings.HasPrefix(s, "Child") })
	if len(kids) != 100 || !slices.Equal(more, []string{"Show 20 more, of 20 not shown"}) {
		t.Fatalf("the parent's children list %d and %q", len(kids), more)
	}
	h.a.showMore(parent.id().String())
	titles, more = listed(t, h, "inbox")
	if kids = slices.DeleteFunc(titles, func(s string) bool { return !strings.HasPrefix(s, "Child") }); len(kids) != 120 || len(more) != 0 {
		t.Fatalf("the children's next page lists %d and %q", len(kids), more)
	}
}

// However many threads move section at once, a page of them at most shows
// past the pages, so a copy's list stays in its bounds; the thread shown
// shows still.
func TestTheSidebarKeepsAPageOfMovedThreadsAtMost(t *testing.T) {
	h := start(t, pagedDir(t, pagedBatch{name: "Settled old", n: 20, settled: true}, pagedBatch{name: "Mover", n: 250}, pagedBatch{name: "Settled new", n: 120, settled: true}))
	h.until("the threads", func() bool { return len(h.a.itemsIn(catSettled)) == 140 })
	h.a.sectionClosed[catSettled] = false
	listed(t, h, "bottom")
	var movers []thread
	for _, it := range h.a.itemsIn(catInbox) {
		movers = append(movers, it.t)
	}
	last := movers[len(movers)-1]
	h.a.choose(last)
	for _, m := range movers {
		h.a.backend.SetDisposition(m.env, m.id(), api.Disposition{State: api.Settled})
	}
	h.until("the movers settled", func() bool {
		for _, m := range movers {
			if m.View.Disposition.State != api.Settled || len(m.Pending) > 0 {
				return false
			}
		}
		return len(h.a.itemsIn(catSettled)) == 390
	})
	titles, more := listed(t, h, "bottom")
	if len(titles) > 2*sidebarPage+1 || !slices.Contains(titles, last.View.Title) || !slices.Equal(more, []string{fmt.Sprintf("Show 100 more, of %d not shown", 390-len(titles))}) {
		t.Fatalf("Settled lists %d threads, the one shown among them: %v, and %q", len(titles), slices.Contains(titles, last.View.Title), more)
	}
}

// addThreads puts n threads under parent straight into the local machine's
// threads, newer as they go, past the engine's bounds on children.
func addThreads(h *harness, n int, parent api.ID, prefix string) []api.ID {
	e := h.local()
	now := h.tt.Now()
	ids := make([]api.ID, n)
	for i := range n {
		id := api.NewID()
		ids[i] = id
		e.Threads[id] = &client.Thread{View: api.Thread{
			ID: id, Parent: parent, Title: fmt.Sprintf("%s %05d", prefix, i),
			Workspace: "/work/paged", Model: fakeModel, UpdatedAt: now.Add(time.Duration(i) * time.Millisecond),
		}}
		e.Membership++
	}
	return ids
}

// A child lifted into Working shows there and under its parent, and so do
// its children's pages, each with its own row for the next.
func TestTheSidebarPagesALiftedChildsChildrenWhereverItShows(t *testing.T) {
	h := start(t, tempDir(t))
	parent := addThreads(h, 1, api.ID{}, "Snoozed root")[0]
	working := addThreads(h, 1, parent, "Working child")[0]
	addThreads(h, 101, working, "Grandchild")
	e := h.local()
	e.Threads[parent].View.Disposition = api.Disposition{State: api.Snoozed, Until: h.tt.Now().Add(24 * time.Hour)}
	e.Threads[working].View.Run = api.RunRunning
	h.a.sectionClosed[catWorking], h.a.sectionClosed[catSnoozed] = false, false
	h.a.childrenOpen[parent], h.a.childrenOpen[working] = true, true
	titles, more := listed(t, h, "bottom")
	if !h.a.lifted(thread{e, e.Threads[working]}) || len(slices.DeleteFunc(titles, func(s string) bool { return !strings.HasPrefix(s, "Grandchild") })) != 200 || !slices.Equal(more, []string{"Show 1 more, of 1 not shown", "Show 1 more, of 1 not shown"}) {
		t.Fatalf("the lifted child's children show %q", more)
	}
}

// However many pages the user asks for, and however many threads move in,
// a copy's list holds no more than a list can; a row says the rest are
// the palette's.
func TestTheSidebarsListStaysInItsBounds(t *testing.T) {
	h := start(t, tempDir(t))
	movers := addThreads(h, 100, api.ID{}, "Old mover")
	addThreads(h, 20100, api.ID{}, "New")
	e := h.local()
	for _, id := range movers {
		e.Threads[id].View.UpdatedAt = h.tt.Now().Add(-time.Hour)
	}
	h.a.projects = h.a.gather()
	h.a.emitThreadChanges()
	if _, err := h.a.plugins.Copy("sidebar"); err != nil {
		t.Fatal(err)
	}
	s := h.a.plugins.App("sidebar")
	draw := func(version string) []sidebarRow {
		t.Helper()
		h.a.posts++ // as the engine's changes to the threads come
		body := h.a.sidebarBodyOf("inbox", 0, catInbox)
		s.DrawFirst("body", []plugins.PlaceItem{{Key: "inbox", Version: version, Context: body}}, []string{"inbox"}, time.Now().Add(5*time.Second))
		if s.Err != nil {
			t.Fatalf("%s, the copy failed: %v", version, s.Err)
		}
		var items int
		s.PlaceView("body", "inbox").Walk(func(n *widget.Node) {
			if n.Type == "list" {
				items = len(n.Items)
			}
		})
		if items > widget.MaxListItems {
			t.Fatalf("%s, the list holds %d items", version, items)
		}
		return body.Sections[0].Rows
	}
	draw("first")
	for range 250 {
		if _, err := h.a.pluginEffect("sidebar", plugins.Effect{Type: "sidebar.more", Args: json.RawMessage(`{"key":"inbox"}`)}); err != nil {
			t.Fatal(err)
		}
	}
	rows := draw("every page")
	if last := rows[len(rows)-1]; !last.Full || last.More+len(rows)-1 != 20200 {
		t.Fatalf("past the list's room, the last row is %+v of %d", last, len(rows))
	}
	for _, id := range movers {
		e.Threads[id].View.Run = api.RunRunning
	}
	h.a.emitThreadChanges()
	for _, id := range movers {
		e.Threads[id].View.Run = api.RunIdle
	}
	h.a.emitThreadChanges()
	draw("the movers back")
}

// A thread settled here leaves the inbox at once, before the engine
// answers or the next frame.
func TestTheInboxSortsAThreadSettledHereAtOnce(t *testing.T) {
	h := start(t, inboxDir(t))
	th := h.thread("SIP re-INVITE handling")
	has := func(c category) bool {
		return slices.ContainsFunc(h.a.itemsIn(c), func(it inboxItem) bool { return it.t.id() == th.id() })
	}
	if !has(catInbox) || has(catSettled) {
		t.Fatal("the thread isn't in the inbox")
	}
	h.a.backend.SetDisposition(th.env, th.id(), api.Disposition{State: api.Settled})
	if has(catInbox) || !has(catSettled) {
		t.Error("settled here, the thread is still in the inbox till the next frame")
	}
}
