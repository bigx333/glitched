package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestScriptedCounter(t *testing.T) {
	a := offline(t)
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

func TestScriptedWidgetTour(t *testing.T) {
	a := offline(t)
	tt := ui.NewTester(a.view, 1320, 1400)
	a.router.Push("/settings/scripts")
	tt.Frame()
	if !tt.HasText("Ada · Pro · 3 seats · volume 60% · notifications on · tags go, luau") {
		t.Fatalf("the tour did not render: %q", tt.Texts())
	}
	if err := tt.Click("Preferences"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.widgetTour.Value("tab") != 1.0 || !tt.HasText("Volume") {
		t.Fatalf("tab %v, texts %q", a.widgetTour.Value("tab"), tt.Texts())
	}
}
