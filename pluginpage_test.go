package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"

	"glitched/internal/plugins"
)

func TestAPluginPageShowsAndItsCommandRuns(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(plugins.Dir(dir), "hello")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"plugin.json", "index.js"} {
		b, err := os.ReadFile(filepath.Join("examples/plugins/hello", f))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, f), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p := &posts{wake: make(chan struct{}, 1)}
	a := newApp(dir, nil, p.post)
	t.Cleanup(a.plugins.Close)
	h := &harness{t, a, ui.NewTester(a.view, 1320, 860), p}
	h.tt.Frame()

	h.click("Hello")
	if a.router.Path() != "/plugins/hello/home" {
		t.Fatalf("path %q", a.router.Path())
	}
	h.until("the plugin's page", func() bool { return h.tt.HasText("Greeted 0 times") })
	for _, want := range []string{"A sample plugin's page, drawn by Glitched", "GREETINGS", "mygo-plugin", "Open"} {
		if !h.tt.HasText(want) {
			t.Errorf("no %q in %q", want, h.tt.Texts())
		}
	}
	h.click("Greet")
	h.until("the page read again after greet", func() bool { return h.tt.HasText("Greeted once") })
}
