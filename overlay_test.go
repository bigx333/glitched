package main

import (
	"go/ast"
	"go/parser"
	gotoken "go/token"
	"path/filepath"
	"strings"
	"testing"
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
