package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/daemon"
	"glitched/internal/plugins"
	"glitched/internal/plugins/bundled"
	"glitched/internal/plugins/pack"
	"glitched/internal/provider/fake"
)

// queueTexts is what the queue over the composer lists, in its order.
func queueTexts(h *harness) []string {
	var out []string
	for _, u := range waiting(h.a.current()) {
		out = append(out, u.Text)
	}
	return out
}

// The messages that wait their turn are listed over the composer, not in
// the timeline, each with what can be done with it: Remove takes one out,
// Down and Up move it past its neighbour, Edit changes its words in place,
// ⌘⏎ saving them and Escape leaving them, and Send now hands it to the
// agent's turn. None of it stops the run.
func TestTheQueueOverTheComposerManagesEachMessage(t *testing.T) {
	h, th := asking(t, runTests)
	run := h.a.current().View.Turn.Run
	for _, text := range []string{"First queued.", "Second queued.", "Third queued."} {
		h.a.backend.Send(th.env, th.id(), text, api.AfterActive)
	}
	h.until("the three queued over the composer", func() bool {
		return slices.Equal(queueTexts(h), []string{"First queued.", "Second queued.", "Third queued."}) && h.tt.HasText("Third queued.")
	})
	for _, e := range entriesOf(h.a.current()) {
		if e.kind == entryUser && e.text != "Run the sidebar tests." {
			t.Fatalf("the timeline lists %q, which waits its turn in the queue", e.text)
		}
	}

	h.click("Remove")
	h.until("the first removed", func() bool { return slices.Equal(queueTexts(h), []string{"Second queued.", "Third queued."}) })
	h.click("Down")
	h.until("the second moved down", func() bool { return slices.Equal(queueTexts(h), []string{"Third queued.", "Second queued."}) })
	h.click("Up")
	h.until("the second moved back up", func() bool { return slices.Equal(queueTexts(h), []string{"Second queued.", "Third queued."}) })

	h.click("Edit")
	h.until("the edit", func() bool { return h.tt.HasText("Save") })
	h.tt.Key(0, ui.KeyEscape)
	h.until("the edit left", func() bool { return !h.tt.HasText("Save") && h.tt.HasText("Edit") })
	h.click("Edit")
	h.until("the edit again", func() bool { return h.tt.HasText("Save") })
	h.a.queueEditing.text = "Second, shorter."
	h.tt.Frame()
	h.tt.Key(ui.Cmd, ui.KeyEnter)
	h.until("the edit saved", func() bool {
		return slices.Equal(queueTexts(h), []string{"Second, shorter.", "Third queued."}) && !h.tt.HasText("Save")
	})
	if v := h.a.current().View; v.Turn == nil || v.Turn.Run != run || v.Run != api.RunWaiting {
		t.Fatalf("saving with ⌘⏎ changed the run: %v, %+v; want it waiting on its approval", v.Run, v.Turn)
	}

	h.click("Send now")
	h.until("the message steered into the turn", func() bool {
		return slices.Equal(queueTexts(h), []string{"Third queued."}) && h.a.current().View.Queued == 1
	})
	if v := h.a.current().View; v.Turn == nil || v.Turn.Run != run {
		t.Fatalf("Send now changed the run: %+v; want the message steered into it", v.Turn)
	}
	h.tt.Advance(time.Second)
}

// The built-in Queue's main.js and its native copy draw the same row,
// node for node, wherever the row is.
func TestTheBuiltInQueueIsItsNativeCopy(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "queue")
	src, err := fs.Sub(bundled.FS, "queue")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(folder, src); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(folder, "signature.json"))
	key, err := pack.LoadKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := pack.Sign(folder, key); err != nil {
		t.Fatal(err)
	}
	q := make(chan func(), 1024)
	h := plugins.Open(t.TempDir(), plugins.Options{Post: func(fn func()) { q <- fn }, Bundled: os.DirFS(root), ReleaseKeys: []string{key.Public()}})
	t.Cleanup(h.Close)
	s := h.App("queue")
	if s == nil {
		t.Fatalf("the built-in didn't load: %v", h.Failed("queue"))
	}
	for i, r := range []queueRow{
		{Key: "a", Index: 0, Count: 1},
		{Key: "b", Index: 1, Count: 3, Held: true},
		{Key: "c", Index: 2, Count: 3, Editing: true},
	} {
		key := string(rune('a' + i))
		s.SetItems("row", []plugins.PlaceItem{{Key: key, Version: key, Context: r}})
		deadline := time.After(5 * time.Second)
		for s.PlaceView("row", key) == nil && s.Err == nil {
			select {
			case fn := <-q:
				fn()
			case <-deadline:
				t.Fatalf("the built-in drew nothing for %+v: %v", r, s.Err)
			}
		}
		if got, want := s.PlaceView("row", key), queueRowNode(r); !reflect.DeepEqual(got, want) {
			g, _ := json.Marshal(got)
			w, _ := json.Marshal(want)
			t.Errorf("for %+v main.js drew %s, its native copy %s", r, g, w)
		}
	}
}

// A queued message's row keeps the keyboard focus as messages are queued
// ahead of it: the focus follows the message, not the place it was in.
func TestAQueuedRowKeepsTheFocus(t *testing.T) {
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(false)))
	seed(t, d, "/work/glitched", "Queued history")
	h := start(t, dir)
	th := h.thread("Queued history")
	h.a.select_(th)
	h.until("initial snapshot", func() bool { return th.Transcript != nil && th.Transcript.Cursor.Seq > 0 })
	tr := th.Transcript
	queue := func(text string, pos int64, mode api.DispatchMode) {
		applyTo(t, tr, tr.Cursor.Log, api.MessageQueued{Message: api.NewID(), Text: text, Pos: pos, Mode: mode, Held: mode == api.Defer})
		h.a.changed()
	}
	for i := range 12 {
		queue(fmt.Sprintf("Queued turn %d", i), int64(i), api.Defer)
	}
	h.tt.Advance(time.Second)
	removeOf := func(text string) bool {
		row, ok := h.tt.Find(text)
		if !ok {
			return false
		}
		for _, n := range h.tt.AccessAll() {
			if n.Focused && n.Name == "Remove" {
				mid := n.Bounds.Y + n.Bounds.H/2
				return mid > row.Y-4 && mid < row.Y+row.H+4
			}
		}
		return false
	}
	for range 400 {
		if removeOf("Queued turn 3") {
			break
		}
		h.tt.Key(0, ui.KeyTab)
		h.tt.Frame()
	}
	if !removeOf("Queued turn 3") {
		t.Fatalf("setup: Tab never reached the Remove of turn 3: %q", h.tt.Texts())
	}
	queue("New first", -1, api.RestartActive)
	queue("New second", -2, api.RestartActive)
	h.tt.Frame()
	if !removeOf("Queued turn 3") {
		t.Fatalf("after two messages queued ahead, the focus left turn 3's Remove: %q", h.tt.Texts())
	}
}

// focusedNode is what has the keyboard focus, as assistive technology
// reads it.
func focusedNode(h *harness) (ui.AccessInfo, bool) {
	for _, n := range h.tt.AccessAll() {
		if n.Focused {
			return n, true
		}
	}
	return ui.AccessInfo{}, false
}

// tabTo tabs till name has the focus.
func tabTo(t *testing.T, h *harness, name string) {
	t.Helper()
	for range 150 {
		if n, ok := focusedNode(h); ok && n.Name == name {
			return
		}
		h.tt.Key(0, ui.KeyTab)
		h.tt.Frame()
	}
	t.Fatalf("Tab never reached %q", name)
}

// A queued message's words are edited from the keyboard alone: the area
// they're edited in says what it is, and as the edit ends, saved or not,
// the focus goes back to the row's Edit.
func TestAQueuedMessageIsEditedFromTheKeyboard(t *testing.T) {
	h := enterReady(t, burst{}, "Start work.")
	h.until("the agent at work", func() bool { return h.a.current().View.Run == api.RunRunning })
	th := h.a.current()
	h.a.backend.Send(th.env, th.id(), "The queued text", api.AfterActive)
	h.until("the queued row", func() bool { _, edit := h.tt.Access("Edit"); return len(queueTexts(h)) == 1 && edit })
	tabTo(t, h, "Edit")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	if n, ok := focusedNode(h); !ok || !n.Multiline || n.Name != "Edit queued message" {
		t.Fatalf("Edit gave the focus to %+v; want the area named Edit queued message", n)
	}
	h.tt.Key(ui.Cmd, ui.KeyA)
	h.tt.Type("Changed by keyboard")
	h.tt.Key(ui.Cmd, ui.KeyEnter)
	h.until("the edit saved", func() bool { q := queueTexts(h); return len(q) == 1 && q[0] == "Changed by keyboard" })
	if n, ok := focusedNode(h); !ok || n.Name != "Edit" {
		t.Errorf("after the save the focus is on %+v; want the row's Edit", n)
	}
	tabTo(t, h, "Edit")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	h.tt.Type("Discard this")
	h.tt.Key(0, ui.KeyEscape)
	h.tt.Frame()
	if n, ok := focusedNode(h); !ok || n.Name != "Edit" {
		t.Errorf("after Escape the focus is on %+v; want the row's Edit", n)
	}
	if q := queueTexts(h); h.tt.HasText("Save") || q[0] != "Changed by keyboard" {
		t.Fatalf("Escape kept the edit: %q", q)
	}
}
