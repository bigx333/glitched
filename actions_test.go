package main

import (
	"math"
	"testing"
	"time"

	"glitched/internal/client"
)

// Pointing at a message shows its actions, which take no room. Copy
// copies it and Edit & resend puts it back in the composer. A reply whose
// turn completed has nothing to retry.
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
	if !h.tt.HasText("Copy") || h.tt.HasText("Retry") {
		t.Errorf("a completed reply's actions are %q, want Copy and no Retry", h.tt.Texts())
	}
	if len(th.Pending) != 0 {
		t.Errorf("pointing at messages sent %d commands", len(th.Pending))
	}
}

// The pointer reaches a message's actions however slowly it goes down to
// them: under a message of yours, short or long, drawn by the built-in
// rows or by a copy that places transcript.body alone, the toolbar shows
// on every step of the way, and Copy and Edit & resend do what they say
// where the pointer stops.
func TestAMessagesActionsStayOnTheWayDownToThem(t *testing.T) {
	for _, tc := range []struct {
		name, msg string
		own       bool // a copy that places transcript.body alone
	}{
		{"short", "test", false},
		{"long", "Sort them by most recent activity.", false},
		{"in a copy that places the body alone", "Sort them by most recent activity.", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, th := checked(t)
			if tc.own {
				writeOwnPlugin(t, h.a, "my-rows", rowsManifest, rowsModule(`{type:"native", id:"body", name:"transcript.body"}`))
				allow(t, h.a, "my-rows", "ui.replace")
				settlePlugins(t, h.a, h.tt, h.p)
			}
			for _, e := range th.Transcript.Entries {
				if e, ok := e.(*client.UserEntry); ok {
					e.Text = tc.msg
				}
			}
			h.a.changed()
			h.tt.Advance(time.Second)
			for _, label := range []string{"Copy", "Edit & resend"} {
				h.tt.Move(800, 30)
				h.tt.Advance(time.Second)
				h.hover(tc.msg)
				h.tt.Advance(time.Second)
				from, _ := h.tt.Find(tc.msg)
				to, ok := h.tt.Find(label)
				if !ok {
					t.Fatalf("no %s under the message pointed at: %q", label, h.tt.Texts())
				}
				x0, y0 := from.X+from.W/2, from.Y+from.H/2
				x1, y1 := to.X+to.W/2, to.Y+to.H/2
				steps := int(math.Ceil(float64(max(abs32(x1-x0), abs32(y1-y0)))))
				for s := 1; s <= steps; s++ {
					f := float32(s) / float32(steps)
					x, y := x0+(x1-x0)*f, y0+(y1-y0)*f
					h.tt.Move(x, y)
					h.tt.Advance(16 * time.Millisecond)
					if !h.tt.HasText(label) {
						t.Fatalf("the toolbar went away at (%v, %v), on the way down to %s at (%v, %v)", x, y, label, x1, y1)
					}
				}
				h.tt.ClickAt(x1, y1)
				h.tt.Frame()
			}
			if h.tt.Clipboard() != tc.msg {
				t.Errorf("Copy copied %q, want %q", h.tt.Clipboard(), tc.msg)
			}
			if h.a.draft != tc.msg {
				t.Errorf("draft %q after Edit & resend, want %q", h.a.draft, tc.msg)
			}
		})
	}
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
