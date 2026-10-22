package main

import (
	"sync"
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

// Stop ends the turn where it is: the work log says so, a half-streamed
// reply stands and gets its actions, and the thread is the user's again.
func TestStopEndsTurn(t *testing.T) {
	a := newApp()
	tt := ui.NewTester(a.view, 1320, 860)
	_, th := a.current() // t1, working
	th.entries = append(th.entries, entry{kind: entryAssistant, text: "Persisting the state now.", streaming: true})
	tt.Frame()
	if err := tt.Click("Send"); err != nil { // the stop button, while working
		t.Fatal(err)
	}
	last := th.entries[len(th.entries)-1]
	if th.status != statusIdle || last.streaming || th.entries[len(th.entries)-2].took != "Stopped" {
		t.Errorf("status %d, streaming %v, work log %q", th.status, last.streaming, th.entries[len(th.entries)-2].took)
	}
	if !tt.HasText("Stopped") {
		t.Error("the work log doesn't say it was stopped")
	}
}

// A turn stopped, then the thread cut by Edit & resend, writes nothing
// more: the turn's goroutine sees it is no longer live and its queued
// changes are dropped. Left alone, a turn runs to its end.
func TestStoppedTurnWritesNothing(t *testing.T) {
	a := newApp()
	a.selected = "t3"
	tt := ui.NewTester(a.view, 1320, 860)
	_, th := a.current()
	ticks := make(chan struct{})
	var mu sync.Mutex
	var queued []func()
	a.turns = &turnRunner{
		sleep:  func(time.Duration) { <-ticks },
		update: func(fn func()) { mu.Lock(); queued = append(queued, fn); mu.Unlock() },
	}
	flush := func() {
		mu.Lock()
		fns := queued
		queued = nil
		mu.Unlock()
		for _, fn := range fns {
			fn()
		}
		tt.Frame()
	}
	a.draft = "Fix the flicker when the window is resized on Windows 11, please."
	tt.Frame()
	if err := tt.Click("Send"); err != nil {
		t.Fatal(err)
	}
	if th.status != statusWorking || len(th.entries) != 2 {
		t.Fatalf("status %d, %d entries after send", th.status, len(th.entries))
	}
	if err := tt.Click("Send"); err != nil { // Stop
		t.Fatal(err)
	}
	r, ok := tt.Find("Fix the flicker when the window is resized on Windows 11, please.")
	if !ok {
		t.Fatal("no message to point at")
	}
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	tt.Advance(time.Second)
	if err := tt.Click("Edit & resend"); err != nil {
		t.Fatal(err)
	}
	if len(th.entries) != 0 {
		t.Fatalf("%d entries after Edit & resend", len(th.entries))
	}
	ticks <- struct{}{} // the stopped turn wakes, and goes
	time.Sleep(50 * time.Millisecond)
	flush()
	if len(th.entries) != 0 || th.status != statusIdle {
		t.Errorf("the stopped turn wrote: %d entries, status %d", len(th.entries), th.status)
	}

	// Sent again, the turn runs to its end.
	tt.Frame()
	if err := tt.Click("Send"); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		for {
			select {
			case ticks <- struct{}{}:
			case <-done:
				return
			}
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for th.status != statusCompleted && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		flush()
	}
	close(done)
	if th.status != statusCompleted || len(th.entries) != 3 || th.entries[2].streaming {
		t.Errorf("status %d, %d entries, streaming %v after the turn", th.status, len(th.entries), len(th.entries) > 2 && th.entries[2].streaming)
	}
}
