package main

import (
	"testing"
	"time"
)

// Pointing at a message shows its actions, which take no room. Copy
// copies it, Edit & resend puts it back in the composer, and Retry on a
// reply sends the message it answered again.
func TestMessageActions(t *testing.T) {
	h, th := checked(t)
	const msg = "Sort them by most recent activity."
	if h.tt.HasText("Edit & resend") {
		t.Error("actions show without the pointer")
	}
	below, _ := h.tt.Find("Checking the build.")
	h.hover(msg)
	h.tt.Advance(time.Second)
	if after, _ := h.tt.Find("Checking the build."); after.Y != below.Y {
		t.Errorf("the reply moved from y %v to %v as the message was pointed at", below.Y, after.Y)
	}
	h.click("Copy")
	if h.tt.Clipboard() != msg {
		t.Errorf("clipboard %q", h.tt.Clipboard())
	}
	h.click("Edit & resend")
	if h.a.draft != msg {
		t.Fatalf("draft %q after Edit & resend", h.a.draft)
	}
	h.a.draft = ""
	h.tt.Frame()

	h.hover("One test fails.")
	h.tt.Advance(time.Second)
	h.click("Retry")
	h.until("the message sent again", func() bool {
		n := 0
		for _, e := range entriesOf(th) {
			if e.kind == entryUser && e.text == msg {
				n++
			}
		}
		return n == 2
	})
}

// A command's step says how it went and how long it took, a failed one
// what it printed, and an edit's step opens its file in the diff.
func TestWorkLogSteps(t *testing.T) {
	h, th := checked(t)
	for _, s := range []string{"✓ 0", "✗ 1", " FAIL  Sidebar > sorts threads by updatedAt\n Test Files  1 failed (1)", "3 steps"} {
		if !h.tt.HasText(s) {
			t.Errorf("no %q in the work log; texts %q", s, h.tt.Texts())
		}
	}
	h.click("Edit apps/web/src/components/Sidebar.tsx")
	if !h.a.showDiff || h.a.diffFile[th.id()] != sidebarPath {
		t.Errorf("diff %v at file %q after clicking the edit step", h.a.showDiff, h.a.diffFile[th.id()])
	}
	h.tt.Advance(time.Second)
	if !h.tt.HasText("apps/web/src/components/Sidebar.tsx") {
		t.Errorf("the diff doesn't list the edited file; texts %q", h.tt.Texts())
	}
}
