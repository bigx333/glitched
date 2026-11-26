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
		{[]client.WorkStep{{Kind: "command"}, {Kind: "edit", Path: "a.go"}, {Kind: "command"}, {Kind: "search"}}, "Ran 2 commands · edited 1 file · searched 1 time"},
		{[]client.WorkStep{{Kind: "read", Path: "a"}, {Kind: "read", Path: "b"}, {Kind: "read", Path: "c"}, {Kind: "read", Path: "d"}, {Kind: "command"}}, "Read 4 files · ran 1 command"},
		// A file edited three times is one file edited.
		{[]client.WorkStep{{Kind: "edit", Path: "a.go"}, {Kind: "edit", Path: "a.go"}, {Kind: "edit", Path: "a.go"}}, "Edited 1 file"},
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
		open bool
		want workMode
	}{
		{entry{work: one}, false, workInlineSingle},
		{entry{work: one}, true, workExpanded},
		{entry{work: one, paused: "Waiting for approval"}, false, workSettledCollapsed},
		{entry{work: one, working: true}, false, workLiveCollapsed},
		{entry{work: two, working: true}, true, workExpanded},
		{entry{work: two}, false, workSettledCollapsed},
		{entry{work: two}, true, workExpanded},
	} {
		if got := classifyWork(tc.e, tc.open); got != tc.want {
			t.Errorf("classifyWork(%+v, %v) = %v, want %v", tc.e, tc.open, got, tc.want)
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
	const summary = "Ran 2 commands · edited 1 file · started 1 subagent"
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
