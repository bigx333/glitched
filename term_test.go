package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

// The drawer's terminal runs a real shell through libghostty-vt: typed
// commands run, and what they print shows. Skipped where the library
// can't load (offline, before its first download), or where the project
// it opens isn't checked out.
func TestRealTerminal(t *testing.T) {
	if fi, err := os.Stat(expandHome("~/Projects/t3-mygo")); err != nil || !fi.IsDir() {
		t.Skip("~/Projects/t3-mygo not checked out")
	}
	if err := terminal.Load(); err != nil {
		t.Skip("libghostty-vt unavailable:", err)
	}
	term, err := terminal.New(terminal.Options{Command: []string{"/bin/sh"}, Dir: projectDir("~/Projects/t3-mygo"), Theme: darkTheme.terminal()})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	tt := ui.NewTester(func(c *ui.Context) { terminal.View(c, term).Fill().AutoFocus() }, 640, 300)
	tt.Type("echo hello from $(basename $PWD)")
	tt.Key(0, ui.KeyEnter)
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(term.Text(), "hello from t3-mygo") {
		if time.Now().After(deadline) {
			t.Fatalf("no output; terminal shows %q", term.Text())
		}
		time.Sleep(20 * time.Millisecond)
		tt.Frame()
	}
}

func TestProjectDir(t *testing.T) {
	if d := projectDir("~/definitely/not/here"); d == "" || strings.Contains(d, "definitely") {
		t.Errorf("missing project dir %q, want the home directory", d)
	}
}
