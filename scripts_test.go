package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestScriptedCounter(t *testing.T) {
	a := newApp()
	tt := ui.NewTester(a.view, 1320, 860)
	a.router.Push("/settings/scripts")
	tt.Frame()
	if !tt.HasText("Count: 0") {
		t.Fatalf("the counter did not render on its first frame: %q", tt.Texts())
	}
	for range 2 {
		if err := tt.Click("Increment"); err != nil {
			t.Fatal(err)
		}
	}
	if !tt.HasText("Count: 2") || a.counter.Value("count") != 2.0 {
		t.Fatalf("after 2 clicks: count %v, texts %q", a.counter.Value("count"), tt.Texts())
	}
	if err := tt.Click("Reset"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("Count: 0") {
		t.Fatalf("after reset: %q", tt.Texts())
	}
}
