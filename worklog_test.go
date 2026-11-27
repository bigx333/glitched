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
)

func TestWorkSummarySaysWhatTheStepsDid(t *testing.T) {
	sub := &client.Subagent{}
	for _, tc := range []struct {
		steps []client.WorkStep
		want  string
	}{
		{nil, "Worked"},
		{[]client.WorkStep{{Kind: "command"}, {Kind: "edit", Path: "a.go"}, {Kind: "command"}, {Kind: "search"}}, "Ran 2 commands · made 1 edit · searched 1 time"},
		{[]client.WorkStep{{Kind: "read", Path: "a"}, {Kind: "read", Path: "b"}, {Kind: "read", Path: "c"}, {Kind: "read", Path: "d"}, {Kind: "command"}}, "Read 4 files · ran 1 command"},
		// Repeated reads of the same file count once, while edits count operations
		// because multi-file patches carry only one Path.
		{[]client.WorkStep{{Kind: "read", Path: "a.go"}, {Kind: "read", Path: "a.go"}}, "Read 1 file"},
		{[]client.WorkStep{{Kind: "edit", Path: "a.go", Title: "Edit a.go, b.go"}}, "Made 1 edit"},
		{[]client.WorkStep{{Kind: "edit", Path: "a.go"}, {Kind: "edit", Path: "a.go"}, {Kind: "edit", Path: "a.go"}}, "Made 3 edits"},
		// Pathless reads (such as WebFetch) count as tools rather than files.
		{[]client.WorkStep{{Kind: "read", Title: "WebFetch https://a"}, {Kind: "read", Title: "WebFetch https://b"}}, "Used 2 tools"},
		{[]client.WorkStep{{Kind: "tool", Subagent: sub}, {Kind: "tool"}, {Kind: "url"}}, "Started 1 subagent · used 2 tools"},
	} {
		if got := workSummary(tc.steps); got != tc.want {
			t.Errorf("workSummary(%v) = %q, want %q", tc.steps, got, tc.want)
		}
	}
}

func TestClassifyWork(t *testing.T) {
	one, two := []client.WorkStep{{Kind: "command"}}, []client.WorkStep{{Kind: "command"}, {Kind: "edit"}}
	for _, tc := range []struct {
		e    entry
		act  workActivity
		open bool
		want workMode
	}{
		{entry{work: one}, workIdle, false, workInlineSingle},
		{entry{work: one}, workIdle, true, workInlineSingle},
		{entry{work: one, paused: "Waiting for approval"}, workPaused, false, workCollapsed},
		{entry{work: one, working: true}, workRunning, false, workCollapsed},
		{entry{work: one}, workThinking, false, workCollapsed},
		{entry{work: two, working: true}, workRunning, true, workExpanded},
		{entry{work: two}, workIdle, false, workCollapsed},
		{entry{work: two}, workIdle, true, workExpanded},
	} {
		if got := classifyWork(tc.e, tc.act, tc.open); got != tc.want {
			t.Errorf("classifyWork(%+v, %v, %v) = %v, want %v", tc.e, tc.act, tc.open, got, tc.want)
		}
	}
}

// A disclosure is as it starts until the user toggles it, and toggled
// back, it is forgotten.
func TestADisclosureRemembersOnlyWhatTheUserChanged(t *testing.T) {
	var m timelineMemo
	if m.disclosureOpen("w", false) || !m.disclosureOpen("w", true) {
		t.Fatal("an untouched disclosure isn't as it starts")
	}
	m.toggleDisclosure("w", false)
	if !m.disclosureOpen("w", false) || !m.disclosureOpen("w", true) {
		t.Fatal("opened, the disclosure isn't open")
	}
	m.toggleDisclosure("p", true)
	if m.disclosureOpen("p", true) {
		t.Fatal("closed, the disclosure is open")
	}
	m.toggleDisclosure("w", false)
	m.toggleDisclosure("p", true)
	if len(m.disclosures) != 0 || m.disclosureOpen("w", false) || !m.disclosureOpen("p", true) {
		t.Fatalf("toggled back, the disclosures are kept: %v", m.disclosures)
	}
}

// replaceEntries shows es as the thread's transcript.
func replaceEntries(h *harness, th thread, es []client.Entry) {
	fresh := *th.Transcript
	fresh.Entries = es
	th.Transcript.Replace(fresh)
	h.a.changed()
	h.tt.Frame()
}

// liveSteps are n steps of a run, the last at work.
func liveSteps(n int) []client.WorkStep {
	at := time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)
	exit := 0
	ws := make([]client.WorkStep, n)
	for i := range ws {
		ws[i] = client.WorkStep{Item: fmt.Sprintf("s%d", i), Kind: "command", Title: fmt.Sprintf("make step-%d", i), Status: api.WorkCompleted, Exit: &exit, Started: at, Ended: at}
	}
	ws[n-1].Status, ws[n-1].Exit = api.WorkStarted, nil
	return ws
}

// A live work log is one line of the step at work, which each new step
// changes where it is: it neither grows nor moves, nor does the thread,
// short or past the window, at its end.
func TestALiveWorkLogIsOneLineThatStaysPut(t *testing.T) {
	for _, tc := range []struct {
		name  string
		turns int
	}{{"short", 0}, {"overflowing", 15}} {
		t.Run(tc.name, func(t *testing.T) {
			run := api.NewID()
			before := append(zenTurns(tc.turns), &client.UserEntry{Message: api.NewID(), Text: "Build it.", State: client.Sent})
			with := func(n int) []client.Entry {
				return append(slices.Clone(before), &client.WorkEntry{Run: run, Items: liveSteps(n), Started: time.Now()})
			}
			h, th := zenThread(t, with(1), false, "make step-0")
			first, ok := h.tt.Find("make step-0")
			if !ok {
				t.Fatalf("the live step doesn't show: %q", h.tt.Texts())
			}
			for n := 2; n <= 6; n++ {
				replaceEntries(h, th, with(n))
				label := fmt.Sprintf("make step-%d", n-1)
				// Frame by frame, as the step comes, nothing glides.
				for range 12 {
					h.tt.Advance(16 * time.Millisecond)
					r, ok := h.tt.Find(label)
					if !ok {
						t.Fatalf("step %d doesn't show: %q", n, h.tt.Texts())
					}
					if r.Y != first.Y || r.H != first.H {
						t.Fatalf("with %d steps, the live line is at %v, was %v", n, r, first)
					}
				}
				if h.tt.HasText(fmt.Sprintf("make step-%d", n-2)) {
					t.Fatalf("a step done shows under the live line: %q", h.tt.Texts())
				}
				if !h.tt.HasText(plural(n, "step")) {
					t.Fatalf("the live line doesn't count %d steps: %q", n, h.tt.Texts())
				}
			}
			if tc.turns > 0 && !h.a.listFor(th.id()).AtEnd() {
				t.Error("the thread left its end as steps came")
			}
		})
	}
}

// head is the bounds of the button whose name starts with prefix: the
// innermost node so named, under the row around it.
func head(t *testing.T, tt *ui.Tester, prefix string) ui.Rect {
	t.Helper()
	var r ui.Rect
	for _, n := range tt.AccessAll() {
		if strings.HasPrefix(n.Name, prefix) && (r.H == 0 || n.Bounds.H < r.H) {
			r = n.Bounds
		}
	}
	if r.H == 0 {
		t.Fatalf("no %q: %q", prefix, tt.Texts())
	}
	return r
}

func clickRect(h *harness, r ui.Rect) {
	h.tt.ClickAt(r.X+r.W/2, r.Y+r.H/2)
	h.tt.Advance(time.Second)
}

// A settled log of several steps folds to a line of what it did, which a
// click opens and closes in place; the steps that failed or ran a
// subagent show under it folded. One step shows as it is, and a long
// log scrolls its steps.
func TestASettledWorkLogFoldsToWhatItDid(t *testing.T) {
	at := time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)
	exit0, exit1 := 0, 1
	run, one, long := api.NewID(), api.NewID(), api.NewID()
	many := make([]client.WorkStep, 30)
	for i := range many {
		many[i] = client.WorkStep{Item: fmt.Sprintf("m%d", i), Kind: "search", Title: fmt.Sprintf("Searched for thing %d", i), Status: api.WorkCompleted, Started: at, Ended: at}
	}
	es := []client.Entry{
		&client.UserEntry{Message: api.NewID(), Text: "Fix it.", State: client.Sent},
		&client.WorkEntry{Run: run, Started: at, Ended: at.Add(40 * time.Second), Items: []client.WorkStep{
			{Item: "a", Kind: "command", Title: "go build ./...", Status: api.WorkCompleted, Exit: &exit0, Started: at, Ended: at},
			{Item: "b", Kind: "edit", Title: "Edit main.go", Path: "main.go", Status: api.WorkCompleted, Started: at, Ended: at},
			{Item: "c", Kind: "command", Title: "go test ./...", Status: api.WorkCompleted, Exit: &exit1, Output: "--- FAIL: TestIt", Started: at, Ended: at},
			{Item: "d", Kind: "tool", Title: "Agent alpha", Status: api.WorkCompleted, Subagent: &client.Subagent{}, Started: at, Ended: at},
		}},
		&client.AssistantEntry{Run: run, Text: "One test fails."},
		&client.UserEntry{Message: api.NewID(), Text: "Read the notes.", State: client.Sent},
		&client.WorkEntry{Run: one, Started: at, Ended: at.Add(time.Second), Items: []client.WorkStep{{Item: "r", Kind: "read", Title: "Read notes.txt", Path: "notes.txt", Status: api.WorkCompleted, Started: at, Ended: at}}},
		&client.AssistantEntry{Run: one, Text: "Read them."},
		&client.UserEntry{Message: api.NewID(), Text: "Look everywhere.", State: client.Sent},
		&client.WorkEntry{Run: long, Started: at, Ended: at.Add(time.Minute), Items: many},
		&client.AssistantEntry{Run: long, Text: "Looked everywhere."},
	}
	h, _ := zenThread(t, es, false, "Looked everywhere.")
	const summary = "Ran 2 commands · made 1 edit · started 1 subagent"
	for _, s := range []string{summary, "1 failed", "40s", "ERR 1", "go test ./...", "Agent alpha", "notes.txt", "Searched 30 times"} {
		if !h.tt.HasText(s) {
			t.Errorf("folded, the thread doesn't show %q: %q", s, h.tt.Texts())
		}
	}
	for _, s := range []string{"Edit main.go", "go build ./...", "1 step", "Searched for thing 0", "--- FAIL: TestIt"} {
		if h.tt.HasText(s) {
			t.Errorf("folded, the thread shows %q", s)
		}
	}

	top := head(t, h.tt, summary)
	clickRect(h, top)
	if !h.tt.HasText("Edit main.go") || !h.tt.HasText("go build ./...") {
		t.Fatalf("a click didn't open the log: %q", h.tt.Texts())
	}
	if h.tt.HasText("--- FAIL: TestIt") {
		t.Fatalf("opening the log expanded the failed command's output before a click")
	}
	// At the end, opening it follows the end: its head goes up by as much
	// as the thread now overflows, and stays in view.
	l := h.a.listFor(h.a.current().id())
	if r := head(t, h.tt, summary); r.Y > top.Y || r.Y < 0 || !l.AtEnd() {
		t.Errorf("opening it moved its head from %v to %v, at end %v", top.Y, r.Y, l.AtEnd())
	}
	clickRect(h, head(t, h.tt, summary))
	if h.tt.HasText("Edit main.go") || !h.tt.HasText("go test ./...") || !h.tt.HasText("ERR 1") || h.tt.HasText("--- FAIL: TestIt") {
		t.Fatalf("a second click didn't close the log cleanly: %q", h.tt.Texts())
	}
	if r := head(t, h.tt, summary); r.Y != top.Y || !l.AtEnd() {
		t.Errorf("closing it moved its head from %v to %v, at end %v", top.Y, r.Y, l.AtEnd())
	}

	// Open, the long log's steps scroll past workScrollHeight.
	clickRect(h, head(t, h.tt, "Searched 30 times"))
	lh := head(t, h.tt, "Searched 30 times")
	reply, ok := h.tt.Find("Looked everywhere.")
	if !ok || !h.tt.HasText("Searched for thing 0") {
		t.Fatalf("the long log didn't open: %q", h.tt.Texts())
	}
	// Under the head, the steps, the card's border and the gap to the reply.
	if steps := reply.Y - (lh.Y + lh.H); steps > workScrollHeight+24 || steps < workScrollHeight {
		t.Errorf("the long log's steps take %v, want %v and the gap", steps, workScrollHeight)
	}
}

// A plan whose steps are all done folds to its head, which a click opens
// in place; a plan with steps to go starts open.
func TestADonePlanFoldsToItsHead(t *testing.T) {
	done, going := api.NewID(), api.NewID()
	es := []client.Entry{
		&client.UserEntry{Message: api.NewID(), Text: "Plan it.", State: client.Sent},
		&client.PlanEntry{Run: done, Steps: []api.PlanStep{{Text: "Read the code", Status: api.PlanDone}, {Text: "Fix the bug", Status: api.PlanDone}, {Text: "Run the tests", Status: api.PlanDone}}},
		&client.AssistantEntry{Run: done, Text: "All done."},
		&client.UserEntry{Message: api.NewID(), Text: "Ship it.", State: client.Sent},
		&client.PlanEntry{Run: going, Steps: []api.PlanStep{{Text: "Tag the release", Status: api.PlanDone}, {Text: "Publish it", Status: api.PlanPending}}},
		&client.AssistantEntry{Run: going, Text: "Tagged."},
	}
	h, _ := zenThread(t, es, false, "Tagged.")
	if !h.tt.HasText("3 of 3 done") || h.tt.HasText("Read the code") {
		t.Fatalf("the done plan isn't folded to its head: %q", h.tt.Texts())
	}
	if !h.tt.HasText("1 of 2 done") || !h.tt.HasText("Publish it") {
		t.Fatalf("the plan with steps to go isn't open: %q", h.tt.Texts())
	}
	top := head(t, h.tt, "Checklist 3 of 3 done")
	clickRect(h, top)
	if !h.tt.HasText("Read the code") || !h.tt.HasText("Run the tests") {
		t.Fatalf("a click didn't open the done plan: %q", h.tt.Texts())
	}
	if r := head(t, h.tt, "Checklist 3 of 3 done"); r.Y != top.Y {
		t.Errorf("opening it moved its head from %v to %v", top.Y, r.Y)
	}
	clickRect(h, head(t, h.tt, "Checklist 3 of 3 done"))
	if h.tt.HasText("Read the code") {
		t.Error("a second click didn't fold the done plan")
	}
}

// Completing one command while the run keeps working before starting the
// next stays on the same single live line so the conversation never jumps.
func TestSequentialCommandsKeepTheConversationStill(t *testing.T) {
	at := time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)
	exit0 := 0
	run := api.NewID()
	before := append(zenTurns(15), &client.UserEntry{Message: run, Text: "Run two commands.", State: client.Sent})
	entryWith := func(items []client.WorkStep, ended time.Time) []client.Entry {
		return append(slices.Clone(before), &client.WorkEntry{Run: run, Items: items, Started: at, Ended: ended})
	}
	step1Running := []client.WorkStep{{Item: "c1", Kind: "command", Title: "go vet ./...", Status: api.WorkStarted, Started: at}}
	step1Done := []client.WorkStep{{Item: "c1", Kind: "command", Title: "go vet ./...", Status: api.WorkCompleted, Exit: &exit0, Started: at, Ended: at.Add(time.Second)}}
	step2Running := append(slices.Clone(step1Done), client.WorkStep{Item: "c2", Kind: "command", Title: "go test ./...", Status: api.WorkStarted, Started: at.Add(2 * time.Second)})

	h, th := zenThread(t, entryWith(step1Running, time.Time{}), false, "go vet ./...")
	th.View.Run = api.RunRunning
	th.View.Turn = &api.Turn{Run: run}
	h.a.changed()
	h.tt.Frame()

	msg0, ok := h.tt.Find("Run two commands.")
	if !ok {
		t.Fatalf("prompt missing: %q", h.tt.Texts())
	}
	line0, ok := h.tt.Find("go vet ./...")
	if !ok {
		t.Fatalf("live step missing: %q", h.tt.Texts())
	}

	// Command 1 finishes; the model thinks before starting command 2.
	replaceEntries(h, th, entryWith(step1Done, at.Add(time.Second)))
	h.tt.Advance(100 * time.Millisecond)
	if !h.tt.HasText("Thinking…") || !h.tt.HasText("go vet ./...") {
		t.Fatalf("between sequential steps, live line should show Thinking… and last step: %q", h.tt.Texts())
	}
	if msg1, ok := h.tt.Find("Run two commands."); !ok || msg1.Y != msg0.Y {
		t.Fatalf("prompt moved from %v to %v between commands", msg0.Y, msg1.Y)
	}
	if line1, ok := h.tt.Find("Thinking… · go vet ./..."); !ok || line1.Y != line0.Y || line1.H != line0.H {
		t.Fatalf("live line moved from %v to %v between commands", line0, line1)
	}

	// Command 2 starts.
	replaceEntries(h, th, entryWith(step2Running, time.Time{}))
	h.tt.Advance(100 * time.Millisecond)
	if msg2, ok := h.tt.Find("Run two commands."); !ok || msg2.Y != msg0.Y {
		t.Fatalf("prompt moved from %v to %v when command 2 started", msg0.Y, msg2.Y)
	}
	if line2, ok := h.tt.Find("go test ./..."); !ok || line2.Y != line0.Y || line2.H != line0.H {
		t.Fatalf("live line moved from %v to %v when command 2 started", line0, line2)
	}
}

// A very long live command title stays clipped inside its box and never
// paints over the step count on the right.
func TestLongLiveTitleStaysClearOfStepCount(t *testing.T) {
	at := time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)
	exit0 := 0
	run := api.NewID()
	longTitle := "go test -v -count=1 " + strings.Repeat("very-long-package-name/", 12)
	es := []client.Entry{
		&client.UserEntry{Message: run, Text: "Test it.", State: client.Sent},
		&client.WorkEntry{Run: run, Started: at, Items: []client.WorkStep{
			{Item: "c1", Kind: "command", Title: "go vet .", Status: api.WorkCompleted, Exit: &exit0, Started: at, Ended: at},
			{Item: "c2", Kind: "command", Title: longTitle, Status: api.WorkStarted, Started: at},
		}},
	}
	h, _ := zenThread(t, es, false, "2 steps")
	titleBox, ok := h.tt.Find(longTitle)
	if !ok {
		t.Fatalf("long live title missing: %q", h.tt.Texts())
	}
	stepsBox, ok := h.tt.Find("2 steps")
	if !ok {
		t.Fatalf("step count missing: %q", h.tt.Texts())
	}
	if titleBox.W <= 0 || titleBox.X+titleBox.W > stepsBox.X {
		t.Fatalf("live title box %v overlaps step count %v", titleBox, stepsBox)
	}
	// Render a reference frame with a short title so the pixels inside
	// stepsBox must match identically (proving the long title did not bleed).
	longImg := h.tt.Image()
	es[1].(*client.WorkEntry).Items[1].Title = "go test ."
	replaceEntries(h, h.a.current(), es)
	h.tt.Advance(100 * time.Millisecond)
	shortImg := h.tt.Image()
	for y := int(stepsBox.Y); y < int(stepsBox.Y+stepsBox.H); y++ {
		for x := int(stepsBox.X); x < int(stepsBox.X+stepsBox.W); x++ {
			if longImg.At(x, y) != shortImg.At(x, y) {
				t.Fatalf("long live title painted into step count at (%d,%d)", x, y)
			}
		}
	}
}

// Keyboard users can Tab into an expanded long work log and scroll to its
// last command.
func TestExpandedLongWorkLogScrollsByKeyboard(t *testing.T) {
	at := time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)
	exit0 := 0
	run := api.NewID()
	steps := make([]client.WorkStep, 50)
	for i := range steps {
		steps[i] = client.WorkStep{Item: fmt.Sprintf("c%d", i), Kind: "command", Title: fmt.Sprintf("make step-%d", i), Status: api.WorkCompleted, Exit: &exit0, Started: at, Ended: at}
	}
	es := []client.Entry{
		&client.UserEntry{Message: api.NewID(), Text: "Build everything.", State: client.Sent},
		&client.WorkEntry{Run: run, Started: at, Ended: at.Add(time.Minute), Items: steps},
		&client.AssistantEntry{Run: run, Text: "Built."},
	}
	h, _ := zenThread(t, es, false, "Built.")
	clickRect(h, head(t, h.tt, "Ran 50 commands"))
	if h.tt.HasText("All 50") {
		t.Errorf("a log of one kind shows filter tabs: %q", h.tt.Texts())
	}
	// Focus the inner scroll container via Tab from the header.
	h.tt.Key(0, ui.KeyTab)
	if !h.tt.Focused("Work steps") {
		t.Fatalf("Tab from open work log header did not focus Work steps")
	}
	scrollBox, ok := h.tt.Find("Work steps")
	if !ok {
		t.Fatal("Work steps scroll container missing")
	}
	h.tt.Key(0, ui.KeyEnd)
	h.tt.Advance(100 * time.Millisecond)
	last, ok := h.tt.Find("make step-49")
	if !ok || last.Y+last.H > scrollBox.Y+scrollBox.H+2 || last.Y < scrollBox.Y-2 {
		t.Fatalf("KeyEnd did not bring make step-49 (%v) into Work steps (%v)", last, scrollBox)
	}
}

// An expanded live work log follows newly appended steps and failure
// output growth when already at its bottom.
func TestExpandedLiveWorkLogFollowsNewSteps(t *testing.T) {
	run := api.NewID()
	base := []client.Entry{&client.UserEntry{Message: run, Text: "Build many.", State: client.Sent}}
	with := func(steps []client.WorkStep) []client.Entry {
		return append(slices.Clone(base), &client.WorkEntry{Run: run, Items: steps, Started: time.Now()})
	}
	// The box is found again each time: a first failure adds its filter
	// tab, and the bar of tabs moves the steps down.
	inScroll := func(h *harness, _ ui.Rect, name string) (ui.Rect, bool) {
		scrollBox, ok := h.tt.Find("Work steps")
		if !ok {
			return ui.Rect{}, false
		}
		for _, n := range h.tt.AccessAll() {
			if n.Name == name && !n.Offscreen && n.Bounds.Y >= scrollBox.Y-2 && n.Bounds.Y+n.Bounds.H <= scrollBox.Y+scrollBox.H+2 {
				return n.Bounds, true
			}
		}
		return ui.Rect{}, false
	}

	h, th := zenThread(t, with(liveSteps(25)), false, "25 steps")
	clickRect(h, head(t, h.tt, "make step-24"))
	scrollBox, ok := h.tt.Find("Work steps")
	if !ok {
		t.Fatal("Work steps scroll container missing")
	}
	// Newly opened live log starts at its end showing step 24 inside scrollBox.
	if _, ok := inScroll(h, scrollBox, "make step-24"); !ok {
		t.Fatalf("expanded live log did not start at end inside Work steps (%v)", scrollBox)
	}
	// Command 24 fails with multi-line output (default collapsed); once
	// opened, further output growth still follows the bottom without step
	// count changing, and steps 25..39 follow after it.
	steps25Fail := liveSteps(25)
	exit1 := 1
	steps25Fail[24].Status, steps25Fail[24].Exit = api.WorkFailed, &exit1
	steps25Fail[24].Output = "error line\nexit status 1"
	th.View.Run = api.RunRunning
	th.View.Turn = &api.Turn{Run: run}
	replaceEntries(h, th, with(steps25Fail))
	h.tt.Advance(100 * time.Millisecond)
	if h.tt.HasText(steps25Fail[24].Output) {
		t.Fatalf("failed step output expanded before a click")
	}
	if _, ok := inScroll(h, scrollBox, "make step-24"); !ok {
		t.Fatalf("expanded live log did not keep failed step in view inside Work steps (%v)", scrollBox)
	}
	clickRect(h, head(t, h.tt, "make step-24"))
	steps25Fail[24].Output = strings.Repeat("error line\n", 8) + "exit status 1"
	replaceEntries(h, th, with(steps25Fail))
	h.tt.Advance(100 * time.Millisecond)
	if _, ok := inScroll(h, scrollBox, steps25Fail[24].Output); !ok {
		t.Fatalf("expanded live log did not follow failure output growth inside Work steps (%v)", scrollBox)
	}
	steps40 := liveSteps(40)
	steps40[24] = steps25Fail[24]
	replaceEntries(h, th, with(steps40))
	h.tt.Advance(100 * time.Millisecond)
	if _, ok := inScroll(h, scrollBox, "make step-39"); !ok {
		t.Fatalf("expanded live log did not follow appended steps after failure inside Work steps (%v)", scrollBox)
	}
}

// A multiline command title (as Codex emits) flattens line breaks in the
// live shimmer box so it never paints into adjacent rows.
func TestMultilineLiveTitleStaysInsideOneLine(t *testing.T) {
	at := time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)
	run := api.NewID()
	multi := "echo line1\necho line2\necho line3\necho line4"
	es := []client.Entry{
		&client.UserEntry{Message: run, Text: "Run multiline.", State: client.Sent},
		&client.WorkEntry{Run: run, Started: at, Items: []client.WorkStep{
			{Item: "c1", Kind: "command", Title: multi, Status: api.WorkStarted, Started: at},
		}},
	}
	h, _ := zenThread(t, es, false, "echo line1")
	promptBox, ok := h.tt.Find("Run multiline.")
	if !ok {
		t.Fatalf("prompt missing: %q", h.tt.Texts())
	}
	titleBox, ok := h.tt.Find(multi)
	if !ok {
		t.Fatalf("multiline title missing: %q", h.tt.Texts())
	}
	if titleBox.H > 20 {
		t.Fatalf("multiline live title grew vertically to %v", titleBox.H)
	}
	// Compare pixels inside the prompt row above against a single-line title.
	multiImg := h.tt.Image()
	es[1].(*client.WorkEntry).Items[0].Title = "echo ok"
	replaceEntries(h, h.a.current(), es)
	h.tt.Advance(100 * time.Millisecond)
	singleImg := h.tt.Image()
	for y := int(promptBox.Y); y < int(promptBox.Y+promptBox.H); y++ {
		for x := int(promptBox.X); x < int(promptBox.X+promptBox.W); x++ {
			if multiImg.At(x, y) != singleImg.At(x, y) {
				t.Fatalf("multiline live title painted into prompt row at (%d,%d)", x, y)
			}
		}
	}
}

// Inside an open Zen fold, a single-step settled work log renders inline
// directly rather than showing a toggle that removes itself when clicked.
func TestSingleStepLogInZenModeRendersInline(t *testing.T) {
	at := time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)
	run := api.NewID()
	es := []client.Entry{
		&client.UserEntry{Message: api.NewID(), Text: "Read it.", State: client.Sent},
		&client.WorkEntry{Run: run, Started: at, Ended: at.Add(5 * time.Second), Items: []client.WorkStep{
			{Item: "r1", Kind: "read", Title: "Read notes.txt", Path: "notes.txt", Status: api.WorkCompleted, Started: at, Ended: at.Add(5 * time.Second)},
		}},
		&client.AssistantEntry{Run: run, Text: "All read."},
	}
	h, _ := zenThread(t, es, true, "All read.")
	line := zenLineText(h.tt)
	if line == "" {
		t.Fatalf("no Zen line: %q", h.tt.Texts())
	}
	h.click(line)
	h.tt.Advance(time.Second)
	if !h.tt.HasText("notes.txt") {
		t.Fatalf("single step did not render inside open Zen line: %q", h.tt.Texts())
	}
	if h.tt.HasText("Read 1 file") {
		t.Fatalf("single-step work log inside open Zen line showed a collapsible card header: %q", h.tt.Texts())
	}
}

func TestStepKindOfFollowsTheSummary(t *testing.T) {
	for _, tc := range []struct {
		w    client.WorkStep
		want workFilter
	}{
		{client.WorkStep{Kind: "command"}, workFilterCommand},
		{client.WorkStep{Kind: "read", Path: "a.go"}, workFilterRead},
		{client.WorkStep{Kind: "read", Title: "WebFetch https://a"}, workFilterTool},
		{client.WorkStep{Kind: "edit", Path: "a.go"}, workFilterEdit},
		{client.WorkStep{Kind: "search"}, workFilterSearch},
		{client.WorkStep{Kind: "tool", Subagent: &client.Subagent{}}, workFilterSubagent},
		{client.WorkStep{Kind: "command", Subagent: &client.Subagent{}}, workFilterSubagent},
		{client.WorkStep{Kind: "tool"}, workFilterTool},
		{client.WorkStep{Kind: "url"}, workFilterTool},
	} {
		if got := stepKindOf(tc.w); got != tc.want {
			t.Errorf("stepKindOf(%+v) = %v, want %v", tc.w, got, tc.want)
		}
	}
}

// timedStep is a finished step of kind that took d, exiting with exit
// when it is not nil.
func timedStep(item, kind string, d time.Duration, exit *int) client.WorkStep {
	at := time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)
	return client.WorkStep{Item: item, Kind: kind, Title: item, Path: map[bool]string{true: item}[kind == "read" || kind == "edit"],
		Status: api.WorkCompleted, Exit: exit, Started: at, Ended: at.Add(d)}
}

func TestWorkLedgerStats(t *testing.T) {
	exit0, exit1 := 0, 1
	tabs := func(lg workLedger) []string {
		var out []string
		for _, tb := range lg.tabs {
			out = append(out, tb.text())
		}
		return out
	}

	// Quick commands measure against the floor, so none draws full.
	quick := workLedgerStats([]client.WorkStep{timedStep("a", "command", 100*time.Millisecond, &exit0), timedStep("b", "command", 300*time.Millisecond, &exit0)})
	if quick.maxDur != minLedgerDur {
		t.Errorf("quick commands measure against %v, want the %v floor", quick.maxDur, minLedgerDur)
	}
	if got := durationFill(300*time.Millisecond, quick.maxDur); got >= durationBarWidth/2 {
		t.Errorf("a 300ms command among quick ones fills %v of %v", got, durationBarWidth)
	}
	// One kind of step with no subset to filter has only All (no bar).
	if got := tabs(quick); !slices.Equal(got, []string{"All 2"}) {
		t.Errorf("the tabs of one kind are %q", got)
	}
	// One kind with a partial failure shows All and Failed without a
	// duplicate Cmd tab; when every step fails, Failed duplicates All too.
	partial := workLedgerStats([]client.WorkStep{timedStep("a", "command", time.Second, &exit0), timedStep("b", "command", time.Second, &exit1)})
	if got := tabs(partial); !slices.Equal(got, []string{"All 2", "Failed 1"}) {
		t.Errorf("the tabs of one kind with one failure are %q", got)
	}
	allFailed := workLedgerStats([]client.WorkStep{timedStep("a", "command", time.Second, &exit1), timedStep("b", "command", time.Second, &exit1)})
	if got := tabs(allFailed); !slices.Equal(got, []string{"All 2"}) {
		t.Errorf("the tabs when every step failed are %q", got)
	}

	// Mixed steps: kinds in the order each first came, then the failed.
	mixed := workLedgerStats([]client.WorkStep{
		timedStep("go build", "command", 2*time.Second, &exit0),
		timedStep("a.go", "read", 0, nil),
		timedStep("a.go", "edit", 0, nil),
		timedStep("b.go", "read", 0, nil),
		timedStep("go test", "command", 10*time.Second, &exit1),
	})
	if mixed.maxDur != 10*time.Second {
		t.Errorf("mixed steps measure against %v, want the longest, 10s", mixed.maxDur)
	}
	if got := tabs(mixed); !slices.Equal(got, []string{"All 5", "Cmd 2", "Read 2", "Edit 1", "Failed 1"}) {
		t.Errorf("the tabs of mixed steps are %q", got)
	}
	// A running step has no duration yet.
	running := timedStep("go vet", "command", time.Minute, nil)
	running.Status = api.WorkStarted
	if got := workLedgerStats([]client.WorkStep{running}).maxDur; got != minLedgerDur {
		t.Errorf("a running step measures %v", got)
	}

	for _, tc := range []struct {
		d, max time.Duration
		want   float32
		slow   bool
	}{
		{0, minLedgerDur, 3, false},
		{750 * time.Millisecond, minLedgerDur, 14, false},
		{minLedgerDur, minLedgerDur, 28, false},
		{20 * time.Second, 10 * time.Second, 28, true},
		{7 * time.Second, 10 * time.Second, 19.6, true},
		{5 * time.Second, 10 * time.Second, 14, false},
		{1900 * time.Millisecond, 2 * time.Second, 26.6, false},
	} {
		if got := durationFill(tc.d, tc.max); math.Abs(float64(got-tc.want)) > 0.01 {
			t.Errorf("durationFill(%v, %v) = %v, want %v", tc.d, tc.max, got, tc.want)
		}
		if got := slowStep(tc.d, tc.max); got != tc.slow {
			t.Errorf("slowStep(%v, %v) = %v, want %v", tc.d, tc.max, got, tc.slow)
		}
	}
}

// The breakdown bar draws runs of a kind in order, and with too many
// runs, a run a kind.
func TestWorkSparkCapsItsRuns(t *testing.T) {
	exit1 := 1
	steps := []client.WorkStep{{Kind: "command"}, {Kind: "command"}, {Kind: "edit"}, {Kind: "command", Exit: &exit1}, {Kind: "command"}}
	if got, want := workSpark(steps), []sparkRun{{workFilterCommand, 2}, {workFilterEdit, 1}, {workFilterFailed, 1}, {workFilterCommand, 1}}; !slices.Equal(got, want) {
		t.Errorf("workSpark = %v, want %v", got, want)
	}
	var many []client.WorkStep
	for range 100 {
		many = append(many, client.WorkStep{Kind: "command"}, client.WorkStep{Kind: "read", Path: "a"})
	}
	if got, want := workSpark(many), []sparkRun{{workFilterCommand, 100}, {workFilterRead, 100}}; !slices.Equal(got, want) {
		t.Errorf("workSpark of 200 alternating steps = %v, want %v", got, want)
	}
}

// ledger is a thread whose one settled work log of steps is open.
func ledger(t *testing.T, steps []client.WorkStep, summary string) *harness {
	t.Helper()
	at := time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)
	run := api.NewID()
	es := []client.Entry{
		&client.UserEntry{Message: api.NewID(), Text: "Look into it.", State: client.Sent},
		&client.WorkEntry{Run: run, Started: at, Ended: at.Add(time.Minute), Items: steps},
		&client.AssistantEntry{Run: run, Text: "Looked."},
	}
	h, _ := zenThread(t, es, false, "Looked.")
	clickRect(h, head(t, h.tt, summary))
	return h
}

// A long command keeps how long it took on one line, clear of the
// command, which gives way instead (the right column used to wrap under
// a long rg).
func TestALongCommandKeepsItsTelemetryOnOneLine(t *testing.T) {
	exit0 := 0
	long := `rg -n "Unread\(|markRead|unreadCount|lastReadAt|isUnread|readState" apps/web/src apps/server/src packages/contracts/src --glob '!*.test.ts' 2>/dev/null`
	cmd := timedStep(long, "command", 3200*time.Millisecond, &exit0)
	short := timedStep("go vet .", "command", 3400*time.Millisecond, &exit0)
	h := ledger(t, []client.WorkStep{cmd, short, timedStep("main.go", "edit", 0, nil)}, "Ran 2 commands")
	took, ok := h.tt.Find("3.2s")
	if !ok {
		t.Fatalf("no duration for the long command: %q", h.tt.Texts())
	}
	if took.H > 18 {
		t.Errorf("the duration wrapped to %v high", took.H)
	}
	// It keeps the room it takes beside a short command.
	if ref, ok := h.tt.Find("3.4s"); !ok || math.Abs(float64(took.W-ref.W)) > 0.5 || math.Abs(float64(took.X-ref.X)) > 0.5 {
		t.Errorf("beside the long command the duration is %v, beside a short one %v", took, ref)
	}
	_, args, _ := strings.Cut(long, " ")
	text, ok := h.tt.Find(args)
	if !ok {
		t.Fatalf("no arguments of the long command: %q", h.tt.Texts())
	}
	if text.H > 18 {
		t.Errorf("the command wrapped to %v high", text.H)
	}
	// The bar and its gap sit between the command and its duration.
	if right := text.X + text.W; right > took.X-durationBarWidth-6+0.5 {
		t.Errorf("the command ends at %v, over its duration at %v", right, took.X)
	}
	if mid, tmid := text.Y+text.H/2, took.Y+took.H/2; math.Abs(float64(mid-tmid)) > 2 {
		t.Errorf("the duration sits at %v, off the command's line at %v", tmid, mid)
	}
}

// An open log of mixed steps filters them by kind, back to all with All,
// and opened again, it shows all. A log of one kind has nothing to filter.
func TestWorkLogFiltersByKind(t *testing.T) {
	exit0, exit1 := 0, 1
	steps := []client.WorkStep{
		timedStep("go build ./...", "command", 2*time.Second, &exit0),
		timedStep("a.go", "read", 0, nil),
		timedStep("main.go", "edit", 0, nil),
		timedStep("b.go", "read", 0, nil),
		timedStep("go test ./...", "command", 4*time.Second, &exit1),
	}
	steps[1].Title, steps[2].Title, steps[3].Title = "Read a.go", "Edit main.go", "Read b.go"
	const summary = "Ran 2 commands · read 2 files · made 1 edit"
	h := ledger(t, steps, summary)
	for _, s := range []string{"All 5", "Cmd 2", "Read 2", "Edit 1", "Failed 1"} {
		if !h.tt.HasText(s) {
			t.Errorf("no tab %q: %q", s, h.tt.Texts())
		}
	}
	shows := func(want ...string) {
		t.Helper()
		for _, s := range []string{"go build ./...", "Read a.go", "Edit main.go", "Read b.go", "go test ./..."} {
			if h.tt.HasText(s) != slices.Contains(want, s) {
				t.Errorf("showing %q is %v, want %v: %q", s, h.tt.HasText(s), slices.Contains(want, s), h.tt.Texts())
			}
		}
	}
	tabSelected := func(want string) {
		t.Helper()
		for _, name := range []string{"All 5", "Cmd 2", "Read 2", "Edit 1", "Failed 1"} {
			found, sel := false, false
			for _, n := range h.tt.AccessAll() {
				if n.Role == ui.RoleTab && n.Name == name {
					found, sel = true, n.Selected
					break
				}
			}
			if !found {
				t.Fatalf("no RoleTab %q", name)
			}
			if sel != (name == want) {
				t.Errorf("tab %q Selected = %v, want %v", name, sel, name == want)
			}
		}
	}
	tabSelected("All 5")
	// From the header, Tab enters the filter tablist once and the next Tab
	// reaches Work steps.
	h.tt.Key(0, ui.KeyTab)
	if !h.tt.Focused("All 5") {
		t.Fatalf("Tab from mixed log header did not focus active filter tab")
	}
	h.tt.Key(0, ui.KeyTab)
	if !h.tt.Focused("Work steps") {
		t.Fatalf("second Tab from mixed log header did not focus Work steps")
	}

	h.click("Read 2")
	h.tt.Advance(time.Second)
	tabSelected("Read 2")
	shows("Read a.go", "Read b.go")
	h.click("Failed 1")
	h.tt.Advance(time.Second)
	tabSelected("Failed 1")
	shows("go test ./...")
	h.click("All 5")
	h.tt.Advance(time.Second)
	tabSelected("All 5")
	shows("go build ./...", "Read a.go", "Edit main.go", "Read b.go", "go test ./...")

	h.click("Edit 1")
	h.tt.Advance(time.Second)
	shows("Edit main.go")
	clickRect(h, head(t, h.tt, summary))
	clickRect(h, head(t, h.tt, summary))
	shows("go build ./...", "Read a.go", "Edit main.go", "Read b.go", "go test ./...")
}

// Switching filters at the end of a long thread keeps the end, and the
// full composer, in view; scrolled up, it keeps the work log's header in
// place under the pointer.
func TestSwitchingWorkFiltersKeepsHeaderInPlace(t *testing.T) {
	at := time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)
	exit0 := 0
	run := api.NewID()
	steps := make([]client.WorkStep, 22)
	for i := range 21 {
		steps[i] = timedStep(fmt.Sprintf("make step-%d", i), "command", time.Second, &exit0)
		steps[i].Item = fmt.Sprintf("c%d", i)
	}
	steps[21] = timedStep("main.go", "edit", 0, nil)
	steps[21].Item, steps[21].Title = "e1", "Edit main.go"
	es := append(zenTurns(15),
		&client.UserEntry{Message: api.NewID(), Text: "Build it.", State: client.Sent},
		&client.WorkEntry{Run: run, Started: at, Ended: at.Add(time.Minute), Items: steps},
		&client.AssistantEntry{Run: run, Text: "Built."},
	)
	const summary = "Ran 21 commands · made 1 edit"
	h, _ := zenThread(t, es, false, "Built.")
	l := h.a.listFor(h.a.current().id())
	clickRect(h, head(t, h.tt, summary))
	h.click("Edit 1")
	atEndAndFull(t, h, l, "filtering at the end")
	h.click("All 22")
	atEndAndFull(t, h, l, "unfiltering at the end")
	h.click("Edit 1")
	atEndAndFull(t, h, l, "filtering at the end again")

	// Scrolled up, the steps showing all of them grow under the header.
	wheel(h, -120)
	h.tt.Advance(time.Second)
	if l.AtEnd() {
		t.Fatal("scrolling up left the list at its end")
	}
	before := head(t, h.tt, summary)
	h.click("All 22")
	h.tt.Advance(time.Second)
	after := head(t, h.tt, summary)
	if math.Abs(float64(after.Y-before.Y)) > 1 || l.AtEnd() {
		t.Errorf("scrolled up, switching filter moved header from Y=%v to Y=%v (at end %v)", before.Y, after.Y, l.AtEnd())
	}
}

// A click opens a command to read in full, and a second closes it.
func TestACommandOpensToReadInFull(t *testing.T) {
	exit0 := 0
	long := "go test -run 'TestWork|TestASettled|TestALive|TestLongLive|TestExpanded|TestMultiline' -count=1 ./internal/... ./cmd/... ."
	h := ledger(t, []client.WorkStep{timedStep(long, "command", time.Second, &exit0), timedStep("main.go", "edit", 0, nil)}, "Ran 1 command")
	drawerText := func() (ui.Rect, bool) {
		for _, n := range h.tt.AccessAll() {
			if n.Role == ui.RoleText && n.Name == long {
				return n.Bounds, true
			}
		}
		return ui.Rect{}, false
	}
	if _, ok := drawerText(); ok {
		t.Fatal("the command drawer shows before a click")
	}
	h.click(long)
	h.tt.Advance(time.Second)
	full, ok := drawerText()
	if !ok {
		t.Fatalf("a click didn't open the command: %q", h.tt.Texts())
	}
	if row, _ := h.tt.Find(long); full.H <= row.H/2 {
		t.Errorf("the open command is %v high, under its row's %v", full.H, row.H)
	}
	h.click(long)
	h.tt.Advance(time.Second)
	if _, ok := drawerText(); ok {
		t.Error("a second click didn't close the command")
	}
}

func TestSplitCommandPreservesQuotedSpaces(t *testing.T) {
	for _, tc := range []struct {
		in, bin, args string
	}{
		{`rg 'a  b'`, "rg", `'a  b'`},
		{"echo line1\necho  line2", "echo", "line1 echo  line2"},
		{"/usr/local/bin/check", "/usr/local/bin/check", ""},
		{"  go   test -run 'a  b'", "go", "test -run 'a  b'"},
	} {
		bin, args := splitCommand(tc.in)
		if bin != tc.bin || args != tc.args {
			t.Errorf("splitCommand(%q) = (%q, %q), want (%q, %q)", tc.in, bin, args, tc.bin, tc.args)
		}
	}
}

// longWorkLogThread is a thread long enough to scroll whose last turn ran
// a settled log of commands, folded, under many turns before it.
func longWorkLogThread(t *testing.T) *harness {
	t.Helper()
	at := time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)
	exit0 := 0
	run := api.NewID()
	es := synthetic(40)
	es = append(es,
		&client.UserEntry{Message: api.NewID(), Text: "Build and test it.", State: client.Sent},
		&client.WorkEntry{Run: run, Started: at, Ended: at.Add(40 * time.Second), Items: []client.WorkStep{
			{Item: "a", Kind: "command", Title: "go build ./...", Status: api.WorkCompleted, Exit: &exit0, Output: "built fine", Started: at, Ended: at},
			{Item: "b", Kind: "command", Title: "go vet ./...", Status: api.WorkCompleted, Exit: &exit0, Started: at, Ended: at},
			{Item: "c", Kind: "command", Title: "go test ./...", Status: api.WorkCompleted, Exit: &exit0, Started: at, Ended: at},
		}},
		&client.AssistantEntry{Run: run, Text: "Built and tested."},
	)
	h, _ := zenThread(t, es, false, "Built and tested.")
	return h
}

// atEndAndFull fails unless the list shows its end, the composer is full
// and Jump to latest is gone, frame by frame for a second.
func atEndAndFull(t *testing.T, h *harness, l *ui.ListState, what string) {
	t.Helper()
	for range 60 {
		h.tt.Advance(16 * time.Millisecond)
		if !l.AtEnd() || h.a.composerK != 1 || h.a.jumpK != 0 {
			t.Fatalf("%s: at end %v, composer k %v, jump %v; want true, 1, 0", what, l.AtEnd(), h.a.composerK, h.a.jumpK)
		}
	}
}

// At the end of a thread that scrolls, opening or closing a work log,
// a step's drawer in it, or its filter keeps the end in view, so the
// composer stays full.
func TestAWorkLogAtTheEndKeepsTheEndInView(t *testing.T) {
	h := longWorkLogThread(t)
	const summary = "Ran 3 commands"
	l := h.a.listFor(h.a.current().id())
	press := func(r ui.Rect) { h.tt.ClickAt(r.X+r.W/2, r.Y+r.H/2) }
	if !l.AtEnd() || h.a.composerK != 1 {
		t.Fatalf("the thread doesn't start at its end with the composer full: %v, %v", l.AtEnd(), h.a.composerK)
	}
	press(head(t, h.tt, summary))
	atEndAndFull(t, h, l, "opening the log")
	if !h.tt.HasText("go vet ./...") {
		t.Fatalf("a click didn't open the log: %q", h.tt.Texts())
	}

	press(head(t, h.tt, "go build ./..."))
	atEndAndFull(t, h, l, "opening a step's drawer")
	if !h.tt.HasText("built fine") {
		t.Fatalf("a click didn't open the step's drawer: %q", h.tt.Texts())
	}

	press(head(t, h.tt, summary))
	atEndAndFull(t, h, l, "closing the log")
	if h.tt.HasText("go vet ./...") {
		t.Fatalf("a second click didn't close the log: %q", h.tt.Texts())
	}
}

// Scrolled up, opening or closing a work log keeps its head where it is
// rather than going to the end.
func TestAWorkLogScrolledUpHoldsItsPlace(t *testing.T) {
	h := longWorkLogThread(t)
	const summary = "Ran 3 commands"
	l := h.a.listFor(h.a.current().id())
	wheel(h, -120)
	h.tt.Advance(time.Second)
	if l.AtEnd() {
		t.Fatal("scrolling up left the list at its end")
	}
	top := head(t, h.tt, summary)
	clickRect(h, top)
	if !h.tt.HasText("go vet ./...") {
		t.Fatalf("a click didn't open the log: %q", h.tt.Texts())
	}
	if r := head(t, h.tt, summary); r.Y != top.Y || l.AtEnd() {
		t.Errorf("opening it moved its head from %v to %v (at end %v)", top.Y, r.Y, l.AtEnd())
	}
	clickRect(h, head(t, h.tt, summary))
	if h.tt.HasText("go vet ./...") {
		t.Fatalf("a second click didn't close the log: %q", h.tt.Texts())
	}
	if r := head(t, h.tt, summary); r.Y != top.Y || l.AtEnd() {
		t.Errorf("closing it moved its head from %v to %v (at end %v)", top.Y, r.Y, l.AtEnd())
	}
}

// A short thread that fits until its work log opens follows its end as
// the log grows past it, and is back at its end, the composer full, when
// the log closes.
func TestAWorkLogOverflowingAShortThreadKeepsTheEndInView(t *testing.T) {
	at := time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)
	exit0 := 0
	run := api.NewID()
	steps := make([]client.WorkStep, 12)
	for i := range steps {
		steps[i] = client.WorkStep{Item: fmt.Sprintf("c%d", i), Kind: "command", Title: fmt.Sprintf("make step-%d", i), Status: api.WorkCompleted, Exit: &exit0, Started: at, Ended: at}
	}
	es := []client.Entry{
		&client.UserEntry{Message: api.NewID(), Text: strings.Repeat("A line of the prompt that goes on.\n", 12), State: client.Sent},
		&client.WorkEntry{Run: run, Started: at, Ended: at.Add(40 * time.Second), Items: steps},
		&client.AssistantEntry{Run: run, Text: "Built."},
	}
	const summary = "Ran 12 commands"
	h, _ := zenThread(t, es, false, "Built.")
	l := h.a.listFor(h.a.current().id())
	top := head(t, h.tt, summary)
	// It fits: the wheel doesn't move it.
	wheel(h, -120)
	h.tt.Advance(time.Second)
	if r := head(t, h.tt, summary); !l.AtEnd() || r.Y != top.Y {
		t.Fatalf("the thread scrolls before the log opens: at end %v, head %v, was %v", l.AtEnd(), r.Y, top.Y)
	}
	h.tt.ClickAt(top.X+top.W/2, top.Y+top.H/2)
	atEndAndFull(t, h, l, "opening the log")
	if !h.tt.HasText("make step-0") {
		t.Fatalf("a click didn't open the log: %q", h.tt.Texts())
	}
	opened := head(t, h.tt, summary)
	if opened.Y >= top.Y {
		t.Fatalf("the open log doesn't overflow the thread: head %v, was %v", opened.Y, top.Y)
	}
	h.tt.ClickAt(opened.X+opened.W/2, opened.Y+opened.H/2)
	atEndAndFull(t, h, l, "closing the log")
	if r := head(t, h.tt, summary); h.tt.HasText("make step-0") || r.Y != top.Y {
		t.Errorf("a second click didn't close the log in place: head %v, was %v", r.Y, top.Y)
	}
}

// stepButton is the bounds of the clickable step row button named name.
func stepButton(t *testing.T, tt *ui.Tester, name string) ui.Rect {
	t.Helper()
	for _, n := range tt.AccessAll() {
		if n.Role == ui.RoleButton && n.Name == name {
			return n.Bounds
		}
	}
	t.Fatalf("no button %q: %q", name, tt.Texts())
	return ui.Rect{}
}

// Failed step error output drawers default collapsed everywhere (under a
// folded work log, inside an expanded work log, and in an inline single
// step) and toggle open and closed on click for both commands and
// non-command tool calls.
func TestFailedWorkStepOutputDefaultsCollapsedAndToggles(t *testing.T) {
	at := time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)
	exit0, exit1 := 0, 1
	runMulti, runSingle := api.NewID(), api.NewID()
	const (
		cmdOut    = "--- FAIL: TestAuth\nexpected 200, got 500"
		toolOut   = "404 Not Found\nserver refused request"
		readOut   = "open missing.txt: no such file or directory"
		singleOut = "permission denied on /etc/shadow"
	)
	es := []client.Entry{
		&client.UserEntry{Message: api.NewID(), Text: "Run checks.", State: client.Sent},
		&client.WorkEntry{Run: runMulti, Started: at, Ended: at.Add(10 * time.Second), Items: []client.WorkStep{
			{Item: "ok", Kind: "command", Title: "go build ./...", Status: api.WorkCompleted, Exit: &exit0, Started: at, Ended: at},
			{Item: "fc", Kind: "command", Title: "go test ./...", Status: api.WorkFailed, Exit: &exit1, Output: cmdOut, Started: at, Ended: at},
			{Item: "ft", Kind: "tool", Title: "WebFetch https://example.com", Status: api.WorkFailed, Output: toolOut, Started: at, Ended: at},
			{Item: "fr", Kind: "read", Title: "Read missing.txt", Path: "missing.txt", Status: api.WorkFailed, Output: readOut, Started: at, Ended: at},
		}},
		&client.AssistantEntry{Run: runMulti, Text: "Three failed."},
		&client.UserEntry{Message: api.NewID(), Text: "Read secret.", State: client.Sent},
		&client.WorkEntry{Run: runSingle, Started: at, Ended: at.Add(time.Second), Items: []client.WorkStep{
			{Item: "s1", Kind: "tool", Title: "Fetch secret", Status: api.WorkFailed, Output: singleOut, Started: at, Ended: at},
		}},
		&client.AssistantEntry{Run: runSingle, Text: "Could not read it."},
	}
	h, _ := zenThread(t, es, false, "Could not read it.")
	const summary = "Ran 2 commands · used 1 tool · read 1 file"

	// 1. Folded work log + inline single step: failed rows show, outputs stay hidden.
	for _, want := range []string{summary, "3 failed", "ERR 1", "go test ./...", "WebFetch https://example.com", "missing.txt", "Fetch secret"} {
		if !h.tt.HasText(want) {
			t.Fatalf("folded, missing %q: %q", want, h.tt.Texts())
		}
	}
	for _, hidden := range []string{cmdOut, toolOut, readOut, singleOut} {
		if h.tt.HasText(hidden) {
			t.Fatalf("folded, error output %q expanded before click", hidden)
		}
	}

	// Clicking each surfaced failed row under the folded work log toggles its output.
	for _, tc := range []struct {
		label, out string
		isCmd      bool
	}{
		{"go test ./...", cmdOut, true},
		{"WebFetch https://example.com", toolOut, false},
		{"Read missing.txt", readOut, false},
	} {
		clickRect(h, stepButton(t, h.tt, tc.label))
		if !h.tt.HasText(tc.out) {
			t.Fatalf("folded, clicking %q did not open %q: %q", tc.label, tc.out, h.tt.Texts())
		}
		if !tc.isCmd {
			// Non-command failed steps should not duplicate w.Title as a drawer command block.
			nText := 0
			for _, n := range h.tt.AccessAll() {
				if n.Role == ui.RoleText && n.Name == tc.label {
					nText++
				}
			}
			if nText > 1 {
				t.Errorf("non-command failed drawer duplicated title %q (%d RoleText nodes)", tc.label, nText)
			}
		}
		clickRect(h, stepButton(t, h.tt, tc.label))
		if h.tt.HasText(tc.out) {
			t.Fatalf("folded, second click on %q did not collapse %q", tc.label, tc.out)
		}
	}

	// Opening a kept drawer, expanding the work log, then closing the work log
	// resets cleanly so the drawer collapses and can open again.
	clickRect(h, stepButton(t, h.tt, "go test ./..."))
	if !h.tt.HasText(cmdOut) {
		t.Fatalf("clicking kept failed command did not open output")
	}
	clickRect(h, head(t, h.tt, summary))
	if !h.tt.HasText("go build ./...") || !h.tt.HasText(cmdOut) {
		t.Fatalf("expanding log after opening kept drawer lost state: %q", h.tt.Texts())
	}
	clickRect(h, head(t, h.tt, summary))
	if h.tt.HasText("go build ./...") || h.tt.HasText(cmdOut) {
		t.Fatalf("closing log did not fold steps and reset kept drawer: %q", h.tt.Texts())
	}

	// 2. Inside an expanded work log, failed outputs default collapsed and toggle on click.
	clickRect(h, head(t, h.tt, summary))
	for _, hidden := range []string{cmdOut, toolOut, readOut} {
		if h.tt.HasText(hidden) {
			t.Fatalf("expanded log showed error output %q before click", hidden)
		}
	}
	for _, tc := range []struct{ label, out string }{
		{"go test ./...", cmdOut},
		{"WebFetch https://example.com", toolOut},
		{"Read missing.txt", readOut},
	} {
		clickRect(h, stepButton(t, h.tt, tc.label))
		if !h.tt.HasText(tc.out) {
			t.Fatalf("expanded, clicking %q did not open %q", tc.label, tc.out)
		}
		clickRect(h, stepButton(t, h.tt, tc.label))
		if h.tt.HasText(tc.out) {
			t.Fatalf("expanded, second click on %q did not collapse %q", tc.label, tc.out)
		}
	}
	clickRect(h, head(t, h.tt, summary))

	// 3. Single-step inline failed tool call defaults collapsed and toggles on click.
	clickRect(h, stepButton(t, h.tt, "Fetch secret"))
	if !h.tt.HasText(singleOut) {
		t.Fatalf("inline single failed step did not open %q on click", singleOut)
	}
	clickRect(h, stepButton(t, h.tt, "Fetch secret"))
	if h.tt.HasText(singleOut) {
		t.Fatalf("inline single failed step did not collapse %q on second click", singleOut)
	}
}

// Expanding and collapsing a surfaced failed error drawer at the end of a
// scrolling timeline keeps AtEnd and the full composer; scrolled up, it
// holds the row's place.
func TestFailedWorkErrorDrawerAtEndKeepsEndInView(t *testing.T) {
	at := time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)
	exit0, exit1 := 0, 1
	run := api.NewID()
	longErr := strings.Repeat("--- FAIL: TestLongTrace line\n", 10) + "exit status 1"
	es := synthetic(40)
	es = append(es,
		&client.UserEntry{Message: api.NewID(), Text: "Run failing test.", State: client.Sent},
		&client.WorkEntry{Run: run, Started: at, Ended: at.Add(5 * time.Second), Items: []client.WorkStep{
			{Item: "a", Kind: "command", Title: "go build ./...", Status: api.WorkCompleted, Exit: &exit0, Started: at, Ended: at},
			{Item: "b", Kind: "command", Title: "go test ./pkg/broken", Status: api.WorkFailed, Exit: &exit1, Output: longErr, Started: at, Ended: at},
		}},
		&client.AssistantEntry{Run: run, Text: "Test failed."},
	)
	h, _ := zenThread(t, es, false, "Test failed.")
	l := h.a.listFor(h.a.current().id())
	press := func(r ui.Rect) { h.tt.ClickAt(r.X+r.W/2, r.Y+r.H/2) }
	if !l.AtEnd() || h.a.composerK != 1 {
		t.Fatalf("thread did not start at end with full composer: atEnd=%v composerK=%v", l.AtEnd(), h.a.composerK)
	}
	if h.tt.HasText(longErr) {
		t.Fatal("failed error output expanded before click")
	}

	press(stepButton(t, h.tt, "go test ./pkg/broken"))
	atEndAndFull(t, h, l, "expanding failed error drawer at end")
	if !h.tt.HasText(longErr) {
		t.Fatalf("clicking failed row did not show error output: %q", h.tt.Texts())
	}

	press(stepButton(t, h.tt, "go test ./pkg/broken"))
	atEndAndFull(t, h, l, "collapsing failed error drawer at end")
	if h.tt.HasText(longErr) {
		t.Fatal("second click did not collapse error output")
	}

	// Scrolled up, toggling the failed error drawer holds its row in place.
	wheel(h, -120)
	h.tt.Advance(time.Second)
	if l.AtEnd() {
		t.Fatal("scrolling up left list at end")
	}
	rowBefore := stepButton(t, h.tt, "go test ./pkg/broken")
	clickRect(h, rowBefore)
	if !h.tt.HasText(longErr) {
		t.Fatal("scrolled up, clicking failed row did not show error output")
	}
	if r := stepButton(t, h.tt, "go test ./pkg/broken"); math.Abs(float64(r.Y-rowBefore.Y)) > 1 || l.AtEnd() {
		t.Errorf("scrolled up, expanding error drawer moved row from Y=%v to Y=%v (atEnd=%v)", rowBefore.Y, r.Y, l.AtEnd())
	}
	clickRect(h, stepButton(t, h.tt, "go test ./pkg/broken"))
	if h.tt.HasText(longErr) {
		t.Fatal("scrolled up, second click did not collapse error output")
	}
	if r := stepButton(t, h.tt, "go test ./pkg/broken"); math.Abs(float64(r.Y-rowBefore.Y)) > 1 || l.AtEnd() {
		t.Errorf("scrolled up, collapsing error drawer moved row from Y=%v to Y=%v (atEnd=%v)", rowBefore.Y, r.Y, l.AtEnd())
	}
}
