package main

import (
	"fmt"
	"math"
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

// zenTurns is a transcript of n turns, each a message, work, a reply in
// between, more work and the reply.
func zenTurns(n int) []client.Entry {
	var es []client.Entry
	at := time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)
	for i := range n {
		run := api.NewID()
		es = append(es,
			&client.UserEntry{Message: api.NewID(), Text: turnText(i), State: client.Sent},
			&client.WorkEntry{Run: run, Items: []client.WorkStep{{Item: "s1", Kind: "search", Title: "Searched for ListState", Status: api.WorkCompleted, Started: at, Ended: at}},
				Started: at, Ended: at.Add(12 * time.Second)},
			&client.AssistantEntry{Run: run, Text: zenBetween(i)},
			&client.WorkEntry{Run: run, Items: []client.WorkStep{{Item: "s2", Kind: "edit", Title: "Edited chat.go", Status: api.WorkCompleted, Started: at, Ended: at}},
				Started: at, Ended: at.Add(30 * time.Second)},
			&client.AssistantEntry{Run: run, Text: zenFinal(i)},
		)
	}
	return es
}

func zenBetween(i int) string { return fmt.Sprintf("Between %d: now the edit.", i) }
func zenFinal(i int) string   { return fmt.Sprintf("Final %d: the list keeps its place.", i) }

// zenThread is the app showing a thread whose transcript is es, with Zen
// mode as zen says, once it shows what last says.
func zenThread(t *testing.T, es []client.Entry, zen bool, last string) (*harness, thread) {
	t.Helper()
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(false)))
	seed(t, d, "/work/t3code", "A thread", "Start")
	h := start(t, dir)
	th := h.thread("A thread")
	h.a.select_(th)
	h.until("its first turn", func() bool { settleTester(h.tt); return h.tt.HasText("Done.") })
	fresh := *th.Transcript
	fresh.Entries = es
	th.Transcript.Replace(fresh)
	h.a.settings.zen = zen
	h.a.changed()
	h.until("its rows", func() bool { settleTester(h.tt); return h.tt.HasText(last) })
	h.tt.Advance(time.Second)
	return h, th
}

// zenLineText is the text of the Zen line that shows, "" for none.
func zenLineText(tt *ui.Tester) string {
	for _, s := range tt.Texts() {
		if s == "Worked" || strings.HasPrefix(s, "Worked for ") {
			return s
		}
	}
	return ""
}

// With Zen mode on, a finished turn shows its message, a line and the
// reply; the reply in between shows once the line is clicked.
func TestZenFoldsAFinishedTurnIntoALineThatOpens(t *testing.T) {
	dir := tempDir(t)
	engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(false)))
	h := start(t, dir)
	h.a.settings.zen = true
	h.a.draft = "Look at main.go."
	h.tt.Frame()
	h.click("Send")
	h.until("the reply", func() bool {
		settleTester(h.tt)
		return h.tt.HasText("Done.") && !h.a.current().View.Working()
	})
	h.tt.Advance(time.Second)
	line := zenLineText(h.tt)
	if line == "" || !h.tt.HasText("2 steps") || !h.tt.HasText("Look at main.go.") {
		t.Fatalf("no message, line and reply: %q", h.tt.Texts())
	}
	if h.tt.HasText("Looking at the code.") || h.tt.HasText("Read main.go") {
		t.Fatalf("the reply in between or a step shows while folded: %q", h.tt.Texts())
	}
	h.click(line)
	h.tt.Advance(time.Second)
	if !h.tt.HasText("Looking at the code.") || !h.tt.HasText("Read main.go") || !h.tt.HasText("Done.") {
		t.Fatalf("the open line doesn't show what it folds: %q", h.tt.Texts())
	}
	h.click(line)
	h.tt.Advance(time.Second)
	if h.tt.HasText("Looking at the code.") {
		t.Fatalf("the line closed again shows what it folds: %q", h.tt.Texts())
	}
	if len(h.a.zenOpen) != 0 {
		t.Errorf("the line closed is still kept: %v", h.a.zenOpen)
	}
	h.click(line)
	h.tt.Advance(time.Second)
	if !h.tt.HasText("Looking at the code.") || !h.tt.HasText("Read main.go") {
		t.Fatalf("the line opened again doesn't show what it folds: %q", h.tt.Texts())
	}
}

// With Zen mode on, a thread at work says it thinks under its last row,
// below the line and the reply it writes, though the line folds rows above
// it and the list has fewer items than rows.
func TestZenShowsThinkingUnderTheLastRow(t *testing.T) {
	h, gate := holding(t, fake.Turn(false), len(fake.Turn(false)))
	defer close(gate)
	h.a.settings.zen = true
	h.tt.SetSize(1320, 800)
	sendNew(h, "Look at main.go.")
	h.until("the reply so far", func() bool { return h.a.current().View.Working() && shows(h, "Done.") })
	th := h.a.current()
	v := h.a.viewFor(th.id(), h.a.rowsOf(th))
	if at := thinkingAt(th, h.a.rowsOf(th)); at < 0 || v.len() >= len(v.es) || v.pos(at) != v.len()-1 {
		t.Fatalf("the thinking row goes under row %d, of %d rows in %d items", at, len(v.es), v.len())
	}
	line := zenLineText(h.tt)
	thinking, ok := h.tt.Find("Thinking…")
	if !ok || line == "" {
		t.Fatalf("no line, or no Thinking…, with Zen on: %q", h.tt.Texts())
	}
	for _, s := range []string{line, "Done."} {
		r, ok := h.tt.Find(s)
		if !ok {
			t.Fatalf("no %q with Zen on: %q", s, h.tt.Texts())
		}
		if thinking.Y <= r.Y {
			t.Errorf("Thinking… at %v, not below %q at %v", thinking.Y, s, r.Y)
		}
	}
	if thinking.Y+thinking.H > 800 {
		t.Errorf("Thinking… at %v, below the window", thinking.Y)
	}
}

// With Zen mode on, a turn waiting on an approval shows the card whole,
// with the line saying it's paused, and allowing it finishes the turn.
func TestZenShowsTheApprovalItWaitsOn(t *testing.T) {
	h, _ := asking(t, fake.Turn(true))
	h.a.settings.zen = true
	h.tt.Frame()
	h.tt.Advance(time.Second)
	for _, s := range []string{"Approval needed", "Allow once", "Deny", "Paused for approval", "1 step", "May I edit it?"} {
		if !h.tt.HasText(s) {
			t.Errorf("no %q while waiting, with Zen on: %q", s, h.tt.Texts())
		}
	}
	if h.tt.HasText("Looking at the code.") {
		t.Errorf("the reply in between shows while folded: %q", h.tt.Texts())
	}
	h.click("Allow once")
	h.until("the turn to finish", func() bool {
		settleTester(h.tt)
		return h.tt.HasText("Done.") && !h.a.current().View.Working()
	})
	h.tt.Advance(time.Second)
	if zenLineText(h.tt) == "" || h.tt.HasText("Paused for approval") {
		t.Errorf("the finished turn's line is %q: %q", zenLineText(h.tt), h.tt.Texts())
	}
}

// With Zen mode on, a question shows and is answered as with it off.
func TestZenShowsTheQuestionItWaitsOn(t *testing.T) {
	h, th := questioning(t, fake.Asking())
	h.a.settings.zen = true
	h.tt.Frame()
	h.tt.Advance(time.Second)
	for _, s := range []string{"Which store should the cache use?", "SQLite", "Which suites gate the merge?", "Submit"} {
		if !h.tt.HasText(s) {
			t.Fatalf("no %q on the waiting card, with Zen on: %q", s, h.tt.Texts())
		}
	}
	for _, s := range []string{"SQLite", "Unit"} {
		h.click(s)
		h.tt.Frame()
	}
	h.click("Submit")
	answered(t, h, th, "Answered")
}

// With Zen mode on, ⌘⏎ allows a request only once the window draws its
// card, as with it off, while the work around it folds.
func TestZenCmdEnterAllowsOnlyWhatTheWindowDraws(t *testing.T) {
	above := provider.Work{Item: "r", Kind: "read", Title: "Read f", Status: api.WorkCompleted}
	for name, events := range map[string][]provider.Event{
		"work above":   {above, commandAsk("ask", curl)},
		"a step below": {above, commandAsk("ask", curl), provider.Work{Item: "w", Kind: "read", Title: "Read g", Status: api.WorkStarted}},
		"a reply below": {provider.TextDelta{Text: "First a look."}, above, commandAsk("ask", curl),
			provider.TextDelta{Text: strings.Repeat("All good here, nothing to see. ", 30)}},
	} {
		t.Run(name, func(t *testing.T) {
			h := askedAll(t, events...)
			h.a.settings.zen = true
			h.tt.SetSize(1320, 480)
			h.tt.Frame()
			h.tt.Advance(time.Second)
			if h.tt.HasText("Read f") {
				t.Fatalf("the work above shows while folded: %q", h.tt.Texts())
			}
			answerAsSeen(t, h, lastRequest(h.a.current()), cmdEnter(h))
		})
	}
}

// With Zen mode on, a run's error shows, and the line says how many of
// the steps it folds failed.
func TestZenShowsARunsError(t *testing.T) {
	es := zenTurns(2)
	run := es[len(es)-1].(*client.AssistantEntry).Run
	at := time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)
	es = append(es,
		&client.UserEntry{Message: api.NewID(), Text: "Try again.", State: client.Sent},
		&client.WorkEntry{Run: run, Items: []client.WorkStep{{Item: "f", Kind: "command", Title: "bun run test", Status: api.WorkFailed, Started: at, Ended: at}}, Started: at, Ended: at.Add(5 * time.Second)},
		&client.ErrorEntry{Run: run, Error: "The agent stopped: rate limited"},
	)
	h, _ := zenThread(t, es, true, "The agent stopped: rate limited")
	if !h.tt.HasText("1 failed") || h.tt.HasText("bun run test") {
		t.Fatalf("the line doesn't say a step failed, or shows it: %q", h.tt.Texts())
	}
	h.a.settings.zen = false
	h.tt.Frame()
	h.tt.Advance(time.Second)
	if !h.tt.HasText("The agent stopped: rate limited") || !h.tt.HasText("bun run test") || h.tt.HasText("1 failed") {
		t.Fatalf("with Zen off, the error and the step don't show as they are: %q", h.tt.Texts())
	}
}

// With Zen mode on, a turn's marker scrolls the timeline to the turn's
// message, wherever the lines above put it in the list.
func TestZenMarkersGoToTheirTurns(t *testing.T) {
	h, th := zenThread(t, zenTurns(100), true, zenFinal(99))
	h.click("Go to: " + turnText(40))
	h.tt.Advance(time.Second)
	v := h.a.viewFor(th.id(), h.a.rowsOf(th))
	if first, _ := h.a.listFor(th.id()).Visible(); first != v.pos(5*40) {
		t.Errorf("the marker of turn 40 scrolled to item %d, want %d", first, v.pos(5*40))
	}
	if !onScreen(h.tt, turnText(40)) {
		t.Fatalf("turn 40's message doesn't show: %q", h.tt.Texts())
	}
}

// Zen mode turned on and off keeps the timeline on what it showed, though
// the row at its top folds into a line and back.
func TestZenKeepsThePlaceAsItTurnsOnAndOff(t *testing.T) {
	h, th := zenThread(t, zenTurns(100), false, zenFinal(99))
	l := h.a.listFor(th.id())
	// Turn 49's last work at the top, turn 50's message under it.
	l.ScrollTo(5*49+3, ui.Start)
	h.tt.Frame()
	h.tt.Advance(time.Second)
	if !onScreen(h.tt, turnText(50)) || !h.tt.HasText("Edited chat.go") {
		t.Fatalf("turn 50's message doesn't show under turn 49's work: %q", h.tt.Texts())
	}
	for _, zen := range []bool{true, false, true} {
		h.a.settings.zen = zen
		h.tt.Frame()
		h.tt.Advance(time.Second)
		if !onScreen(h.tt, turnText(50)) {
			t.Fatalf("with Zen %v, turn 50's message went: %q", zen, h.tt.Texts())
		}
	}
	// Opening the line at the top keeps it there.
	line := slices.IndexFunc(h.tt.Texts(), func(s string) bool { return strings.HasPrefix(s, "Worked for ") })
	if line < 0 {
		t.Fatalf("no line: %q", h.tt.Texts())
	}
	text := h.tt.Texts()[line]
	before, _ := h.tt.Find(text)
	h.click(text)
	h.tt.Advance(time.Second)
	if after, ok := h.tt.Find(text); !ok || after.Y != before.Y {
		t.Fatalf("the line opened moved from %v to %v", before.Y, after.Y)
	}
}

// At the end of a long thread, with Zen mode on, opening the last turn's
// line keeps it where it was, on the next frame as once settled, though
// the list follows its end.
func TestZenOpeningTheLastLineAtTheEndKeepsItThere(t *testing.T) {
	es := zenTurns(30)
	// The last turn's line says "Worked for 1m2s", which no other line says.
	w := es[len(es)-2].(*client.WorkEntry)
	w.Ended = w.Started.Add(50 * time.Second)
	h, th := zenThread(t, es, true, zenFinal(29))
	if !h.a.listFor(th.id()).AtEnd() {
		t.Fatal("the list isn't at its end")
	}
	const line = "Worked for 1m2s"
	before, ok := h.tt.Find(line)
	if !ok {
		t.Fatalf("no line for the last turn: %q", h.tt.Texts())
	}
	h.click(line)
	h.tt.Frame()
	next, _ := h.tt.Find(line)
	h.tt.Advance(time.Second)
	after, ok := h.tt.Find(line)
	if !ok || next.Y != before.Y || after.Y != before.Y {
		t.Fatalf("the last line opened at the end went from %v to %v, then %v", before.Y, next.Y, after.Y)
	}
	if !onScreen(h.tt, zenBetween(29)) {
		t.Fatalf("the open line doesn't show the reply in between: %q", h.tt.Texts())
	}
}

// brightTicks are the turns, of 0 to n-1, whose ticks stand out from both
// ticks either side, sampled in each tick's middle: the pane behind them
// shades from top to bottom, but only so much from one tick to the next.
func brightTicks(h *harness, n int) []int {
	img := h.tt.Image()
	shade := map[int]float64{}
	for k := range n {
		if r, ok := h.tt.Find("Go to: " + turnText(k)); ok {
			if c := img.RGBAAt(int(r.X+6+markerW/2), int(r.Y+r.H/2)); c.A == 255 {
				shade[k] = float64(c.R) + float64(c.G) + float64(c.B)
			}
		}
	}
	var bright []int
	for k := range n {
		s, ok := shade[k]
		if !ok {
			continue
		}
		out, m := true, 0
		for _, j := range []int{k - 1, k + 1} {
			if t, ok := shade[j]; ok {
				out, m = out && math.Abs(s-t) > 60, m+1
			}
		}
		if out && m > 0 {
			bright = append(bright, k)
		}
	}
	return bright
}

// With Zen mode on, the marker of the turn whose message is at the top is
// the bright one, though the lines above put the message at another index
// in the list than in the rows.
func TestZenMarksTheTurnAtTheTop(t *testing.T) {
	h, th := zenThread(t, zenTurns(100), true, zenFinal(99))
	v := h.a.viewFor(th.id(), h.a.rowsOf(th))
	h.a.listFor(th.id()).ScrollTo(v.pos(5*40), ui.Start)
	h.tt.Move(800, 400)
	h.tt.Frame()
	h.tt.Advance(time.Second)
	if first, _ := h.a.listFor(th.id()).Visible(); v.anchor(first) != 5*40 || v.pos(5*40) == 5*40 {
		t.Fatalf("item %d at the top, want turn 40's message at item %d, not its row", first, v.pos(5*40))
	}
	if bright := brightTicks(h, 100); !slices.Equal(bright, []int{40}) {
		t.Errorf("the bright ticks are turns %v, want turn 40's alone", bright)
	}
}

// A thread gone takes with it what the app kept of it: its rows, markers
// and the timeline's views; the thread that shows keeps its own.
func TestPruneDropsAGoneThreadsTimeline(t *testing.T) {
	h, th := zenThread(t, zenTurns(3), true, zenFinal(2))
	gone := api.NewID()
	h.a.rows[gone] = &rowsCache{}
	h.a.markers[gone] = &markerState{}
	h.a.listed[gone] = &timelineMemo{}
	h.tt.Frame()
	if _, ok := h.a.rows[gone]; ok {
		t.Error("a gone thread's rows are kept")
	}
	if _, ok := h.a.markers[gone]; ok {
		t.Error("a gone thread's markers are kept")
	}
	if _, ok := h.a.listed[gone]; ok {
		t.Error("a gone thread's timeline views are kept")
	}
	if h.a.listed[th.id()] == nil || h.a.rows[th.id()] == nil {
		t.Error("the thread that shows lost its timeline")
	}
}
