package main

import (
	"go/ast"
	"go/parser"
	gotoken "go/token"
	"image"
	"path/filepath"
	"slices"
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
// sidebar, or a popover's button, and shows where it went. Each key comes
// as one event, with no frame between its press and its release, as the
// Mac probe posts them: KeyRepeat sends the press alone, and the frames it
// asks for follow.
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
	t.Run("the palette, from a turn in view, drawn focused", func(t *testing.T) {
		if err := h.tt.Click(marker(0)); err != nil {
			t.Fatal(err)
		}
		h.tt.Move(660, 300)
		h.tt.KeyRepeat(0, ui.KeyTab)
		h.tt.Advance(time.Second)
		focused(t, marker(1), "Tab from turn 0")
		e, ok := h.tt.Access(marker(1))
		if !ok {
			t.Fatal("turn 1's marker doesn't show")
		}
		// The marker's box and the ring around it.
		r := e.Bounds
		at := image.Rect(int(r.X)-4, int(r.Y)-4, int(r.X+r.W)+4, int(r.Y+r.H)+4)
		shown := func() []byte {
			img := h.tt.Image()
			return img.SubImage(at).(*image.RGBA).Pix
		}
		want := slices.Clone(shown())
		h.tt.KeyRepeat(ui.Cmd, ui.KeyK)
		h.tt.KeyRepeat(0, ui.KeyEscape)
		focused(t, marker(1), "the palette dismissed")
		if !slices.Equal(shown(), want) {
			t.Error("the marker given the focus back isn't drawn as it was focused")
		}
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
		focused(t, "Another thread", "a click on it")
		h.tt.KeyRepeat(ui.Cmd, ui.KeyK)
		h.tt.KeyRepeat(0, ui.KeyEscape)
		focused(t, "Another thread", "the palette dismissed")
	})
}

// Whatever an overlay's content takes the focus with as it shows, the
// app's popovers and dialogs give it back to what had it before.
func TestOverlaysKeepTheFocusBefore(t *testing.T) {
	for _, kind := range []string{"popover", "dialog"} {
		t.Run(kind, func(t *testing.T) {
			a := app{covers: map[coverKey]*ui.Handle{}}
			var open bool
			var q string
			tt := ui.NewTester(func(c *ui.Context) {
				ui.Button(c.Key("before"), "Before")
				b := ui.Button(c.Key("anchor"), "Open")
				if c.Shortcut(ui.Cmd, ui.KeyK) {
					open = true
				}
				field := func() { ui.TextInput(c.Key("q"), &q).Label("Query").AutoFocus() }
				if kind == "popover" {
					a.popover(c, b, &open, func(panel ui.Element) { panel.Children(field) })
				} else {
					a.dialog(c, &open, func(_, panel ui.Element) { panel.Children(field) })
				}
			}, 400, 300)
			tt.Frame()
			if err := tt.Click("Before"); err != nil {
				t.Fatal(err)
			}
			tt.KeyRepeat(ui.Cmd, ui.KeyK)
			if !open || !tt.Focused("Query") {
				t.Fatalf("⌘K: open %v, the field focused %v", open, tt.Focused("Query"))
			}
			tt.KeyRepeat(0, ui.KeyEscape)
			if open {
				t.Fatal("Escape didn't close it")
			}
			if !tt.Focused("Before") {
				t.Error("the focus didn't go back to what had it before")
			}
		})
	}
}
