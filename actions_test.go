package main

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// Pointing at a message shows its actions, which take no room. Copy
// copies it; Edit & resend puts it back in the composer, the thread cut
// to before it; Retry has the agent answer again; Fork here opens a
// thread of the messages so far.
func TestMessageActions(t *testing.T) {
	a := newApp()
	a.selected = "t3"
	tt := ui.NewTester(a.view, 1320, 860)
	_, th := a.current()
	replyCount = 0 // the reply without a code block, which has a Copy of its own
	// Longer than a title, so the sidebar doesn't show the same text.
	const msg = "Fix the flicker when the window is resized on Windows 11, please."
	a.draft = msg
	a.attachments = []attachment{{name: "flicker.png", detail: "PNG"}}
	tt.Frame()
	tt.Advance(time.Second) // the composer grows for the chip
	if err := tt.Click("Send"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(time.Second)
	if tt.HasText("Edit & resend") {
		t.Error("actions show without the pointer")
	}
	r, _ := tt.Find(msg)
	below, _ := tt.Find("Worked for 3s")
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	tt.Advance(time.Second)
	if after, _ := tt.Find("Worked for 3s"); after.Y != below.Y {
		t.Errorf("the work log moved from y %v to %v as the message was pointed at", below.Y, after.Y)
	}
	if err := tt.Click("Copy"); err != nil {
		t.Fatal(err)
	}
	if tt.Clipboard() != msg {
		t.Errorf("clipboard %q", tt.Clipboard())
	}
	if err := tt.Click("Edit & resend"); err != nil {
		t.Fatal(err)
	}
	if a.draft != msg || len(th.entries) != 0 {
		t.Fatalf("draft %q, entries %d", a.draft, len(th.entries))
	}
	if len(a.attachments) != 1 || a.attachments[0].name != "flicker.png" {
		t.Errorf("attachments %v after Edit & resend, want the message's", a.attachments)
	}
	a.attachments = nil

	replyCount = 0
	a.draft = msg
	tt.Frame()
	if err := tt.Click("Send"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(time.Second)
	hoverReply := func() {
		r, ok := tt.Find("Done. The collapsed projects are kept in localStorage under t3code:sidebar:collapsed, as a list of project ids.")
		if !ok {
			t.Fatalf("no reply; texts %q", tt.Texts())
		}
		tt.Move(r.X+r.W/2, r.Y+r.H/2)
		tt.Advance(time.Second)
	}
	hoverReply()
	replyCount = 0
	if err := tt.Click("Retry"); err != nil {
		t.Fatal(err)
	}
	if len(th.entries) != 3 || th.entries[0].kind != entryUser || th.status != statusCompleted {
		t.Fatalf("after retry: %d entries, status %d", len(th.entries), th.status)
	}
	hoverReply()
	if err := tt.Click("Fork here"); err != nil {
		t.Fatal(err)
	}
	_, f := a.current()
	if f == th || len(f.entries) != 3 || f.title != th.title+" (fork)" {
		t.Fatalf("fork: %q with %d entries", f.title, len(f.entries))
	}
}

// A command's step shows how it went, a failed one what it printed, and
// an edit's step opens the file in the diff.
func TestWorkLogSteps(t *testing.T) {
	a := newApp()
	tt := ui.NewTester(a.view, 1320, 860)
	if !tt.HasText("✗ 1") || !tt.HasText("✓ 0") || !tt.HasText("Test Files  1 failed (1)") {
		t.Fatalf("texts %q", tt.Texts())
	}
	if err := tt.Click("Editing"); err != nil {
		t.Fatal(err)
	}
	if !a.showDiff || a.diffFile != 0 {
		t.Errorf("diff %v at file %d after clicking the edit step", a.showDiff, a.diffFile)
	}
}
