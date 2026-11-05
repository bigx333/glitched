package main

import (
	"go/ast"
	"go/parser"
	gotoken "go/token"
	"path/filepath"
	"strings"
	"testing"
)

// overlayMakers are MyGo's ways to show something over the app that takes
// the keyboard: the app's dialogs, popovers and panels.
var overlayMakers = map[string]bool{
	"AlertDialog": true, "DialogBase": true, "Modal": true, "Overlay": true, "Popover": true, "PopoverBase": true,
}

// Every overlay of the app is built in overlay.go, which notes that it
// shows, so the question card's keys wait under it: a dialog or popover
// built elsewhere would leave them reaching the questions behind it.
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
			// ui.Modal(c, …) and an element's .Modal() both make a dialog.
			if x, ok := sel.X.(*ast.Ident); ok && x.Name == "ui" || sel.Sel.Name == "Modal" {
				t.Errorf("%s: %s outside overlay.go; build it with the app's own (a.popover, a.dialog, a.alert, a.overlay)",
					fset.Position(call.Pos()), sel.Sel.Name)
			}
			return true
		})
	}
}
