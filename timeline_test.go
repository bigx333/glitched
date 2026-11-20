package main

import (
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
	"glitched/internal/provider/fake"
)

// synthetic is a transcript of n rows: turns of a user message, a run's
// work and the reply, as long threads have them.
func synthetic(n int) []client.Entry {
	var es []client.Entry
	at := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	for i := 0; len(es) < n; i++ {
		run := api.NewID()
		user := fmt.Sprintf("Turn %d: look at chat.go and tell me why the list jumps when a reply streams in.", i)
		if i%5 == 0 {
			user += "\n\n```go\nfor i := range es {\n\tdraw(es[i])\n}\n```"
		}
		es = append(es, &client.UserEntry{Message: api.NewID(), Text: user, State: client.Sent})
		if len(es) == n {
			break
		}
		exit0, exit1 := 0, 1
		steps := []client.WorkStep{
			{Item: "s1", Kind: "command", Title: "go test ./...", Status: api.WorkCompleted, Exit: &exit0, Started: at, Ended: at.Add(2 * time.Second)},
			{Item: "s2", Kind: "edit", Title: "Edited chat.go", Status: api.WorkCompleted, Started: at, Ended: at},
			{Item: "s3", Kind: "search", Title: "Searched for ListState", Status: api.WorkCompleted, Started: at, Ended: at},
		}
		if i%7 == 0 {
			steps = append(steps, client.WorkStep{Item: "s4", Kind: "command", Title: "go vet .", Status: api.WorkCompleted, Exit: &exit1, Output: "chat.go:12: unused variable\nchat.go:40: unreachable code\nexit status 1", Started: at, Ended: at.Add(time.Second)})
		}
		es = append(es, &client.WorkEntry{Run: run, Items: steps, Started: at, Ended: at.Add(40 * time.Second)})
		if len(es) == n {
			break
		}
		es = append(es, &client.AssistantEntry{Run: run, Text: syntheticReply(i)})
	}
	return es
}

func syntheticReply(i int) string {
	s := fmt.Sprintf("Reply %d. The list keeps its place by key, so a row that changes height above the fold moves everything under it. "+
		"`ListState` measures rows as they come into view and estimates the rest, which is why a long reply that streams in can shift the view.\n\n"+
		"- The row's key stays the same while it streams.\n- Heights above the viewport are estimates until drawn.\n- `FollowEnd` glides only while you are at the end.\n\n"+
		"I changed the anchor to the first visible row and kept the offset inside it, which holds the view still as rows above it settle.", i)
	if i%4 == 0 {
		s += "\n\n```go\nfunc (s *ListState) anchor() (row int, offset float32) {\n\tfirst, _ := s.Visible()\n\treturn first, s.offsetIn(first)\n}\n```"
	}
	return s
}

// turnText is what the user said in turn i of synthetic.
func turnText(i int) string {
	return fmt.Sprintf("Turn %d: look at chat.go and tell me why the list jumps when a reply streams in.", i)
}

// accessNames are the names of the window's accessibility tree with
// prefix, in its order, and how many of them show.
func accessNames(tt *ui.Tester, prefix string) (names []string, shown int) {
	for _, n := range tt.AccessAll() {
		if strings.HasPrefix(n.Name, prefix) {
			names = append(names, n.Name)
			if !n.Offscreen {
				shown++
			}
		}
	}
	return names, shown
}

// TestALongTimeline drives a thread of 3,000 rows: it opens at its end,
// builds only what shows, keeps its place as rows above it change, follows
// a reply streaming in at its end, and its markers and work logs act as on
// a short thread.
func TestALongTimeline(t *testing.T) {
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(false)))
	seed(t, d, "/work/t3code", "A long thread", "Start")
	seed(t, d, "/work/t3code", "Another thread", "Hello")
	h := start(t, dir)
	long, other := h.thread("A long thread"), h.thread("Another thread")
	h.a.select_(long)
	h.until("its transcript", func() bool { return long.Transcript != nil && len(long.Transcript.Entries) > 0 })
	tr := long.Transcript
	tr.Entries = synthetic(3000)
	h.a.changed()
	l := func() *ui.ListState { return h.a.listFor(long.id()) }
	// changed is the transcript changing as the client posts it: a frame
	// follows.
	changed := func() {
		h.a.changed()
		h.tt.Frame()
	}
	// apply folds an event in, as following the thread does.
	apply := func(b api.EventBody) {
		p, err := json.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		tr.Apply(api.Event{Cursor: api.Cursor{Log: tr.Cursor.Log, Seq: tr.Cursor.Seq + 1}, Type: api.EventType(b), Schema: api.EventSchema, At: time.Now(), Payload: p})
	}
	// The y of a text that shows, to tell the list kept its place.
	at := func(t *testing.T, text string) float32 {
		t.Helper()
		r, ok := h.tt.Find(text)
		if !ok {
			t.Fatalf("%q doesn't show", text)
		}
		return r.Y
	}

	t.Run("opens at its end in its first frame", func(t *testing.T) {
		h.a.select_(other)
		h.tt.Advance(time.Second)
		delete(h.a.lists, long.id())
		h.a.select_(long)
		h.tt.Frame()
		if !h.tt.HasText("Reply 999. The list keeps its place") {
			t.Fatalf("the first frame doesn't show the last reply: %q", h.tt.Texts())
		}
		if first, last := l().Visible(); last != 2999 || last-first > 10 {
			t.Errorf("rows %d to %d show, want the last few", first, last)
		}
		if slices.Contains(h.tt.Texts(), turnText(1)) {
			t.Error("the first turn's message was built, far above what shows")
		}
	})

	t.Run("its markers are the ticks that show, in order", func(t *testing.T) {
		h.tt.Advance(time.Second)
		// Those that show, then the last, for Shift-Tab to come to.
		names, shown := accessNames(h.tt, "Go to: ")
		if want := markersShown(1000, 860) + 1; len(names) != want {
			t.Fatalf("%d markers in the accessibility tree, want %d", len(names), want)
		}
		for i, name := range names {
			k := i
			if i == len(names)-1 {
				k = 999
			}
			if want := "Go to: " + turnText(k); name != want {
				t.Fatalf("marker %d is %q, want %q", i, name, want)
			}
		}
		if shown < len(names)-5 {
			t.Errorf("only %d of the %d markers built show", shown, len(names))
		}
		h.click("Go to: " + turnText(5))
		h.tt.Advance(time.Second)
		if first, _ := l().Visible(); first != 15 {
			t.Errorf("the marker of turn 5 scrolled to row %d, want 15", first)
		}
		at(t, turnText(5))
	})

	t.Run("Tab walks every marker, and Shift-Tab back", func(t *testing.T) {
		h.click("Go to: " + turnText(0))
		h.tt.Advance(time.Second)
		names, _ := accessNames(h.tt, "Go to: ")
		y0 := markerY(t, h.tt, turnText(0))
		for k := 1; k < 1000; k++ {
			h.tt.Key(0, ui.KeyTab)
			h.tt.Frame()
			if !h.tt.Focused("Go to: " + turnText(k)) {
				t.Fatalf("Tab %d didn't move to the marker of turn %d", k, k)
			}
			if k%97 == 0 {
				// Where it would be with every marker built, out of view
				// past the window, and the markers that show are as they were.
				if y := markerY(t, h.tt, turnText(k)); y-y0 != float32(k*markerPitch) {
					t.Errorf("the marker of turn %d is %v below the first, want %v", k, y-y0, k*markerPitch)
				}
				if now, _ := accessNames(h.tt, "Go to: "); !slices.Equal(now[:len(names)-1], names[:len(names)-1]) {
					t.Errorf("Tab to turn %d changed the markers that show", k)
				}
			}
		}
		for k := 998; k > 990; k-- {
			h.tt.Key(ui.Shift, ui.KeyTab)
			h.tt.Frame()
			if !h.tt.Focused("Go to: " + turnText(k)) {
				t.Fatalf("Shift-Tab didn't move back to the marker of turn %d", k)
			}
		}
		h.tt.Key(0, ui.KeyEnter)
		h.tt.Advance(time.Second)
		if first, _ := l().Visible(); first != 3*991 {
			t.Errorf("Enter on the marker of turn 991 scrolled to row %d, want %d", first, 3*991)
		}
	})

	t.Run("keeps its place as a row above grows", func(t *testing.T) {
		l().ScrollTo(300, ui.Start)
		h.tt.Frame()
		h.tt.Advance(time.Second)
		y := at(t, turnText(101))
		// A command of the work above, out of view, fails, and shows what
		// it printed.
		exit := 1
		apply(api.WorkItem{Run: tr.Entries[298].(*client.WorkEntry).Run, Item: "s1", Status: api.WorkFailed, Exit: &exit,
			Output: strings.Repeat("--- FAIL: TestAnother line it printed\n", 6) + "exit status 1"})
		changed()
		h.tt.Advance(time.Second)
		if got := at(t, turnText(101)); got != y {
			t.Errorf("the turn in view moved from %v to %v as a reply above it grew", y, got)
		}
		// Brought into view, it's measured: what showed moves by the scroll.
		wheel(h, -120)
		if got := at(t, turnText(101)); got-y < 119 || got-y > 121 {
			t.Errorf("scrolling up 120 moved the turn by %v", got-y)
		}
		// The work above shows what the command printed.
		l().ScrollTo(298, ui.Start)
		h.tt.Frame()
		h.tt.Advance(time.Second)
		if !slices.ContainsFunc(h.tt.Texts(), func(s string) bool { return strings.Contains(s, "TestAnother line it printed") }) {
			t.Error("the work above doesn't show what its command printed")
		}
		l().ScrollTo(300, ui.Start)
		h.tt.Frame()
		h.tt.Advance(time.Second)
	})

	t.Run("keeps its place as older rows come above it", func(t *testing.T) {
		y := at(t, turnText(101))
		older := synthetic(30)
		for _, e := range older {
			if u, ok := e.(*client.UserEntry); ok {
				u.Text = "Older: " + u.Text
			}
		}
		tr.Entries = append(older, tr.Entries...)
		changed()
		h.tt.Advance(time.Second)
		if got := at(t, turnText(101)); got != y {
			t.Errorf("the turn in view moved from %v to %v as rows came above it", y, got)
		}
		names, _ := accessNames(h.tt, "Go to: ")
		if want := "Go to: Older: " + turnText(0); names[0] != want {
			t.Errorf("the first marker is %q, want %q", names[0], want)
		}
	})

	t.Run("a work log folds and unfolds in place", func(t *testing.T) {
		l().ScrollToEnd()
		h.tt.Frame()
		h.tt.Advance(time.Second)
		// Whether the log whose head is at head shows its first step under
		// it.
		open := func(head ui.Rect) bool {
			for _, n := range h.tt.AccessAll() {
				if n.Name == "go test ./..." && n.Bounds.Y > head.Y && n.Bounds.Y < head.Y+head.H+40 {
					return true
				}
			}
			return false
		}
		// The last work log's head in view.
		last := func() (head ui.Rect) {
			for _, n := range h.tt.AccessAll() {
				if n.Name == "Worked for 40s 3 steps" && !n.Offscreen {
					head = n.Bounds
				}
			}
			return head
		}
		head := last()
		if head.H == 0 || !open(head) {
			t.Fatal("no open work log shows at the end")
		}
		h.tt.ClickAt(head.X+head.W/2, head.Y+head.H/2)
		h.tt.Advance(time.Second)
		if head = last(); open(head) {
			t.Fatal("a click on its head didn't fold the work log")
		}
		h.tt.ClickAt(head.X+head.W/2, head.Y+head.H/2)
		h.tt.Advance(time.Second)
		if !open(last()) {
			t.Error("a second click didn't unfold the work log")
		}
	})

	t.Run("follows a reply streaming in at its end", func(t *testing.T) {
		l().ScrollToEnd()
		h.tt.Frame()
		h.tt.Advance(time.Second)
		reply := &client.AssistantEntry{Run: api.NewID(), Streaming: true}
		tr.Entries = append(tr.Entries, reply)
		for _, w := range strings.SplitAfter(strings.Repeat(syntheticReply(7)+"\n\n", 2), ". ") {
			reply.Text += w
			changed()
			h.tt.Advance(time.Second / 30)
		}
		h.tt.Advance(time.Second)
		if !l().AtEnd() {
			t.Fatal("the list stopped following the end as the reply streamed")
		}
		if _, last := l().Visible(); last != len(entriesOf(long))-1 {
			t.Errorf("row %d is the last shown, not the reply", last)
		}
		// Scrolled up, it stays where it is as the reply goes on, and
		// Jump to latest takes it back.
		wheel(h, -400)
		first, last := l().Visible()
		es := entriesOf(long)
		k := slices.IndexFunc(es[first:last+1], func(e entry) bool { return e.kind == entryUser && !strings.Contains(e.text, "```") })
		if k < 0 {
			t.Fatal("no message shows after scrolling up")
		}
		shown := es[first+k].text
		y := at(t, shown)
		for range 4 {
			reply.Text += "More words, and more. "
			changed()
			h.tt.Advance(time.Second / 30)
		}
		h.tt.Advance(time.Second)
		if l().AtEnd() || at(t, shown) != y {
			t.Fatal("scrolled up, the list moved as the reply streamed")
		}
		h.click("Jump to latest")
		h.tt.Advance(time.Second)
		if !l().AtEnd() {
			t.Error("Jump to latest didn't take the list to its end")
		}
		reply.Streaming = false
		changed()
	})
	t.Run("a selection dragged from one reply into the next stays in the first", func(t *testing.T) {
		es := entriesOf(long)
		k := slices.IndexFunc(es, func(e entry) bool { return strings.HasPrefix(e.text, "Reply 100. ") })
		l().ScrollTo(k, ui.Start)
		h.tt.Frame()
		h.tt.Advance(time.Second)
		from := startsWith(h, "Reply 100. ")
		to := startsWith(h, "Reply 101. ")
		drag(h, from.X+2, from.Y+from.H/2, to.X+120, to.Y+to.H/2)
		// A reply's selection is its own: the drag selects the first reply
		// from where it pressed to its end, which the quote bar offers.
		if got := h.a.quote.of; got.key != es[k].key {
			t.Fatalf("the selection quotes row %q, want reply 100's, %q", got.key, es[k].key)
		}
		if text := h.a.quote.read.text; !strings.Contains(text, "FollowEnd glides only") || strings.Contains(text, "Reply 101") {
			t.Errorf("the selection is %q, want the rest of reply 100 alone", text)
		}
	})

	t.Run("its accessibility tree is what shows, however long the thread", func(t *testing.T) {
		nodes := func(rows int) int {
			tr.Entries = synthetic(rows)
			changed()
			l().ScrollToEnd()
			h.tt.Advance(time.Second)
			return len(h.tt.AccessAll())
		}
		short, long := nodes(300), nodes(30000)
		t.Logf("%d accessibility nodes at 300 rows, %d at 30,000", short, long)
		if long > short+5 {
			t.Errorf("%d accessibility nodes at 30,000 rows, %d at 300", long, short)
		}
	})
}

// markerY is the top of the marker of the turn that said text, built
// whether or not it shows.
func markerY(t *testing.T, tt *ui.Tester, text string) float32 {
	t.Helper()
	for _, n := range tt.AccessAll() {
		if n.Name == "Go to: "+text {
			return n.Bounds.Y
		}
	}
	t.Fatalf("no marker for %q", text)
	return 0
}
