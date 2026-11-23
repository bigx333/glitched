package main

import (
	"testing"
	"time"
)

// presented resizes the window to w points and runs the frames the app
// asks for over about two seconds, as a window draws them, with no input:
// it says what the first of them showed wrong in the header, if one did. A
// control may be missing from a frame while its plugin draws it for the
// width, never past the window or under another, and shows by the end.
func (h *harness) presented(w int, controls ...string) []string {
	h.t.Helper()
	h.tt.SetSize(w, 860)
	for range 120 {
		var shown []string
		for _, s := range controls {
			if r, ok := h.tt.Find(s); ok && r.Y < headerHeight {
				shown = append(shown, s)
			}
		}
		if p := h.misfits(w, shown...); p != nil {
			return p
		}
		h.p.run()
		h.tt.Advance(16 * time.Millisecond)
		time.Sleep(2 * time.Millisecond)
	}
	return h.misfits(w, controls...)
}

// No frame the window presents lays the header out with a control past
// the window or under another, the frames right after a resize too, and
// the first click on the pull request's pill after one opens it: the
// header gives way in the frame that draws it, not over the frames after.
// NEL-131.
func TestNoFrameAfterAResizeClipsTheHeader(t *testing.T) {
	const title = "Header fixture"
	h := narrowHeader(t, title, false, nil)
	controls := append([]string{title, "Pull request #12"}, headerControls...)
	for _, w := range []int{1320, 760, 1320, 1000, 960, 1050, 760} {
		for _, p := range h.presented(w, controls...) {
			t.Errorf("1320 → … → %d: %s", w, p)
		}
	}

	h.presented(1050)
	h.tt.SetSize(760, 860)
	pr, ok := h.inHeader("Pull request #12")
	if !ok {
		t.Fatalf("right after the resize to 760 points the header has no pull request pill: %q", h.tt.Texts())
	}
	h.tt.ClickAt(pr.X+pr.W/2, pr.Y+pr.H/2)
	h.until("the pull request's popover", func() bool { h.p.run(); h.tt.Advance(16 * time.Millisecond); return h.tt.HasText("Open on GitHub") })
	if h.tt.HasText("Finder") {
		t.Errorf("the first click on the pull request's pill opened Open in: %q", h.tt.Texts())
	}
}
