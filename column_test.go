package main

import (
	"fmt"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// The transcript's column and the composer share their edges: the reply's
// text starts where the composer's controls do, less their padding, and
// the message's bubble ends where the composer card does, at any width,
// with and without the sidebar, scrolled to the end or folded compact.
func TestTranscriptAndComposerShareEdges(t *testing.T) {
	a, tt, th := longThread(t, nil)
	edges := func(name string) {
		t.Helper()
		tt.Advance(time.Second)
		reply, ok1 := tt.Find("Looking at the code.")
		bubbleText, ok2 := tt.Find("Step 8: look at main.go again.")
		model, ok3 := tt.Find("Model")
		send, ok4 := tt.Find("Send")
		if !ok1 || !ok2 || !ok3 || !ok4 {
			t.Fatalf("%s: missing a landmark: %q", name, tt.Texts())
		}
		colL, colR := reply.X, bubbleText.X+bubbleText.W+bubbleInset
		cardL, cardR := model.X-8, send.X+send.W+8
		t.Logf("%s: column %.1f→%.1f, composer %.1f→%.1f", name, colL, colR, cardL, cardR)
		if d := colL - cardL; d < -1 || d > 1 {
			t.Errorf("%s: the column's left edge is %.1f DIPs from the composer's", name, d)
		}
		if d := colR - cardR; d < -1 || d > 1 {
			t.Errorf("%s: the column's right edge is %.1f DIPs from the composer's", name, d)
		}
	}
	for _, w := range []int{1320, 1000, 820} {
		tt.SetSize(w, 860)
		edges(fmt.Sprintf("%d wide", w))
	}
	tt.SetSize(1320, 860)
	a.toggleSidebar(false)
	edges("no sidebar")
	a.toggleSidebar(false)
	// Scrolled up with a draft, the composer stays full over a scrolled
	// transcript.
	a.draft = "x"
	a.listFor(th.id()).ScrollTo(0, ui.Start)
	tt.Frame()
	tt.Advance(time.Second)
	a.listFor(th.id()).ScrollToEnd()
	edges("back at the end")
	a.draft = ""
}
