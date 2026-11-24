package main

import (
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/daemon"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
)

// subagentTurn is a turn whose agent starts a native subagent: the
// subagent's text, its command and its end nest under the Agent step.
func subagentTurn() []provider.Event {
	exit := 0
	nest := func(e provider.Event) provider.Event { return provider.Nested{Parent: "task-1", Event: e} }
	return []provider.Event{
		provider.Work{Item: "task-1", Kind: "tool", Title: "Agent alpha", Status: api.WorkStarted},
		nest(provider.TextDelta{Text: "Looking for the sort."}),
		nest(provider.Work{Item: "c-1", Kind: "command", Title: "rg sortThreads", Status: api.WorkStarted}),
		nest(provider.Work{Item: "c-1", Kind: "command", Title: "rg sortThreads", Status: api.WorkCompleted, Exit: &exit}),
		nest(provider.TextDelta{Text: "It sorts in sidebar.go."}),
		nest(provider.TurnEnded{Outcome: api.OutcomeCompleted, Tokens: 11738}),
		provider.Work{Item: "task-1", Kind: "tool", Title: "Agent alpha", Status: api.WorkCompleted},
		provider.TextDelta{Text: "Alpha found the sort."},
	}
}

// subagentShot writes the window as it is to $SUBAGENTSHOTS, when set,
// there as name.png.
func subagentShot(t *testing.T, h *harness, name string) {
	out := os.Getenv("SUBAGENTSHOTS")
	if out == "" {
		return
	}
	f, err := os.Create(filepath.Join(out, name+".png"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, h.tt.Image()); err != nil {
		t.Fatal(err)
	}
}

// The Agent step opens the panel of its subagent, which the built-in
// Subagent panel draws: what the subagent said and the step it took, and
// how it ended. None of it is in the run's reply. Its close button
// closes it, and the diff panel takes its place while open.
func TestAnAgentStepOpensItsSubagentsPanel(t *testing.T) {
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(subagentTurn()))
	seed(t, d, "/work/t3code", "Find the sort", "Where does the sidebar sort?")
	h := start(t, dir)
	th := h.thread("Find the sort")
	h.a.select_(th)
	h.until("its transcript", func() bool { return h.tt.HasText("Alpha found the sort.") })
	h.tt.Advance(time.Second)
	if h.tt.HasText("It sorts in sidebar.go.") {
		t.Fatalf("the subagent's text shows before its panel opens: %q", h.tt.Texts())
	}
	for _, e := range entriesOf(h.thread("Find the sort")) {
		if strings.Contains(e.text, "It sorts") {
			t.Fatalf("the subagent's text is in the run's entry %q", e.text)
		}
	}
	subagentShot(t, h, "step")
	h.click("Agent alpha")
	settlePlugins(t, h.a, h.tt, h.p)
	h.tt.Advance(time.Second)
	subagentShot(t, h, "panel")
	for _, want := range []string{"Looking for the sort.", "rg sortThreads", "It sorts in sidebar.go.", "Done", "11,738 tokens, not counted in the thread's"} {
		if !h.tt.HasText(want) {
			t.Fatalf("the subagent's panel doesn't show %q: %q", want, h.tt.Texts())
		}
	}
	if !h.a.panels().Subagent {
		t.Fatal("the panels don't say the subagent's is open")
	}
	h.a.showDiff = true
	h.tt.Frame()
	if h.tt.HasText("It sorts in sidebar.go.") {
		t.Fatal("the subagent's panel shows beside the diff panel")
	}
	h.a.showDiff = false
	settlePlugins(t, h.a, h.tt, h.p)
	h.tt.Advance(time.Second)
	h.click("Close subagent")
	settlePlugins(t, h.a, h.tt, h.p)
	h.tt.Advance(time.Second)
	if h.tt.HasText("It sorts in sidebar.go.") || h.a.panels().Subagent {
		t.Fatalf("Close subagent left its panel open: %q", h.tt.Texts())
	}
}
