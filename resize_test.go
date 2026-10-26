package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/jsonfile"
)

// dragEdgeBy drags the preview's edge dx DIPs.
func dragEdgeBy(t *testing.T, tt *ui.Tester, dx float32) {
	t.Helper()
	r, ok := tt.Find("Resize preview")
	if !ok {
		t.Fatal("no edge to drag")
	}
	x, y := r.X+r.W/2, r.Y+r.H/2
	tt.Press(x, y)
	tt.Move(x+dx/2, y)
	tt.Move(x+dx, y)
	tt.Release(x+dx, y)
	tt.Frame()
}

func near(a, b float32) bool { return math.Abs(float64(a-b)) <= 1 }

// The preview's edge drags it wider and narrower, the chat taking the
// rest, within limits: the chat keeps its least width, and the preview
// its own, but where the window has no room for both.
func TestPreviewEdgeClamps(t *testing.T) {
	a, tt, _ := browsing(t)
	click(t, tt, "Toggle preview  ⇧⌘P")
	tt.Frame()
	room := a.roomBeside
	start := a.previewWidth()
	if !near(start, panelWidth(room, 0.45, 400, 640)) {
		t.Fatalf("preview %v wide before a drag, want its own %v", start, panelWidth(room, 0.45, 400, 640))
	}
	fits := func(when string) {
		t.Helper()
		if !near(a.chatWidth+a.previewWidth(), a.roomBeside) {
			t.Errorf("%s: chat %v and preview %v in %v", when, a.chatWidth, a.previewWidth(), a.roomBeside)
		}
	}
	dragEdgeBy(t, tt, -100)
	if !near(a.previewWidth(), start+100) {
		t.Errorf("preview %v wide after dragging its edge 100 left, want %v", a.previewWidth(), start+100)
	}
	fits("100 wider")
	dragEdgeBy(t, tt, -2000)
	if !near(a.previewWidth(), room-minChatWidth) {
		t.Errorf("preview %v wide dragged far left, want the room less the chat's %v", a.previewWidth(), room-minChatWidth)
	}
	fits("widest")
	dragEdgeBy(t, tt, 2000)
	if !near(a.previewWidth(), minPreviewWidth) {
		t.Errorf("preview %v wide dragged far right, want %v", a.previewWidth(), minPreviewWidth)
	}
	fits("narrowest")
	// A window too narrow for both: the preview gives way, the chat keeps
	// its width, and the width dragged comes back with the room.
	tt.SetSize(1000, 860)
	tt.Frame()
	if lo, hi := previewSpan(a.roomBeside); !near(a.previewWidth(), hi) || lo != hi {
		t.Errorf("preview %v wide in %v of room, want %v", a.previewWidth(), a.roomBeside, hi)
	}
	fits("narrow window")
	tt.SetSize(1320, 860)
	tt.Frame()
	if !near(a.previewWidth(), minPreviewWidth) {
		t.Errorf("preview %v wide with the room back, want %v", a.previewWidth(), minPreviewWidth)
	}
}

// Where a drag ends the width stays, across launches; a double click on
// the edge gives the preview back its own.
func TestPreviewWidthPersists(t *testing.T) {
	a, tt, _ := browsing(t)
	click(t, tt, "Toggle preview  ⇧⌘P")
	tt.Frame()
	dragEdgeBy(t, tt, -120)
	want := a.previewDragged
	var saved layout
	if err := jsonfile.Load(filepath.Join(a.dir, "layout.json"), &saved); err != nil || want == 0 || saved.PreviewWidth != want {
		t.Fatalf("layout.json holds %v (%v), want %v", saved.PreviewWidth, err, want)
	}
	again := newApp(a.dir, func(func()) {})
	if again.previewDragged != want {
		t.Errorf("relaunched with the preview %v wide, want %v", again.previewDragged, want)
	}
	r, _ := tt.Find("Resize preview")
	tt.ClickAt(r.X+r.W/2, r.Y+r.H/2)
	tt.ClickAt(r.X+r.W/2, r.Y+r.H/2)
	tt.Frame()
	if a.previewDragged != 0 || !near(a.previewWidth(), panelWidth(a.roomBeside, 0.45, 400, 640)) {
		t.Errorf("preview %v wide (dragged %v) after a double click on its edge", a.previewWidth(), a.previewDragged)
	}
	b, _ := os.ReadFile(filepath.Join(a.dir, "layout.json"))
	if string(b) != "{}" && string(b) != "{}\n" {
		t.Errorf("layout.json holds %s after the reset", b)
	}
}

// The chat keeps its column beside the preview: the transcript's edges
// stand against the composer as they do in a window as narrow as the
// chat, without the preview, at any width the edge drags to, with and
// without the sidebar. Where the chat's column shares the composer's
// edges (PR #2), they stay shared.
func TestChatColumnBesidePreview(t *testing.T) {
	h, _ := long(t)
	a, tt := h.a, h.tt
	offsets := func(window float32) (left, right float32) {
		t.Helper()
		tt.Advance(time.Second)
		reply, ok1 := tt.Find("Looking at the code.")
		bubble, ok2 := tt.Find("Persist the collapsed state.")
		model, ok3 := tt.Find("Model")
		send, ok4 := tt.Find("Send")
		if !ok1 || !ok2 || !ok3 || !ok4 {
			t.Fatalf("missing a landmark: %q", tt.Texts())
		}
		if pane := window - a.previewWidth(); a.showPreview && send.X+send.W > pane {
			t.Errorf("the composer's send button ends at %v, under the preview from %v", send.X+send.W, pane)
		}
		return reply.X - (model.X - 8), bubble.X + bubble.W - (send.X + send.W + 8)
	}
	for _, sidebar := range []bool{true, false} {
		if a.showSidebar != sidebar {
			a.toggleSidebar(true)
			tt.Advance(time.Second)
		}
		for _, w := range []float32{minPreviewWidth, 480, 600, 0} {
			tt.SetSize(1320, 860)
			a.showPreview, a.previewDragged = true, w
			gotL, gotR := offsets(1320)
			pane := a.previewWidth()
			// The same chat, in a window that much narrower.
			a.showPreview = false
			tt.SetSize(int(1320-pane+0.5), 860)
			wantL, wantR := offsets(1320 - pane)
			name := fmt.Sprintf("sidebar %v, preview %v wide", sidebar, pane)
			t.Logf("%s: column offsets %.1f, %.1f; without it %.1f, %.1f", name, gotL, gotR, wantL, wantR)
			if !near(gotL, wantL) || !near(gotR, wantR) {
				t.Errorf("%s: the transcript's edges moved against the composer: %.1f, %.1f, want %.1f, %.1f", name, gotL, gotR, wantL, wantR)
			}
		}
	}
	tt.SetSize(1320, 860)
}
