package main

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// An approval is answered from its card or the keyboard; allowed, the
// agent goes on, denied, it replies and stops. With a draft, ⌘⏎ sends.
func TestApproval(t *testing.T) {
	a := newApp()
	a.selected = "t2"
	tt := ui.NewTester(a.view, 1320, 860)
	_, th := a.current()
	ap := th.pending()
	if ap == nil || !tt.HasText("Approval needed") || !tt.HasText("Paused for approval") {
		t.Fatalf("no pending approval shown; texts %q", tt.Texts())
	}
	if err := tt.Click("Allow once"); err != nil {
		t.Fatal(err)
	}
	if ap.decision != allowed || th.status != statusCompleted || !tt.HasText("Allowed") {
		t.Fatalf("decision %d, status %d", ap.decision, th.status)
	}
	if last := th.entries[len(th.entries)-1]; last.kind != entryAssistant || last.text != approved.reply {
		t.Errorf("the agent didn't go on: last entry %+v", last)
	}

	a = newApp()
	a.selected = "t2"
	tt = ui.NewTester(a.view, 1320, 860)
	_, th = a.current()
	ap = th.pending()
	a.draft = "use the other runner"
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyEnter)
	if ap.decision != superseded {
		t.Fatalf("⌘⏎ with a draft decided %d, want it to send, which answers the approval", ap.decision)
	}
	if th.entries[len(th.entries)-1].text != scripts[(replyCount-1)%len(scripts)].reply {
		t.Errorf("⌘⏎ with a draft didn't send")
	}

	a = newApp()
	a.selected = "t2"
	tt = ui.NewTester(a.view, 1320, 860)
	_, th = a.current()
	ap = th.pending()
	tt.Key(ui.Cmd|ui.Shift, ui.KeyD)
	if ap.decision != denied || !tt.HasText("Denied") || th.status != statusCompleted {
		t.Fatalf("⇧⌘D decided %d, status %d", ap.decision, th.status)
	}
	if last := th.entries[len(th.entries)-1]; last.text != refused {
		t.Errorf("no reply to the denial: %+v", last)
	}
}

// A message sent while an approval waits answers it: the card says so,
// its controls go, and the agent's turn runs. Editing the message before
// the approval away leaves the thread idle. The approval thread's edit
// counts as a changed file.
func TestApprovalSteer(t *testing.T) {
	a := newApp()
	a.selected = "t2"
	tt := ui.NewTester(a.view, 1320, 860)
	_, th := a.current()
	ap := th.pending()
	if n := len(th.changed()); n != 1 {
		t.Errorf("%d changed files, want the edit of resize.ts", n)
	}
	a.draft = "use the other runner"
	tt.Frame()
	if err := tt.Click("Send"); err != nil {
		t.Fatal(err)
	}
	if ap.decision != superseded || th.pending() != nil || th.status != statusCompleted {
		t.Fatalf("decision %d, pending %v, status %d", ap.decision, th.pending() != nil, th.status)
	}
	if tt.HasText("Allow once") || tt.HasText("Paused for approval") || !tt.HasText("Answered by your message") {
		t.Errorf("the card still asks; texts %q", tt.Texts())
	}

	a = newApp()
	a.selected = "t2"
	tt = ui.NewTester(a.view, 1320, 860)
	_, th = a.current()
	r, ok := tt.Find(th.entries[0].text)
	if !ok {
		t.Fatal("no message to point at")
	}
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	tt.Advance(time.Second)
	if err := tt.Click("Edit & resend"); err != nil {
		t.Fatal(err)
	}
	if th.status != statusIdle || th.pending() != nil || len(th.entries) != 0 {
		t.Errorf("status %d, %d entries after editing the message away", th.status, len(th.entries))
	}
}
