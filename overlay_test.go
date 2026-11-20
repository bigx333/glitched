package main

import (
	"go/ast"
	"go/parser"
	gotoken "go/token"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/daemon"
	"glitched/internal/provider/fake"
)

// overlayMakers are MyGo's ways to show something over the app: the app's
// dialogs, popovers and panels, which take the keyboard, and its tooltips
// and toasts.
var overlayMakers = map[string]bool{
	"AlertDialog": true, "DialogBase": true, "Modal": true, "Overlay": true, "Popover": true, "PopoverBase": true,
	"Tooltip": true, "TooltipBase": true, "Toast": true, "ToastBase": true,
}

// Every overlay of the app is built in overlay.go, which notes that it
// shows, so the cards' keys wait under it, and covers what it lies over,
// so a card counts no line under it read: a dialog, popover, tooltip or
// toast built elsewhere would leave the keys reaching the card behind it,
// or a line it hides read.
func TestEveryOverlayIsTheApps(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := gotoken.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") || name == "overlay.go" {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !overlayMakers[sel.Sel.Name] {
				return true
			}
			// ui.Modal(c, …) and an element's .Modal() both make a dialog,
			// and an element's .Tooltip() a tooltip.
			if x, ok := sel.X.(*ast.Ident); ok && x.Name == "ui" || sel.Sel.Name == "Modal" || sel.Sel.Name == "Tooltip" {
				t.Errorf("%s: %s outside overlay.go; build it with the app's own (a.popover, a.dialog, a.alert, a.overlay, a.tip, a.toast)",
					fset.Position(call.Pos()), sel.Sel.Name)
			}
			return true
		})
	}
}

// An overlay dismissed gives the focus back to what had it before it
// opened: a turn's marker, past the window's or not, a thread in the
// sidebar, or a popover's button. Each key is followed by a frame and no
// more, as on the Mac.
func TestDismissingGivesTheFocusBack(t *testing.T) {
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(false)))
	seed(t, d, "/work/t3code", "A long thread", "Start")
	seed(t, d, "/work/t3code", "Another thread", "Hello")
	h := start(t, dir)
	long := h.thread("A long thread")
	h.a.select_(long)
	h.until("its transcript", func() bool { return long.Transcript != nil && len(long.Transcript.Entries) > 0 })
	long.Transcript.Entries = synthetic(3000)
	h.a.changed()
	h.tt.Frame()
	h.tt.Advance(time.Second)
	marker := func(k int) string { return "Go to: " + turnText(k) }
	focused := func(t *testing.T, name, after string) {
		t.Helper()
		if h.tt.Focused(name) {
			return
		}
		now := "nothing"
		for _, e := range h.tt.AccessAll() {
			if e.Focused {
				now = e.Name
			}
		}
		t.Fatalf("%s, %q has the focus, want %q", after, now, name)
	}
	past := markersShown(3000, 860) + 10
	h.click(marker(0))
	h.tt.Move(660, 300)
	h.tt.Advance(time.Second)
	for range past {
		h.tt.KeyRepeat(0, ui.KeyTab)
	}
	focused(t, marker(past), "Tab past the window")

	t.Run("the palette, from a turn past the window", func(t *testing.T) {
		for range 2 {
			h.tt.KeyRepeat(ui.Cmd, ui.KeyK)
			if !h.a.cmdkOpen {
				t.Fatal("⌘K didn't open the palette")
			}
			h.tt.KeyRepeat(0, ui.KeyEscape)
			if h.a.cmdkOpen {
				t.Fatal("Escape didn't close the palette")
			}
			focused(t, marker(past), "the palette dismissed")
			h.tt.KeyRepeat(0, ui.KeyTab)
			focused(t, marker(past+1), "Tab after it")
			h.tt.KeyRepeat(ui.Shift, ui.KeyTab)
		}
	})
	t.Run("the Delete alert, from a turn", func(t *testing.T) {
		h.a.settings.confirmDelete = true
		if err := h.tt.RightClick("Another thread"); err != nil {
			t.Fatal(err)
		}
		if err := h.tt.ChooseMenuItem("Delete…"); err != nil {
			t.Fatal(err)
		}
		h.tt.Frame()
		if !h.a.deleteOpen {
			t.Fatal("Delete… didn't ask")
		}
		h.tt.KeyRepeat(0, ui.KeyEscape)
		if h.a.deleteOpen || h.a.deleting.Thread != nil {
			t.Fatal("Escape didn't dismiss the alert")
		}
		focused(t, marker(past), "the alert dismissed")
	})
	t.Run("a popover, from its button", func(t *testing.T) {
		// The composer, and the picker's button in it, show at the end.
		if err := h.tt.Click("Jump to latest"); err != nil {
			t.Fatal(err)
		}
		h.tt.Advance(time.Second)
		if err := h.tt.Click("Model"); err != nil {
			t.Fatal(err)
		}
		if !h.a.pickerOpen {
			t.Fatal("the model picker didn't open")
		}
		h.tt.KeyRepeat(0, ui.KeyEscape)
		if h.a.pickerOpen {
			t.Fatal("Escape didn't close the picker")
		}
		focused(t, "Model", "the picker dismissed")
	})
	t.Run("the palette, from a thread in the sidebar", func(t *testing.T) {
		if err := h.tt.Click("Another thread"); err != nil {
			t.Fatal(err)
		}
		h.tt.KeyRepeat(ui.Cmd, ui.KeyK)
		h.tt.KeyRepeat(0, ui.KeyEscape)
		focused(t, "Another thread", "the palette dismissed")
	})
}
