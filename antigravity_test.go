package main

import (
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/daemon"
	"glitched/internal/plugins/guard"
	"glitched/internal/provider"
	"glitched/internal/provider/acp"
	"glitched/internal/provider/acp/acptest"
	"glitched/internal/provider/codex/codextest"
)

func TestMain(m *testing.M) {
	if acptest.Main() || codextest.Main() {
		return
	}
	// The engine in-process runs gh when the wizard's Clone step shows;
	// no test may reach GitHub. A stub gh that fails, ahead of the real
	// PATH, keeps them off it; stubGH swaps in a test's own.
	bin, err := os.MkdirTemp("", "glitched-gh")
	if err != nil {
		panic(err)
	}
	bundledCache, err := os.MkdirTemp("", "glitched-bundled")
	if err != nil {
		panic(err)
	}
	testBundledDir = bundledCache
	ghBin = bin
	writeGH("echo 'gh: stub' >&2\nexit 1")
	userPath := guard.UserPath
	guard.UserPath = func() string { return bin + string(os.PathListSeparator) + userPath() }
	code := m.Run()
	os.RemoveAll(bin)
	os.RemoveAll(bundledCache)
	os.Exit(code)
}

// withAntigravity has the engine run Antigravity threads through the ACP
// adapter, the test binary playing agy in scenario.
func withAntigravity(t *testing.T, scenario string) func(*daemon.Config) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(acptest.Scenario, scenario)
	t.Setenv(acptest.Log, filepath.Join(t.TempDir(), "wire.jsonl"))
	return func(cfg *daemon.Config) {
		cfg.Providers["antigravity"] = &acp.Adapter{Profile: acp.Antigravity, Path: self}
	}
}

// shoot writes the window as SHOTS names a directory for, if it does.
func shoot(t *testing.T, h *harness, name string) {
	t.Helper()
	dir := os.Getenv("SHOTS")
	if dir == "" {
		return
	}
	f, err := os.Create(filepath.Join(dir, name+".png"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, h.tt.Image()); err != nil {
		t.Fatal(err)
	}
}

// The picker offers the models agy reports under Antigravity, with each
// one's efforts; a thread on one streams the agent's text and steps, and
// its approval is Glitched's own card, naming Antigravity.
func TestAnAntigravityThread(t *testing.T) {
	h := startWith(t, tempDir(t), withAntigravity(t, "gui"))
	allAnswered(h)
	h.until("agy's models", func() bool { return len(h.a.models.reported["antigravity"]) == 2 })
	h.a.model = modelKey{"antigravity", "fake-pro"}
	h.tt.Frame()
	h.click("Model")
	pickerSettle(t, h)
	for _, want := range []string{"Antigravity", "Fake Pro", "Fake Flash", "High"} {
		if !h.tt.HasText(want) {
			t.Fatalf("the picker shows no %q: %q", want, h.tt.Texts())
		}
	}
	var levels []string
	for _, e := range h.a.efforts() {
		levels = append(levels, e.label)
	}
	if !slices.Equal(levels, []string{"Low", "High"}) {
		t.Fatalf("Fake Pro's efforts %q, want agy's", levels)
	}
	shoot(t, h, "picker")
	h.click("Fake Pro")
	pickerSettle(t, h)

	sendNew(h, "Add a line to the notes.")
	h.until("the card", func() bool { return h.tt.HasText("Antigravity wants to") })
	if got := h.a.current().View.Model; got.Provider != "antigravity" || got.Model != "fake-pro" {
		t.Fatalf("the thread runs %+v", got)
	}
	// The text reveals over frames.
	for range 50 {
		h.tt.Advance(100 * time.Millisecond)
	}
	h.openWork("Paused for approval", "Read")
	for _, want := range []string{"I'll read the notes, then add a line to them.", "Read", "notes.txt", "Edit notes.txt", "Allow once", "Deny"} {
		if !h.tt.HasText(want) {
			t.Fatalf("the thread shows no %q: %q", want, h.tt.Texts())
		}
	}
	shoot(t, h, "approval")
	h.click("Allow once")
	h.until("the turn's end", func() bool { return !h.tt.HasText("Stop") })
	for range 50 {
		h.tt.Advance(100 * time.Millisecond)
	}
	if !h.tt.HasText("Answer: allow-once") {
		t.Fatalf("the thread shows no answer: %q", h.tt.Texts())
	}
	shoot(t, h, "answered")
}

// An Antigravity model whose id a Claude model also has stays
// Antigravity's: the thread made on it runs on Antigravity.
func TestAnAntigravityModelSharingAClaudeID(t *testing.T) {
	h := startWith(t, tempDir(t), engineReportsBy(map[string][]provider.Model{
		"claude":      {{ID: "claude-sonnet-4-6", Name: "Sonnet as Claude has it"}},
		"antigravity": {{ID: "claude-sonnet-4-6", Name: "Claude Sonnet 4.6 (Thinking)"}},
	}))
	allAnswered(h)
	h.until("both engines' models", func() bool { return len(h.a.models.reported["antigravity"]) == 1 })
	h.click("Model")
	pickerSettle(t, h)
	h.tt.Move(0, 0)
	h.click("Antigravity")
	pickerSettle(t, h)
	h.click("Claude Sonnet 4.6 (Thinking)")
	pickerSettle(t, h)
	if got := h.a.modelRef(); got.Provider != "antigravity" || got.Model != "claude-sonnet-4-6" {
		t.Fatalf("picking Antigravity's Sonnet made %+v", got)
	}
	id := h.a.backend.Create(h.local(), t.TempDir(), "t", h.a.modelRef(), api.AccessAsk, nil)
	h.until("the thread", func() bool { _, ok := h.a.find(id); return ok })
	if th, _ := h.a.find(id); th.View.Model.Provider != "antigravity" {
		t.Fatalf("the thread runs on %q", th.View.Model.Provider)
	}
}

// The card offers Always allow where agy offers to keep the grant, and
// it goes back as agy's own allow-always; where agy offers only once, or
// on a Claude card, there is no Always allow.
func TestAlwaysAllowOnTheCard(t *testing.T) {
	for _, tc := range []struct {
		scenario string
		offered  bool
	}{{"gui", true}, {"gui-once", false}} {
		t.Run(tc.scenario, func(t *testing.T) {
			h := startWith(t, tempDir(t), withAntigravity(t, tc.scenario))
			allAnswered(h)
			h.until("agy's models", func() bool { return len(h.a.models.reported["antigravity"]) == 2 })
			h.a.model = modelKey{"antigravity", "fake-pro"}
			h.tt.Frame()
			sendNew(h, "Add a line to the notes.")
			h.until("the card", func() bool { return h.tt.HasText("Antigravity wants to") })
			for range 50 {
				h.tt.Advance(100 * time.Millisecond)
			}
			if got := h.tt.HasText("Always allow"); got != tc.offered {
				t.Fatalf("the card shows Always allow %v, want %v: %q", got, tc.offered, h.tt.Texts())
			}
			shoot(t, h, "always-"+tc.scenario)
			if !tc.offered {
				return
			}
			h.click("Always allow")
			h.until("the turn's end", func() bool { return !h.tt.HasText("Stop") })
			for range 50 {
				h.tt.Advance(100 * time.Millisecond)
			}
			if !h.tt.HasText("Answer: allow-always") || !h.tt.HasText("Always allowed") {
				t.Fatalf("after Always allow, the thread shows %q", h.tt.Texts())
			}
		})
	}
}
