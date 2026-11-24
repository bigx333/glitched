package main

import (
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/client"
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

// subagentThreads are the app showing the last of threads named names,
// each of one turn the script plays, whose reply is "Parent reply.".
func subagentThreads(t *testing.T, script []provider.Event, names ...string) *harness {
	t.Helper()
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(append(script, provider.TextDelta{Text: "Parent reply."})))
	h := start(t, dir)
	for _, name := range names {
		id := seed(t, d, "/work/t3code", name)
		h.a.select_(h.thread(name))
		if _, err := d.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: id, Body: api.MessageDispatch{Text: "Run " + name, Mode: api.AfterActive}}); err != nil {
			t.Fatal(err)
		}
		h.until("the parent's reply", func() bool {
			for _, e := range h.a.current().Transcript.Entries {
				if a, ok := e.(*client.AssistantEntry); ok && a.Text == "Parent reply." && a.End != nil {
					return true
				}
			}
			return false
		})
	}
	h.tt.Advance(time.Second)
	return h
}

func openSubagentOf(t *testing.T, h *harness, title string) {
	t.Helper()
	h.click(title)
	settlePlugins(t, h.a, h.tt, h.p)
	h.tt.Advance(time.Second)
}

func spawnStep(item string) provider.Work {
	return provider.Work{Item: item, Kind: "tool", Title: "Agent " + item, Status: api.WorkCompleted}
}

func nestedText(parent, text string) provider.Nested {
	return provider.Nested{Parent: parent, Event: provider.TextDelta{Text: text}}
}

// With no plugin to draw it, the panel says so under the step's title,
// and Glitched's Close still closes it.
func TestASubagentPanelNoPluginDrawsStillCloses(t *testing.T) {
	h := subagentThreads(t, []provider.Event{spawnStep("alpha"), nestedText("alpha", "Alpha's text.")}, "No plugin")
	if err := h.a.plugins.SetEnabled("subagent-panel", false); err != nil {
		t.Fatal(err)
	}
	h.a.pluginsChanged(nil)
	openSubagentOf(t, h, "Agent alpha")
	if !h.a.panels().Subagent || !h.tt.HasText("No plugin draws this panel. Turn on Subagent panel in Settings › Plugins.") {
		t.Fatalf("with no plugin the panel doesn't say so: %q", h.tt.Texts())
	}
	h.click("Close subagent")
	if h.a.panels().Subagent {
		t.Fatal("Close subagent left the panel open with no plugin to draw it")
	}
}

// A subagent's panel closed while the diff covers it stays closed when
// the diff closes.
func TestASubagentPanelClosedUnderTheDiffStaysClosed(t *testing.T) {
	h := subagentThreads(t, []provider.Event{spawnStep("alpha"), nestedText("alpha", "Alpha's text.")}, "Covered")
	openSubagentOf(t, h, "Agent alpha")
	for _, args := range []string{`{"panel":"diff","open":true}`, `{"panel":"subagent","open":false}`, `{"panel":"diff","open":false}`} {
		if _, err := h.a.panelEffect(json.RawMessage(args)); err != nil {
			t.Fatal(err)
		}
	}
	settlePlugins(t, h.a, h.tt, h.p)
	if h.a.panels().Subagent || h.tt.HasText("Alpha's text.") {
		t.Fatal("the subagent's panel, closed under the diff, came back as the diff closed")
	}
}

// A step id a later run uses again opens that run's subagent.
func TestAStepIDALaterRunReusesOpensItsOwnSubagent(t *testing.T) {
	h := subagentThreads(t, []provider.Event{spawnStep("alpha"), nestedText("alpha", "First child's text.")}, "Reused")
	run := api.NewID()
	tr := h.a.current().Transcript
	for _, body := range []api.EventBody{
		api.MessageQueued{Message: run, Text: "Next.", Mode: api.AfterActive},
		api.TurnAdmitted{Run: run}, api.TurnStarted{Run: run},
		api.WorkItem{Run: run, Item: "alpha", Kind: "tool", Title: "Agent beta", Status: api.WorkCompleted},
		api.SubagentDelta{Run: run, Parent: "alpha", Text: "Second child's text."},
		api.SubagentEnded{Run: run, Parent: "alpha"},
		api.TurnEnded{Run: run},
	} {
		p, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		cursor := tr.Cursor
		cursor.Seq++
		tr.Apply(api.Event{Cursor: cursor, Type: api.EventType(body), Schema: api.EventSchema, At: time.Now(), Payload: p})
	}
	h.a.changed()
	settlePlugins(t, h.a, h.tt, h.p)
	h.tt.Advance(time.Second)
	openSubagentOf(t, h, "Agent beta")
	if !h.tt.HasText("Second child's text.") || h.tt.HasText("First child's text.") {
		t.Fatalf("Agent beta opened another run's subagent: %q", h.tt.Texts())
	}
}

// At the narrowest window the panel's Close is in it and closes it.
func TestASubagentPanelClosesAtTheNarrowestWindow(t *testing.T) {
	for _, bar := range []ui.TitleBar{{Height: 52, Left: 78}, {Height: 52}} {
		t.Run(fmt.Sprint(bar.Right), func(t *testing.T) {
			h := subagentThreads(t, []provider.Event{spawnStep("alpha"), nestedText("alpha", "Alpha's text."),
				provider.Nested{Parent: "alpha", Event: provider.TurnEnded{Outcome: api.OutcomeCompleted}}}, "Narrow")
			openSubagentOf(t, h, "Agent alpha")
			h.tt.SetSize(760, 860)
			h.tt.SetTitleBar(bar)
			settlePlugins(t, h.a, h.tt, h.p)
			h.tt.Advance(time.Second)
			r, ok := h.tt.Find("Close subagent")
			if !ok || r.X+r.W > 760-bar.Right {
				t.Fatalf("Close subagent at %+v, found %v, is not within the window's %g points left of its buttons", r, ok, 760-bar.Right)
			}
			h.tt.ClickAt(r.X+r.W/2, r.Y+r.H/2)
			if h.a.panels().Subagent {
				t.Fatal("a click on Close subagent left the panel open")
			}
		})
	}
}

// Each subagent's panel opens at its start, not where another's was
// scrolled to.
func TestEachSubagentsPanelOpensAtItsStart(t *testing.T) {
	script := []provider.Event{spawnStep("alpha"), spawnStep("beta")}
	for _, child := range []string{"alpha", "beta"} {
		script = append(script, nestedText(child, "Start of "+child+"."))
		for i := range 75 {
			script = append(script, provider.Nested{Parent: child, Event: provider.Work{Item: fmt.Sprint(i), Kind: "command", Title: fmt.Sprintf("%s command %d", child, i), Status: api.WorkCompleted}})
		}
	}
	h := subagentThreads(t, script, "Scrolled")
	openSubagentOf(t, h, "Agent alpha")
	r, ok := h.tt.Find("Start of alpha.")
	if !ok {
		t.Fatal("alpha's panel doesn't show its start")
	}
	h.tt.Scroll(r.X+r.W/2, r.Y+200, 0, 999)
	h.tt.Advance(time.Second)
	if now, ok := h.tt.Find("Start of alpha."); ok && now.Y >= r.Y {
		t.Fatal("alpha's panel didn't scroll")
	}
	openSubagentOf(t, h, "Agent beta")
	if now, ok := h.tt.Find("Start of beta."); !ok || now.Y < r.Y-1 {
		t.Fatalf("beta's panel opened at %+v, found %v, scrolled as alpha's was from %+v", now, ok, r)
	}
}

// A subagent past what one view of the panel may hold, in steps, their
// output, a title or its text, shows its latest parts and says how many
// came before; the panel is the built-in's, not a failed view's.
func TestALongSubagentShowsItsLatestParts(t *testing.T) {
	script := []provider.Event{spawnStep("alpha"), nestedText("alpha", strings.Repeat("Words, ", 40000))}
	for i := range 300 {
		script = append(script, provider.Nested{Parent: "alpha", Event: provider.Work{Item: fmt.Sprint(i), Kind: "command",
			Title: strings.Repeat("t", 33000), Status: api.WorkFailed, Output: strings.Repeat("\x1b", 8000)}})
	}
	script = append(script, nestedText("alpha", "The last words."))
	h := subagentThreads(t, script, "Long")
	openSubagentOf(t, h, "Agent alpha")
	earlier := slices.ContainsFunc(h.tt.Texts(), func(s string) bool { return strings.HasSuffix(s, " earlier parts not shown") })
	if !h.tt.HasText("The last words.") || !earlier {
		t.Fatalf("the long subagent's panel doesn't show its latest parts and how many came before: %v", h.a.plugins.App("subagent-panel").Err)
	}
}

// The panel's context keeps a text's end however little of its budget is
// left, and never a cut with no character in it; text a view would
// escape past its room keeps its latest words.
func TestASubagentsContextKeepsItsLatestText(t *testing.T) {
	for _, left := range []int{1, 2, 3, 4} {
		newest := strings.Repeat("n", subagentText-left)
		w := client.WorkStep{Title: "Agent alpha", Subagent: &client.Subagent{Parts: []client.SubagentPart{
			{Text: "An older paragraph."},
			{Step: &client.WorkStep{Item: "c", Kind: "command", Title: "ls", Status: api.WorkCompleted}},
			{Text: newest},
		}}}
		p := subagentContext(w)
		last := p.Parts[len(p.Parts)-1]
		if last.Text != newest || p.Earlier+len(p.Parts) != 3 {
			t.Fatalf("with %d bytes left, the context is %d parts and %d earlier, want the newest text whole and every part counted", left, len(p.Parts), p.Earlier)
		}
		for _, part := range p.Parts {
			if part.Step == nil && (part.Text == "" || part.Text == "…") {
				t.Fatalf("with %d bytes left, the context holds a text cut to %q", left, part.Text)
			}
		}
	}
	escaped := strings.Repeat("<", 120000) + " The last words."
	p := subagentContext(client.WorkStep{Title: "Agent alpha", Subagent: &client.Subagent{Parts: []client.SubagentPart{{Text: escaped}}}})
	if len(p.Parts) != 1 || !strings.HasSuffix(p.Parts[0].Text, " The last words.") {
		t.Fatalf("text all escapes left the context %d parts, %d earlier, want its end kept", len(p.Parts), p.Earlier)
	}
	if b, _ := json.Marshal(p.Parts); len(b) > subagentBytes {
		t.Fatalf("the context's parts are %d bytes as JSON, past %d", len(b), subagentBytes)
	}
}

// At the narrowest window, with the sidebar shown, a subagent's panel
// takes the sidebar's room rather than squeeze its transcript beside the
// chat, as the diff panel does; Show sidebar brings the sidebar back in
// the panel's place. A window with room for all three keeps them.
func TestAtTheNarrowestWindowASidePanelTakesTheSidebarsRoom(t *testing.T) {
	long := "The sidebar sorts its threads by when each last changed, the newest first, in sidebar.go."
	h := subagentThreads(t, []provider.Event{spawnStep("alpha"), nestedText("alpha", long)}, "Narrow room")
	h.tt.SetSize(760, 860)
	h.tt.Advance(time.Second)
	openSubagentOf(t, h, "Agent alpha")
	h.tt.Advance(time.Second)
	subagentShot(t, h, "narrow-760")
	r, ok := h.tt.Find(long)
	if !ok || r.W < 280 || r.X+r.W > 760 {
		t.Fatalf("at 760 points the subagent's transcript is %g points wide at %g, found %v, want at least 280 within the window", r.W, r.X, ok)
	}
	if h.a.sidebarOpen != 0 || !h.a.showSidebar {
		t.Fatalf("the sidebar is %g open, shown %v, want it out of the way, still the user's choice", h.a.sidebarOpen, h.a.showSidebar)
	}
	h.click("Show sidebar  ⌘B")
	h.tt.Advance(time.Second)
	if h.a.panels().Subagent || h.a.sidebarOpen != 1 {
		t.Fatalf("Show sidebar left the panel open %v and the sidebar %g open, want the sidebar in its place", h.a.panels().Subagent, h.a.sidebarOpen)
	}
	h.a.showDiff = true
	h.tt.Frame()
	h.tt.Advance(time.Second)
	if h.a.sidebarOpen != 0 || h.a.diffWidth() < 300 {
		t.Fatalf("the diff panel at 760 points leaves the sidebar %g open and is %g wide, want it in the sidebar's room", h.a.sidebarOpen, h.a.diffWidth())
	}
	h.a.showDiff = false
	h.tt.Frame()
	h.tt.SetSize(1320, 860)
	h.tt.Advance(time.Second)
	openSubagentOf(t, h, "Agent alpha")
	h.tt.Advance(time.Second)
	if h.a.sidebarOpen != 1 || !h.a.panels().Subagent {
		t.Fatalf("at 1320 points the sidebar is %g open beside the panel (open %v), want both", h.a.sidebarOpen, h.a.panels().Subagent)
	}
}
