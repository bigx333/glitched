package main

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// A thread row's context menu renames the thread in place, snoozes and
// settles it, copies its branch, archives it, and deletes it, asking
// first while the setting says to.
func TestThreadMenu(t *testing.T) {
	a := newApp()
	tt := ui.NewTester(a.view, 1320, 860)
	t4 := a.projects[0].threads[3]
	if err := tt.RightClick(t4.title); err != nil {
		t.Fatal(err)
	}
	if m := tt.Menu(); len(m) == 0 || m[0] != "Rename" {
		t.Fatalf("menu %q", m)
	}
	if err := tt.ChooseMenuItem("Copy branch name"); err != nil {
		t.Fatal(err)
	}
	if tt.Clipboard() != "main" {
		t.Errorf("clipboard %q", tt.Clipboard())
	}
	if err := tt.RightClick(t4.title); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Rename"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("Websocket loop")
	tt.Key(0, ui.KeyEnter)
	if t4.title != "Websocket loop" || a.renaming != "" {
		t.Fatalf("title %q, renaming %q", t4.title, a.renaming)
	}
	if err := tt.RightClick(t4.title); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Snooze", "Tomorrow"); err != nil {
		t.Fatal(err)
	}
	if t4.snoozed != "Tomorrow" {
		t.Errorf("snoozed %q", t4.snoozed)
	}
	// The toast naming the thread goes; Snoozed is closed, and its thread
	// shows once it's opened.
	tt.Advance(10 * time.Second)
	if err := tt.Click("Snoozed"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(time.Second) // its rows unfold
	if err := tt.RightClick(t4.title); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Archive"); err != nil {
		t.Fatal(err)
	}
	if indexOf(a.projects[0].threads, t4) >= 0 || indexOf(a.archived, t4) != 0 {
		t.Error("archived thread not moved to the archive")
	}

	// Deleting asks, and deleting the open thread opens another. The
	// open thread is in Working, closed by default.
	t1 := a.projects[0].threads[0]
	if err := tt.Click("Working"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(time.Second) // its rows unfold
	if err := tt.RightClick(t1.title); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Delete…"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("Delete “" + t1.title + "”?") {
		t.Fatalf("no alert; texts %q", tt.Texts())
	}
	if err := tt.Click("Delete"); err != nil {
		t.Fatal(err)
	}
	if indexOf(a.projects[0].threads, t1) >= 0 || a.selected == t1.id {
		t.Errorf("deleted thread still there or selected")
	}
	if _, cur := a.current(); cur == t1 {
		t.Error("the deleted thread is still current")
	}
	a.settings.confirmDelete = false
	t5 := a.projects[0].threads[len(a.projects[0].threads)-1]
	if err := tt.Click("Settled"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(time.Second) // its rows unfold
	if err := tt.RightClick(t5.title); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Delete…"); err != nil {
		t.Fatal(err)
	}
	if indexOf(a.projects[0].threads, t5) >= 0 || tt.HasText("Delete “"+t5.title+"”?") {
		t.Error("deleting without the setting should not ask")
	}
}

// An archived thread restored goes back to the project it came from.
func TestArchiveRestore(t *testing.T) {
	a := newApp()
	tt := ui.NewTester(a.view, 1320, 860)
	t6 := a.projects[1].threads[0]
	if err := tt.RightClick(t6.title); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Archive"); err != nil {
		t.Fatal(err)
	}
	a.router.Push("/settings/archived")
	tt.Frame()
	if err := tt.Click("Restore"); err != nil { // the newest archived, first
		t.Fatal(err)
	}
	if indexOf(a.projects[1].threads, t6) < 0 || indexOf(a.archived, t6) >= 0 {
		t.Error("restored thread is not back in its project")
	}
}

func indexOf(ts []*thread, t *thread) int {
	for i, x := range ts {
		if x == t {
			return i
		}
	}
	return -1
}

// Archiving a working thread ends its turn, so it comes back idle.
func TestArchiveStopsTurn(t *testing.T) {
	a := newApp()
	tt := ui.NewTester(a.view, 1320, 860)
	t1 := a.projects[0].threads[0] // working
	a.archive(t1)
	tt.Frame()
	if t1.status != statusIdle || t1.entries[len(t1.entries)-1].took != "Stopped" {
		t.Errorf("archived working thread: status %d, work log %q", t1.status, t1.entries[len(t1.entries)-1].took)
	}
}
