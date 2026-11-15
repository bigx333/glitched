package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
)

// The model and access chosen for a new thread are the default again
// once the app starts anew on the same state directory.
func TestTheNewThreadDefaultOutlivesARestart(t *testing.T) {
	dir := t.TempDir()
	a := newApp(dir, nil, func(func()) {})
	tt := ui.NewTester(a.view, 1320, 860)
	for _, click := range []string{"Model", "Sonnet 5.5", "Fast mode", "Access", accessAsk} {
		if err := tt.Click(click); err != nil {
			t.Fatal(err)
		}
	}
	a.effort = "Low"
	tt.Frame()
	again := newApp(dir, nil, func(func()) {})
	if got, want := again.choice(), (choice{Model: "Claude Sonnet 5.5", Effort: "Low", Fast: true, Access: accessAsk}); got != want {
		t.Fatalf("after a restart the composer has %+v, want %+v", got, want)
	}
}

// A composer.json naming what the pickers no longer offer keeps the
// app's own default for each such name.
func TestAStaleComposerFileKeepsTheDefaults(t *testing.T) {
	dir := t.TempDir()
	want := offline(t).choice()
	if err := os.WriteFile(filepath.Join(dir, "composer.json"), []byte(`{"model":"Claude Opus 3","effort":"Huge","access":"Root"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := newApp(dir, nil, func(func()) {}).choice(); got != want {
		t.Fatalf("from a stale file the composer has %+v, want %+v", got, want)
	}
}

// In a thread, the pickers show and change that thread's own model and
// access, within its provider, and leave the default for a new thread as
// it was.
func TestAThreadsPickersAreItsOwn(t *testing.T) {
	h := start(t, tempDir(t))
	opus := api.ModelRef{Provider: "claude", Model: "Claude Opus 5.5", Effort: "high"}
	id := h.a.backend.Create(h.local(), t.TempDir(), "Mine", opus, api.AccessAutoEdits)
	h.thread("Mine")
	h.a.selected = id
	h.a.model, h.a.access = "Claude Haiku 5.5", accessFull
	draft := h.a.choice()
	h.tt.Frame()
	if _, ok := h.tt.Find("Claude Opus 5.5"); !ok {
		t.Fatalf("the composer doesn't show the thread's model: %q", h.tt.Texts())
	}
	h.click("Access")
	h.click(accessAsk)
	h.click("Model")
	if _, ok := h.tt.Find("Codex"); ok {
		t.Error("the picker offers another provider's models in a Claude thread")
	}
	h.click("Sonnet 5.5")
	h.click("Fast mode")
	want := api.ModelRef{Provider: "claude", Model: "Claude Sonnet 5.5", Effort: "high", Fast: true}
	h.until("the thread's new model and access", func() bool {
		v := h.thread("Mine").View
		return v.Model == want && v.Access == api.AccessAsk
	})
	if h.a.choice() != draft {
		t.Errorf("the default for a new thread is %+v after changing a thread's, want %+v", h.a.choice(), draft)
	}
	if _, err := os.Stat(h.a.composerPath()); !os.IsNotExist(err) {
		t.Errorf("a thread's change saved the default for a new one: %v", err)
	}
}

// A thread of a provider the picker doesn't list is offered no models to
// switch to: it keeps its provider.
func TestAThreadOfAnUnlistedProviderIsOfferedNoModels(t *testing.T) {
	h := start(t, tempDir(t))
	id := h.a.backend.Create(h.local(), t.TempDir(), "Fake", api.ModelRef{Provider: "fake", Model: "fake"}, api.AccessAsk)
	h.thread("Fake")
	h.a.selected = id
	h.tt.Frame()
	h.click("Model")
	for _, s := range []string{"Claude", "Codex", "Sonnet 5.5", "GPT-5.5"} {
		if r, ok := h.tt.Find(s); ok && r.W > 0 {
			if err := h.tt.Click(s); err == nil {
				t.Errorf("the picker offers %q on a fake thread", s)
			}
		}
	}
	h.tt.Frame()
	if m := h.thread("Fake").View.Model; m.Model != "fake" {
		t.Errorf("the thread's model is %+v", m)
	}
}

// The effort chosen in a thread is that thread's, as the engine names it.
func TestAThreadsEffortIsItsOwn(t *testing.T) {
	h := start(t, tempDir(t))
	opus := api.ModelRef{Provider: "claude", Model: "Claude Opus 5.5", Effort: "high"}
	id := h.a.backend.Create(h.local(), t.TempDir(), "Mine", opus, api.AccessAsk)
	h.thread("Mine")
	h.a.selected = id
	h.tt.Frame()
	h.click("Model")
	faster, _ := h.tt.Find("Faster")
	h.tt.ClickAtWith(0, faster.X+stopInset, faster.Y-24) // Low's point
	h.tt.Advance(time.Second)
	h.until("the thread's new effort", func() bool { return h.thread("Mine").View.Model.Effort == "low" })
	if h.a.effort != "High" {
		t.Errorf("the default effort is %q after changing a thread's, want High", h.a.effort)
	}
}
