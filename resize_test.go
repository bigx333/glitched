package main

import (
	"fmt"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/browse"
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
	again := testApp(t, a.dir, nil, func(func()) {})
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

// While the edge is held and moves, every frame lays both sides out
// anew: the preview at the pointer, the chat in the rest, its column
// against the composer as it stands at rest. GLITCHED_STRIP_OUT names a
// directory for the frames, as PNGs, the page a box where the web view
// would show. The composer follows the edge as it moves: it doesn't ease
// into the width it's given, under the preview meanwhile.
func TestPreviewDragsLive(t *testing.T) {
	h, _ := long(t)
	a, tt := h.a, h.tt
	a.browser.open = func(url string, ev browse.Events) (browse.Page, error) { return &paintedPage{}, nil }
	a.showPreview = true
	tt.Advance(time.Second)
	column := func() (left, right float32) {
		reply, _ := tt.Find("Looking at the code.")
		bubble, _ := tt.Find("Persist the collapsed state.")
		model, _ := tt.Find("Model")
		send, ok := tt.Find("Send")
		if !ok {
			t.Fatal("no send button")
		}
		if pane := 1320 - a.previewWidth(); send.X+send.W+8 > pane+1 {
			t.Errorf("the composer ends at %v, under the preview from %v", send.X+send.W+8, pane)
		}
		return reply.X - (model.X - 8), bubble.X + bubble.W - (send.X + send.W + 8)
	}
	restL, restR := column()
	out := os.Getenv("GLITCHED_STRIP_OUT")
	if out != "" {
		os.MkdirAll(out, 0o755)
	}
	r, ok := tt.Find("Resize preview")
	if !ok {
		t.Fatal("no edge")
	}
	x, y := r.X+r.W/2, r.Y+r.H/2
	tt.Press(x, y)
	const steps = 12
	for i := 1; i <= steps; i++ {
		to := x - float32(i)*12
		tt.Move(to, y)
		tt.Advance(16 * time.Millisecond) // a frame's time, as the window's
		tt.Frame()                        // the frame the chat's new width asks for
		edge, _ := tt.Find("Resize preview")
		if !near(edge.X+edge.W/2, to) {
			t.Errorf("step %d: the edge is at %v, the pointer at %v", i, edge.X+edge.W/2, to)
		}
		if !near(a.chatWidth+a.previewWidth(), a.roomBeside) {
			t.Errorf("step %d: chat %v and preview %v in %v", i, a.chatWidth, a.previewWidth(), a.roomBeside)
		}
		if l, r := column(); !near(l, restL) || !near(r, restR) {
			t.Errorf("step %d: the column stands %v, %v from the composer, %v, %v at rest", i, l, r, restL, restR)
		}
		if out != "" {
			f, _ := os.Create(filepath.Join(out, fmt.Sprintf("frame-%02d.png", i)))
			png.Encode(f, tt.Image())
			f.Close()
		}
	}
	tt.Release(x-steps*12, y)
	if !near(a.previewDragged, panelWidth(1320-272, 0.45, 400, 640)+steps*12) {
		t.Errorf("preview %v wide after the drag", a.previewDragged)
	}
}

// paintedPage is a page that paints a box where the web view would show.
type paintedPage struct{ fakePage }

func (p *paintedPage) Show(c *ui.Context) ui.Element {
	return ui.Box(c).Background(ui.Hex("#0a7ea4")).Children(func() {
		ui.Text(c, "page").Margin(ui.Auto).TextColor(ui.Hex("#ffffff"))
	})
}

// The preview's edge stands wholly over the chat, beside the page, whose
// web view would take the pointer over its own bounds.
func TestPreviewEdgeBesideThePage(t *testing.T) {
	_, tt, _ := browsing(t)
	click(t, tt, "Toggle preview  ⇧⌘P")
	tt.Frame()
	edge, ok1 := tt.Find("Resize preview")
	page, ok2 := tt.Find("Page")
	if !ok1 || !ok2 {
		t.Fatal("no edge or no page")
	}
	// The pane starts at its border, left of the page.
	if pane := page.X - 1; edge.X+edge.W > pane+0.5 {
		t.Errorf("the edge spans %v to %v, over the pane from %v", edge.X, edge.X+edge.W, pane)
	}
}

// An edge holds the chat only while it's pressed: closing its pane mid-drag
// (⇧⌘P), which ends no drag, lets it go.
func TestPreviewEdgeLetGoAsThePaneCloses(t *testing.T) {
	a, tt, _ := browsing(t)
	click(t, tt, "Toggle preview  ⇧⌘P")
	tt.Frame()
	r, _ := tt.Find("Resize preview")
	x, y := r.X+r.W/2, r.Y+r.H/2
	tt.Press(x, y)
	tt.Move(x-30, y)
	tt.Frame()
	if !a.previewHeld {
		t.Fatal("the edge dragged isn't held")
	}
	tt.Key(ui.Cmd|ui.Shift, ui.KeyP)
	tt.Frame()
	tt.Frame()
	if a.showPreview || a.previewHeld {
		t.Errorf("pane %v, held %v after it closed mid-drag", a.showPreview, a.previewHeld)
	}
	tt.Release(x-30, y)
	// The sidebar's edge, dragged and let go, holds nothing either.
	s, _ := tt.Find("Resize sidebar")
	x, y = s.X+s.W/2, s.Y+s.H/2
	tt.Press(x, y)
	tt.Move(x+20, y)
	tt.Frame()
	if !a.sidebarHeld {
		t.Fatal("the sidebar's edge dragged isn't held")
	}
	tt.Release(x+20, y)
	tt.Frame()
	if a.sidebarHeld {
		t.Error("the sidebar's edge let go is still held")
	}
}

// The width dragged to stays, across launches, when the pane closes
// mid-drag (⇧⌘P) or the diff covers it (⌘D): its edge goes, ending the
// drag without the release that would save it.
func TestPreviewWidthPersistsAsThePaneClosesMidDrag(t *testing.T) {
	for _, close := range []struct {
		name string
		key  ui.Key
		mods ui.Modifiers
	}{{"⇧⌘P", ui.KeyP, ui.Cmd | ui.Shift}, {"⌘D", ui.KeyD, ui.Cmd}} {
		t.Run(close.name, func(t *testing.T) {
			a, tt, _ := browsing(t)
			click(t, tt, "Toggle preview  ⇧⌘P")
			tt.Frame()
			r, _ := tt.Find("Resize preview")
			x, y := r.X+r.W/2, r.Y+r.H/2
			tt.Press(x, y)
			tt.Move(x-40, y)
			tt.Frame()
			want := a.previewDragged
			if want == 0 {
				t.Fatal("the edge dragged moved nothing")
			}
			tt.Key(close.mods, close.key)
			tt.Frame()
			if _, ok := tt.Find("Resize preview"); ok {
				t.Fatalf("the edge stays after %s", close.name)
			}
			tt.Release(x-40, y)
			tt.Frame()
			if again := testApp(t, a.dir, nil, func(func()) {}); again.previewDragged != want {
				t.Errorf("relaunched with the preview dragged to %v, want %v", again.previewDragged, want)
			}
		})
	}
}

// A click on the edge, which moves nothing, keeps the layout as it was,
// unwritten: one wobbling along the edge too, which MyGo ends as a drag.
func TestPreviewEdgeClickKeepsTheLayout(t *testing.T) {
	a, tt, _ := browsing(t)
	click(t, tt, "Toggle preview  ⇧⌘P")
	tt.Frame()
	r, _ := tt.Find("Resize preview")
	x, y := r.X+r.W/2, r.Y+r.H/2
	tt.Press(x, y)
	tt.Move(x, y+3)
	tt.Release(x, y+3)
	tt.Frame()
	if _, err := os.Stat(filepath.Join(a.dir, "layout.json")); !os.IsNotExist(err) {
		t.Errorf("a click on the edge wrote layout.json (%v)", err)
	}
	if a.previewHeld || a.previewDragged != 0 {
		t.Errorf("held %v, dragged %v after a click", a.previewHeld, a.previewDragged)
	}
}

// A layout.json holding a width no drag gives, as one edited by hand, is
// none: the preview has its own width.
func TestLayoutRefusesWidthsThatArent(t *testing.T) {
	for _, held := range []string{"-50", "1e39", "-1e39", `"wide"`, "480"} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "layout.json"), []byte(`{"previewWidth": `+held+"}"), 0o600); err != nil {
			t.Fatal(err)
		}
		a := testApp(t, dir, nil, func(func()) {})
		want := float32(0)
		if held == "480" {
			want = 480
		}
		if a.previewDragged != want {
			t.Errorf("layout.json holding %s: preview dragged to %v, want %v", held, a.previewDragged, want)
		}
	}
}
