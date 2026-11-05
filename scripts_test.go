package main

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestScriptedCounter(t *testing.T) {
	a := newApp()
	tt := ui.NewTester(a.view, 1320, 860)
	a.router.Push("/settings/scripts")
	deadline := time.Now().Add(30 * time.Second)
	for !tt.HasText("Count: 0") {
		if time.Now().After(deadline) {
			t.Fatalf("the counter never rendered: %q", tt.Texts())
		}
		time.Sleep(10 * time.Millisecond)
		tt.Frame()
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
