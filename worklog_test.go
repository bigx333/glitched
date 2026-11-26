package main

import (
	"fmt"
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
	for _, s := range []string{summary, "1 failed", "40s", "go test ./...", "--- FAIL: TestIt", "Agent alpha", "notes.txt", "Searched 30 times"} {
		if !h.tt.HasText(s) {
			t.Errorf("folded, the thread doesn't show %q: %q", s, h.tt.Texts())
		}
	}
	for _, s := range []string{"Edit main.go", "go build ./...", "1 step", "Searched for thing 0"} {
		if h.tt.HasText(s) {
			t.Errorf("folded, the thread shows %q", s)
		}
	}

	top := head(t, h.tt, summary)
	clickRect(h, top)
	if !h.tt.HasText("Edit main.go") || !h.tt.HasText("go build ./...") {
		t.Fatalf("a click didn't open the log: %q", h.tt.Texts())
	}
	if r := head(t, h.tt, summary); r.Y != top.Y {
		t.Errorf("opening it moved its head from %v to %v", top.Y, r.Y)
	}
	clickRect(h, head(t, h.tt, summary))
	if h.tt.HasText("Edit main.go") || !h.tt.HasText("--- FAIL: TestIt") {
		t.Fatalf("a second click didn't close the log, or hid its failure: %q", h.tt.Texts())
	}
	if r := head(t, h.tt, summary); r.Y != top.Y {
		t.Errorf("closing it moved its head from %v to %v", top.Y, r.Y)
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
	inScroll := func(h *harness, scrollBox ui.Rect, name string) (ui.Rect, bool) {
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
	// Command 24 fails with multi-line output (height grows without step count changing),
	// then steps 25..39 arrive.
	steps25Fail := liveSteps(25)
	exit1 := 1
	steps25Fail[24].Status, steps25Fail[24].Exit = api.WorkFailed, &exit1
	steps25Fail[24].Output = strings.Repeat("error line\n", 8) + "exit status 1"
	th.View.Run = api.RunRunning
	th.View.Turn = &api.Turn{Run: run}
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
