package main

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
)

// sendNows is the queue's Send now buttons, row by row.
func sendNows(t *testing.T, tt *ui.Tester) []ui.AccessInfo {
	t.Helper()
	var send []ui.AccessInfo
	for _, n := range tt.AccessAll() {
		if n.Name == "Send now" && n.Role == ui.RoleButton {
			send = append(send, n)
		}
	}
	if len(send) == 0 {
		t.Fatalf("no Send now in %q", tt.Texts())
	}
	return send
}

// labelled is whether the queue draws its Send nows with their label:
// an icon alone is about as wide as it's tall.
func labelled(t *testing.T, tt *ui.Tester) bool {
	t.Helper()
	b := sendNows(t, tt)[0].Bounds
	return b.W > 2*b.H
}

// natural is how wide text is at the queue's 12 points, unconstrained.
func natural(text string) float32 {
	var w float32
	tt := ui.NewTester(func(c *ui.Context) { w, _ = c.MeasureText(0, ui.Span{Text: text, Size: 12}) }, 200, 100)
	tt.Frame()
	return w
}

// shownWhole fails t if a row with a labelled Send now leaves any of
// messages less room than its words take.
func shownWhole(t *testing.T, tt *ui.Tester, when string, messages ...string) {
	t.Helper()
	if !labelled(t, tt) {
		return
	}
	for _, m := range messages {
		r, ok := tt.Find(oneLine(m))
		if !ok {
			continue
		}
		if want := natural(oneLine(m)); r.W < want-0.5 {
			t.Errorf("%s, %q has %.1f of the %.1f points it takes beside a labelled Send now", when, m, r.W, want)
		}
	}
}

// pluginRows replaces composer.queue with js, granted, and waits till each
// of n rows shows "Plugin row".
func pluginRows(t *testing.T, h *harness, n int, js string) {
	t.Helper()
	writeOwnPlugin(t, h.a, "rows", replacing("rows", "composer.queue"), js)
	if err := h.a.plugins.SetGranted("rows", "ui.replace", true); err != nil {
		t.Fatal(err)
	}
	h.until("the plugin's rows", func() bool {
		k := 0
		for _, s := range h.tt.Texts() {
			if s == "Plugin row" {
				k++
			}
		}
		return k == n
	})
	h.tt.Advance(time.Second)
}

// iconOnly fails t unless every Send now in the queue is drawn as its
// icon, named and with its name as its tooltip.
func iconOnly(t *testing.T, tt *ui.Tester, when string) {
	t.Helper()
	for _, n := range sendNows(t, tt) {
		if n.Bounds.W > 2*n.Bounds.H || n.Description != "Send now" {
			t.Errorf("%s, Send now is drawn %.1f by %.1f, described %q; want its icon, described as itself", when, n.Bounds.W, n.Bounds.H, n.Description)
		}
	}
}

// A plugin's row lays itself out as it likes, here in 60% of the queue:
// its Send now is drawn as its icon, in the first frame at each width as
// the window widens and narrows, and the queue settles.
func TestAPluginsQueueRowDrawsSendNowAsItsIcon(t *testing.T) {
	h, queue := heldQueue(t, 0)
	text := strings.Repeat("word ", 9) + "last"
	queue(text, 0, api.Defer)
	pluginRows(t, h, 1, `export function place({items}) {
		return items.map(() => ({type:"row", grow:true, maxWidthPercent:60, gap:2, align:"center", children:[
			{type:"native", id:"text", name:"queue.text", grow:true},
			{type:"native", id:"send", name:"queue.steer"},
			{type:"text", id:"marker", text:"Plugin row"}
		]}))
	}`)
	for _, w := range []int{940, 1120, 940, 1120, 760, 1320} {
		h.tt.SetSize(w, 860)
		shownWhole(t, h.tt, "at once at "+strconv.Itoa(w)+" points", text)
		iconOnly(t, h.tt, "at "+strconv.Itoa(w)+" points")
		if h.tt.FramePending() {
			t.Errorf("at %d points the queue keeps asking for frames", w)
		}
	}
}

// The built-in row in a plugin's composer, here 60% of the room, is laid
// out where Glitched can't tell before it is: its Send now is drawn as its
// icon at each width.
func TestTheQueueInAPluginsComposerDrawsSendNowAsItsIcon(t *testing.T) {
	h, queue := heldQueue(t, 0)
	text := strings.Repeat("word ", 7) + "last"
	queue(text, 0, api.Defer)
	writeOwnPlugin(t, h.a, "outer", replacing("outer", "composer"), `export function place({items}) {
		return items.map(() => ({type:"column", grow:true, maxWidthPercent:60, children:[
			{type:"native", id:"input", name:"composer.input"},
			{type:"text", id:"marker", text:"Outer plugin"}
		]}))
	}`)
	if err := h.a.plugins.SetGranted("outer", "ui.replace", true); err != nil {
		t.Fatal(err)
	}
	h.until("the plugin's composer", func() bool { return h.tt.HasText("Outer plugin") })
	for _, w := range []int{940, 1120, 940, 1120, 760, 1320} {
		h.tt.SetSize(w, 860)
		shownWhole(t, h.tt, "at once at "+strconv.Itoa(w)+" points", text)
		iconOnly(t, h.tt, "at "+strconv.Itoa(w)+" points")
	}
}

// In Glitched's own row and composer, Send now keeps its label exactly
// while every message fits beside it: labelled, no message is cut; drawn
// as its icon, the longest wouldn't have fit beside the label. Checked in
// the first frame at each width, with the sidebar shown and hidden.
func TestSendNowKeepsItsLabelExactlyWhileTheMessagesFit(t *testing.T) {
	h, queue := heldQueue(t, 0)
	long := strings.Repeat("word ", 8) + "last"
	queue("short", 0, api.Defer)
	queue(long, 1, api.Defer)
	h.tt.SetSize(1320, 860)
	h.tt.Advance(time.Second)
	if !labelled(t, h.tt) {
		t.Fatalf("setup: at 1320 points Send now has no label")
	}
	label := sendNows(t, h.tt)[0].Bounds.W
	want := natural(long)
	both := map[bool]bool{}
	for _, sidebar := range []bool{true, false} {
		if h.a.showSidebar != sidebar {
			h.a.toggleSidebar(false)
			h.tt.Advance(time.Second)
		}
		for w := 760; w <= 1320; w += 7 {
			h.tt.SetSize(w, 860)
			r, _ := h.tt.Find(long)
			on := labelled(t, h.tt)
			both[on] = true
			icon := sendNows(t, h.tt)[0].Bounds.W
			switch {
			case on && r.W < want-0.5:
				t.Errorf("sidebar %t, %d points: %q has %.1f of %.1f points beside a labelled Send now", sidebar, w, long, r.W, want)
			case !on && r.W-(label-icon) >= want+0.5:
				t.Errorf("sidebar %t, %d points: Send now is an icon though %q has %.1f points, %.1f with the label", sidebar, w, long, r.W, r.W-(label-icon))
			}
		}
	}
	if !both[true] || !both[false] {
		t.Errorf("Send now took one form only from 760 to 1320 points: %v", both)
	}
}

// A queued message's row is drawn from its first frame as it stays: Send
// now doesn't change its form once the row is laid out, nor as the
// window narrows past the message's room.
func TestAQueuedRowIsDrawnAsItStaysFromItsFirstFrame(t *testing.T) {
	h, queue := heldQueue(t, 0)
	h.tt.Advance(time.Second)
	h.tt.Frame()
	queue("A short message fits beside its label.", 0, api.Defer)
	var first float32
	for range 60 {
		h.tt.Advance(time.Second / 60)
		if n, ok := h.tt.Access("Send now"); ok && n.Bounds.W > 0 {
			first = n.Bounds.W
			break
		}
	}
	h.tt.Advance(time.Second)
	if settled := sendNows(t, h.tt)[0].Bounds.W; settled != first {
		t.Errorf("Send now is drawn %.1f wide in its row's first frame, then %.1f", first, settled)
	}
	if !labelled(t, h.tt) {
		t.Errorf("a short message at 1320 points leaves Send now no room for its label")
	}
	long := strings.Repeat("word ", 10) + "last"
	queue(long, 1, api.Defer)
	h.tt.Advance(time.Second)
	h.tt.SetSize(1040, 860)
	h.tt.Advance(time.Second)
	h.tt.SetSize(760, 860)
	shownWhole(t, h.tt, "at once narrowed to 760 points", long)
	if labelled(t, h.tt) {
		t.Errorf("at 760 points Send now keeps its label beside %q", long)
	}
}

// A row being edited shows no message and no Send now: it has no say in
// whether the other rows' Send nows keep their label.
func TestARowBeingEditedLeavesSendNowItsLabel(t *testing.T) {
	h, queue := heldQueue(t, 0)
	queue(strings.Repeat("word ", 10)+"last", 0, api.Defer)
	queue("Sibling", 1, api.Defer)
	h.tt.SetSize(760, 860)
	h.tt.Advance(time.Second)
	if labelled(t, h.tt) {
		t.Fatalf("setup: at 760 points the long message leaves Send now its label")
	}
	h.click("Edit")
	h.until("the edit", func() bool { return h.tt.HasText("Save") })
	h.tt.Advance(time.Second)
	if !labelled(t, h.tt) {
		t.Errorf("with the long message being edited, the sibling's Send now is an icon")
	}
}

// Drawn as its icon, an action is as tall as Send now with its label, so
// the queue's rows keep the pitch a row with a labelled Send now has,
// whichever form the window gives it.
func TestTheQueuesRowsKeepTheirPitchAcrossSendNowsForms(t *testing.T) {
	h, queue := heldQueue(t, 0)
	messages := []string{strings.Repeat("word ", 10) + "last", "Sibling A", "Sibling B"}
	for i, m := range messages {
		queue(m, int64(i), api.Defer)
	}
	h.tt.SetSize(1320, 860)
	h.tt.Advance(time.Second)
	if !labelled(t, h.tt) {
		t.Fatalf("setup: at 1320 points Send now has no label")
	}
	button := sendNows(t, h.tt)[0].Bounds.H
	if edit, _ := h.tt.Access("Edit"); edit.Bounds.H != button {
		t.Errorf("Edit is %.3f points tall, Send now with its label %.3f", edit.Bounds.H, button)
	}
	// A row: the labelled action in the row's padding, then the gap to
	// the next.
	want := button + 2*2 + 2
	forms := map[bool]bool{}
	for w := 760; w <= 1320; w += 20 {
		h.tt.SetSize(w, 860)
		first, _ := h.tt.Find(oneLine(messages[0]))
		second, _ := h.tt.Find(messages[1])
		on := labelled(t, h.tt)
		forms[on] = true
		if pitch := second.Y - first.Y; pitch < want-0.01 || pitch > want+0.01 {
			t.Errorf("at %d points, Send now labelled %t, rows are %.3f apart; want %.3f", w, on, pitch, want)
		}
	}
	if len(forms) != 2 {
		t.Fatalf("Send now drawn in one form only from 760 to 1320 points: %v", forms)
	}
}

// Queued messages that come and go leave the queue's bindings behind in
// the window no more than main does: one a message, which isn't the
// queue's.
func TestTheQueueKeepsNoBindingsForMessagesGone(t *testing.T) {
	h, queue := heldQueue(t, 0)
	var refs int
	tt := ui.NewTester(func(c *ui.Context) {
		h.a.view(c)
		refs = reflect.ValueOf(c).Elem().FieldByName("rt").Elem().FieldByName("refs").Len()
	}, 1320, 860)
	tr := h.a.current().Transcript
	var counts []int
	for i := range 12 {
		queue(fmt.Sprintf("Short queued row %d", i), 0, api.Defer)
		tt.Frame()
		sendNows(t, tt)
		applyTo(t, tr, tr.Cursor.Log, api.MessageCancelled{Message: waiting(h.a.current())[0].Message})
		h.a.changed()
		tt.Frame()
		counts = append(counts, refs)
	}
	if grew, n := counts[len(counts)-1]-counts[2], len(counts)-3; grew > n {
		t.Errorf("the window's bindings after each queued message came and went: %v; %d more over %d messages, want at most one a message", counts, grew, n)
	}
}

// inkOf is how wide text is drawn alone at the queue's 12 points, with
// nothing to hold it in: the reference a row's drawing is checked by.
func inkOf(t *testing.T, text string) float32 {
	t.Helper()
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Box(c).Padding(20).Children(func() { ui.Text(c, text).FontSize(12).SingleLine() })
	}, 800, 100)
	tt.Frame()
	r, ok := tt.Find(text)
	if !ok {
		t.Fatalf("no %q drawn alone", text)
	}
	return inkWidth(tt.Image(), r)
}
