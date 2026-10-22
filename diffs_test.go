package main

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// A file row's actions mark it reviewed, revert it and copy its path;
// the hunk bar steps and keeps from the keyboard.
func TestDiffFileActions(t *testing.T) {
	a := newApp()
	a.showDiff = true
	tt := ui.NewTester(a.view, 1320, 860)
	path := "apps/web/src/components/ChevronIcon.tsx"
	r, ok := tt.Find("Diff of " + path)
	if !ok {
		t.Fatal("no file row")
	}
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	tt.Advance(time.Second)
	if err := tt.Click("Mark reviewed"); err != nil {
		t.Fatal(err)
	}
	if !a.reviewed[path] {
		t.Error("file not marked reviewed")
	}
	if err := tt.Click("Copy path"); err != nil {
		t.Fatal(err)
	}
	if tt.Clipboard() != path {
		t.Errorf("clipboard %q", tt.Clipboard())
	}
	if err := tt.Click("Revert file"); err != nil {
		t.Fatal(err)
	}
	if n := len(a.diffFor(path).Hunks()); n != 0 {
		t.Errorf("%d changes left after reverting the file", n)
	}

	d := a.diffFor("apps/web/src/components/Sidebar.tsx")
	n := len(d.Hunks())
	if n < 2 {
		t.Fatalf("%d changes in the mock diff, want at least 2", n)
	}
	tt.Key(ui.Cmd, ui.KeyBracketRight)
	if i, _ := d.CurrentHunk(); i != 1 {
		t.Errorf("⌘] went to change %d, want 1", i)
	}
	tt.Key(ui.Cmd, ui.KeyBracketLeft)
	if i, _ := d.CurrentHunk(); i != 0 {
		t.Errorf("⌘[ went to change %d, want 0", i)
	}
	tt.Key(ui.Cmd|ui.Shift, ui.KeyEnter)
	if len(d.Hunks()) != n-1 {
		t.Errorf("%d changes after ⇧⌘↩, want %d", len(d.Hunks()), n-1)
	}
}
