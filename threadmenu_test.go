package main

import (
	"os/exec"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/daemon"
	"glitched/internal/provider/fake"
)

// A thread row's context menu renames the thread in place, snoozes it,
// and deletes it, asking first while the setting says to. It offers no
// archive: a thread done with is settled.
func TestThreadMenu(t *testing.T) {
	h := start(t, inboxDir(t))
	const loop = "Investigate websocket reconnect loop"
	th := h.thread(loop)
	h.tt.Advance(time.Second)
	if err := h.tt.RightClick(loop); err != nil {
		t.Fatal(err)
	}
	if m := h.tt.Menu(); len(m) == 0 || m[0] != "Rename" {
		t.Fatalf("menu %q", m)
	}
	if err := h.tt.ChooseMenuItem("Rename"); err != nil {
		t.Fatal(err)
	}
	h.tt.Frame()
	h.tt.Key(ui.Cmd, ui.KeyA)
	h.tt.Type("Websocket loop")
	h.tt.Key(0, ui.KeyEnter)
	h.until("the rename", func() bool { return th.View.Title == "Websocket loop" && len(th.Pending) == 0 })
	h.tt.Advance(time.Second)

	if err := h.tt.RightClick("Websocket loop"); err != nil {
		t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem("Snooze", "Tomorrow"); err != nil {
		t.Fatal(err)
	}
	h.until("the snooze", func() bool { return th.View.Disposition.State == api.Snoozed })
	// The toast naming the thread goes; Snoozed is closed, and its thread
	// shows once it's opened.
	h.tt.Advance(10 * time.Second)
	h.click("Snoozed")
	h.tt.Advance(time.Second)
	if err := h.tt.RightClick("Websocket loop"); err != nil {
		t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem("Archive"); err == nil {
		t.Fatal("the menu offers Archive")
	}
	h.tt.CloseMenu()
	h.tt.Advance(time.Second)

	// Deleting asks; deleting the thread shown shows a new one instead.
	h.a.select_(th)
	h.tt.Advance(time.Second)
	if err := h.tt.RightClick("Websocket loop"); err != nil {
		t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem("Delete…"); err != nil {
		t.Fatal(err)
	}
	h.tt.Frame()
	if !h.tt.HasText("Delete “Websocket loop”?") || !h.tt.HasText("The thread is removed from every list and can't be brought back. Its history, messages included, stays in the engine's log.") {
		t.Fatalf("no alert; texts %q", h.tt.Texts())
	}
	h.click("Delete")
	h.until("the delete", func() bool { _, ok := h.a.find(th.id()); return !ok })
	if h.a.current().id() == th.id() {
		t.Error("the deleted thread is still shown")
	}

	h.a.settings.confirmDelete = false
	const sip = "SIP re-INVITE handling"
	other := h.thread(sip)
	h.tt.Advance(time.Second)
	if err := h.tt.RightClick(sip); err != nil {
		t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem("Delete…"); err != nil {
		t.Fatal(err)
	}
	h.tt.Frame()
	if h.tt.HasText("Delete “" + sip + "”?") {
		t.Error("deleting without the setting asked")
	}
	h.until("the second delete", func() bool { _, ok := h.a.find(other.id()); return !ok })
}

// Copy branch name copies the branch the workspace has checked out, and
// Open in editor opens the workspace in the editor found.
func TestThreadMenuWorkspace(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	ws := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", "-b", "t3code/sidebar-sort", ws).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	var opened string
	was, starts := editorCLI, startEditor
	editorCLI, startEditor = func() string { return "cursor" }, func(ed, path string) error { opened = path; return nil }
	t.Cleanup(func() { editorCLI, startEditor = was, starts })

	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(false)))
	seed(t, d, ws, "Sort sidebar threads by recent activity")
	h := start(t, dir)
	h.thread("Sort sidebar threads by recent activity")
	h.tt.Advance(time.Second)
	if err := h.tt.RightClick("Sort sidebar threads by recent activity"); err != nil {
		t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem("Copy branch name"); err != nil {
		t.Fatal(err)
	}
	if h.tt.Clipboard() != "t3code/sidebar-sort" {
		t.Errorf("clipboard %q", h.tt.Clipboard())
	}
	if err := h.tt.RightClick("Sort sidebar threads by recent activity"); err != nil {
		t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem("Open in editor"); err != nil {
		t.Fatal(err)
	}
	if opened != ws {
		t.Errorf("opened %q, want the workspace %q", opened, ws)
	}
}

// A thread on a server offers nothing that needs its workspace here.
func TestThreadMenuOnAServer(t *testing.T) {
	h := machines(t)
	if err := h.tt.RightClick("Rotate the API keys"); err != nil {
		t.Fatal(err)
	}
	for _, item := range h.tt.Menu() {
		if item == "Copy branch name" || item == "Open in editor" {
			t.Errorf("a server's thread offers %q", item)
		}
	}
}

// A run left uncertain keeps the thread from being deleted until the user
// ends it, as a working one does, and Stop ends it.
func TestAnUncertainRunIsStoppedBeforeTheThreadGoes(t *testing.T) {
	h := start(t, uncertainDir(t))
	th := h.thread("Stuck")
	h.tt.Advance(time.Second)
	if err := h.tt.RightClick("Stuck"); err != nil {
		t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem("Delete…"); err == nil {
		t.Fatal("Delete… was offered while the run is uncertain")
	}
	h.tt.CloseMenu()
	h.a.select_(th)
	h.tt.Advance(time.Second)
	h.click("Stop")
	h.until("the run to end", func() bool { return th.View.Run == api.RunIdle })
	h.tt.Advance(time.Second)
	if err := h.tt.RightClick("Stuck"); err != nil {
		t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem("Delete…"); err != nil {
		t.Fatalf("once the run ended: %v", err)
	}
}

// The chat shows a thread until its delete lands: one that doesn't reach
// the engine stays, with why.
func TestAFailedDeleteKeepsTheThreadShown(t *testing.T) {
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(false)))
	seed(t, d, "/work/t3code", "Ship the sidebar sort")
	h := start(t, dir)
	th := h.thread("Ship the sidebar sort")
	h.a.select_(th)
	d.Close()
	h.until("the engine gone", func() bool { return h.local().Status != client.Status(client.Online{}) })
	h.a.deleteTree(th)
	h.until("the failure", func() bool { return len(th.Pending) == 1 && th.Pending[0].Err != nil })
	h.tt.Advance(time.Second)
	if h.a.current().id() != th.id() || !h.tt.HasText("Couldn't delete the thread: client: not connected to the engine") {
		t.Fatalf("after a failed delete the chat shows %q: %q", h.a.current().View.Title, h.tt.Texts())
	}
}

// A reply's Retry waits while the run is live, an uncertain one too,
// which would refuse the message.
func TestRetryWaitsWhileTheRunIsLive(t *testing.T) {
	h := start(t, uncertainDir(t))
	th := h.thread("Stuck")
	h.a.select_(th)
	h.until("its transcript", func() bool { return h.tt.HasText("May I edit it?") })
	h.tt.Advance(time.Second) // the composer docked from the hero the app opened on
	h.hover("Looking at the code.")
	h.tt.Advance(time.Second)
	h.click("Retry")
	h.tt.Advance(time.Second)
	if len(th.Pending) != 0 {
		t.Fatalf("Retry sent %d commands to an uncertain run", len(th.Pending))
	}
}

// A message the engine refuses to steer in comes back to the composer,
// and a toast says why.
func TestARefusedSteerComesBackToTheComposer(t *testing.T) {
	h := start(t, uncertainDir(t))
	th := h.thread("Stuck")
	h.a.select_(th)
	h.a.draft = "Keep going."
	h.tt.Advance(time.Second)
	h.click("Send")
	h.until("the refusal", func() bool { h.tt.Frame(); return h.a.draft == "Keep going." && len(th.Pending) == 0 })
	h.tt.Frame()
	if !h.tt.HasText("the agent is uncertain and can't take a message; send it once the run ends") || h.tt.HasText("Not sent: api: rejected: the agent is uncertain and can't take a message; send it once the run ends") {
		t.Fatalf("after a refused steer: %q", h.tt.Texts())
	}
}

// A refused steer never loses its text: over a draft of the user's, or
// while another thread is shown, it stays in its thread's timeline, not
// sent, with Retry and Dismiss; the composer keeps what it holds.
func TestARefusedSteerStaysInTheTimeline(t *testing.T) {
	for _, tc := range []struct {
		name  string
		after func(h *harness) // what the user does as the steer goes
		draft string
	}{
		{"over a newer draft", func(h *harness) { h.a.draft = "And the README." }, "And the README."},
		{"from another thread", func(h *harness) { h.a.selected = api.ID{}; h.a.follow() }, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := start(t, uncertainDir(t))
			th := h.thread("Stuck")
			h.a.select_(th)
			h.a.draft = "Keep going."
			h.tt.Advance(time.Second)
			h.click("Send")
			tc.after(h)
			h.until("the refusal", func() bool { h.tt.Frame(); return len(th.Pending) == 1 && th.Pending[0].Err != nil })
			h.tt.Advance(time.Second)
			if h.a.draft != tc.draft {
				t.Errorf("the composer holds %q, want %q", h.a.draft, tc.draft)
			}
			h.a.select_(th)
			h.tt.Frame()
			h.tt.Advance(time.Second)
			if !h.tt.HasText("Keep going.") || !h.tt.HasText("Retry") || !h.tt.HasText("Dismiss") || h.a.draft != tc.draft {
				t.Fatalf("the refused steer isn't kept with Retry and Dismiss, or the composer took it: draft %q, texts %q", h.a.draft, h.tt.Texts())
			}
		})
	}
}

// Every steer refused is kept, not just the last: over a newer draft,
// each stays in the timeline.
func TestTwoRefusedSteersAreBothKept(t *testing.T) {
	h := start(t, uncertainDir(t))
	th := h.thread("Stuck")
	h.a.select_(th)
	for _, m := range []string{"First.", "Second."} {
		h.a.draft = m
		h.a.send(th)
	}
	h.a.draft = "Third, not sent yet."
	h.until("the refusals", func() bool {
		h.tt.Frame()
		return len(th.Pending) == 2 && th.Pending[0].Err != nil && th.Pending[1].Err != nil
	})
	h.tt.Advance(time.Second)
	if !h.tt.HasText("First.") || !h.tt.HasText("Second.") || h.a.draft != "Third, not sent yet." {
		t.Fatalf("composer %q, timeline %q; want both refused steers kept and the draft untouched", h.a.draft, h.tt.Texts())
	}
}

// Stop stays in reach while the composer holds a message: the run stops,
// and the message stays.
func TestStopWhileTheComposerHoldsAMessage(t *testing.T) {
	h := start(t, uncertainDir(t))
	th := h.thread("Stuck")
	h.a.select_(th)
	h.a.draft = "Next, the README."
	h.tt.Advance(time.Second)
	if _, ok := h.tt.Find("Send"); !ok {
		t.Fatal("no Send for the message")
	}
	h.click("Stop")
	h.until("the run's end", func() bool { return !th.View.Live() })
	if h.a.draft != "Next, the README." {
		t.Fatalf("the composer holds %q after Stop", h.a.draft)
	}
}
