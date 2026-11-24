package main

import (
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

// In a plugin's rows, each as wide as it lays itself out, Send now keeps
// its label only while each row's message shows whole, and the queue
// settles on its choice.
func TestTheQueueFitsEachRowOfAPluginsLayout(t *testing.T) {
	h, queue := heldQueue(t, 0)
	long := strings.Repeat("word ", 10) + "first"
	queue(long, 0, api.Defer)
	queue("Second", 1, api.Defer)
	pluginRows(t, h, 2, `export function place({items}) {
		return items.map(({context}) => ({type:"row", grow:true, gap:2, align:"center", children:[
			{type:"native", id:"text", name:"queue.text", grow:true},
			{type:"native", id:"send", name:"queue.steer"},
			{type:"button", id:"extra", label: context.index === 0 ? "Extra action with reserved room and a much wider label" : "Extra"},
			{type:"text", id:"marker", text:"Plugin row"}
		]}))
	}`)
	for _, w := range []int{1320, 1120, 940, 760} {
		h.tt.SetSize(w, 860)
		h.tt.Advance(time.Second)
		if h.tt.FramePending() {
			t.Errorf("at %d points the queue keeps asking for frames", w)
		}
		shownWhole(t, h.tt, "at "+strconv.Itoa(w)+" points", long, "Second")
	}
}

// A plugin's row with two Send nows gains two labels' width: the queue
// settles on its choice at each width, without flipping between them.
func TestTheQueueSettlesWithTwoSendNowsInARow(t *testing.T) {
	h, queue := heldQueue(t, 0)
	text := strings.Repeat("word ", 7) + "last"
	queue(text, 0, api.Defer)
	pluginRows(t, h, 1, `export function place({items}) {
		return items.map(() => ({type:"row", grow:true, gap:2, align:"center", children:[
			{type:"native", id:"text", name:"queue.text", grow:true},
			{type:"native", id:"send1", name:"queue.steer"},
			{type:"native", id:"send2", name:"queue.steer"},
			{type:"text", id:"marker", text:"Plugin row"}
		]}))
	}`)
	for w := 760; w <= 940; w += 10 {
		h.tt.SetSize(w, 860)
		if h.tt.FramePending() {
			t.Fatalf("at %d points the queue still asks for frames after %d", w, 20)
		}
		shownWhole(t, h.tt, "at "+strconv.Itoa(w)+" points", text)
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

// A row is as tall whether Send now has its label or not: the queue's
// rows keep their pitch as the window crosses from one to the other.
func TestTheQueuesRowsKeepTheirPitchAcrossSendNowsForms(t *testing.T) {
	h, queue := heldQueue(t, 0)
	messages := []string{strings.Repeat("word ", 10) + "last", "Sibling A", "Sibling B"}
	for i, m := range messages {
		queue(m, int64(i), api.Defer)
	}
	pitch := map[bool]float32{}
	for w := 760; w <= 1320; w += 20 {
		h.tt.SetSize(w, 860)
		h.tt.Advance(time.Second)
		first, _ := h.tt.Find(oneLine(messages[0]))
		second, _ := h.tt.Find(messages[1])
		pitch[labelled(t, h.tt)] = second.Y - first.Y
	}
	if len(pitch) != 2 {
		t.Fatalf("Send now drawn in one form only from 760 to 1320 points: %v", pitch)
	}
	if pitch[true] != pitch[false] {
		t.Errorf("rows are %.2f apart with Send now labelled, %.2f with its icon", pitch[true], pitch[false])
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
