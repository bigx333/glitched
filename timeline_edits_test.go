package main

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/daemon"
	"glitched/internal/provider/fake"
)

// replay is an app and a thread whose transcript folds the events play
// is given, each a change posted to the window.
func replay(t *testing.T) (*app, thread, func(api.EventBody)) {
	t.Helper()
	a := &app{rows: map[api.ID]*rowsCache{}}
	tr := &client.Transcript{}
	th := thread{Thread: &client.Thread{Transcript: tr}}
	log := api.NewID()
	return a, th, func(b api.EventBody) {
		t.Helper()
		p, err := json.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		seq := tr.Cursor.Seq + 1
		tr.Apply(api.Event{Cursor: api.Cursor{Log: log, Seq: seq}, Type: api.EventType(b), Schema: api.EventSchema, At: time.Unix(int64(seq), 0), Payload: p})
		a.posts++
	}
}

// TestAnEditChangedTwice moves an edit far up a thread to another file,
// then another: the diff follows each move.
func TestAnEditChangedTwice(t *testing.T) {
	a, th, play := replay(t)
	run := api.NewID()
	play(api.MessageQueued{Message: run, Text: "Fix a file"})
	play(api.TurnAdmitted{Run: run})
	play(api.TurnStarted{Run: run})
	play(api.WorkItem{Run: run, Item: "edit", Kind: "edit", Path: "a.go", Status: api.WorkStarted})
	for i := range 70 {
		play(api.AssistantDelta{Run: run, Text: "Still processing."})
		play(api.WorkItem{Run: run, Item: fmt.Sprint(i), Kind: "read", Status: api.WorkCompleted})
	}
	check := func(want string) {
		t.Helper()
		got := a.rowsOf(th).files
		if len(got) != 1 || got[0].path != want {
			t.Fatalf("diff files = %v, want [%s]; full derivation gives %v", got, want, changedFiles(entriesOf(th)))
		}
	}
	check("a.go")
	play(api.WorkItem{Run: run, Item: "edit", Path: "b.go", Status: api.WorkStarted})
	check("b.go")
	play(api.WorkItem{Run: run, Item: "edit", Path: "c.go", Status: api.WorkCompleted})
	check("c.go")
}

// TestAWakeFarUpIsDelivered delivers the wake of a child's report that
// is far up the thread: its row says so.
func TestAWakeFarUpIsDelivered(t *testing.T) {
	a, th, play := replay(t)
	run, child, wake := api.NewID(), api.NewID(), api.NewID()
	play(api.MessageQueued{Message: run, Text: "Delegate work", Pos: 1})
	play(api.TurnAdmitted{Run: run})
	play(api.TurnStarted{Run: run})
	play(api.ChildReported{Child: child, Run: api.NewID(), Kind: api.ReportDone, Title: "Child A"})
	play(api.MessageQueued{Message: wake, Report: child, Held: true, Pos: 2})
	for range 70 {
		play(api.ChildReported{Child: api.NewID(), Run: api.NewID(), Kind: api.ReportBlocked, Title: "Needs input"})
	}
	check := func(want string) {
		t.Helper()
		got := wakeLine(th, a.rowsOf(th).es[1].reports)
		if got != want {
			t.Fatalf("child report = %q, want %q; full derivation gives %q", got, want, wakeLine(th, entriesOf(th)[1].reports))
		}
	}
	check("↳ Goes to the agent after this turn")
	play(api.TurnEnded{Run: run, Outcome: api.OutcomeCompleted})
	play(api.TurnAdmitted{Run: wake})
	play(api.TurnStarted{Run: wake})
	check("↳ Sent to the agent")
}

// TestShiftTabComesToTheLastMarker comes back into the markers with
// Shift-Tab: to the last turn's, built though it does not show.
func TestShiftTabComesToTheLastMarker(t *testing.T) {
	var es []entry
	for i := range 80 {
		es = append(es, entry{kind: entryUser, key: fmt.Sprint(i), text: fmt.Sprintf("Turn %d", i)})
	}
	turns := turnsOf(es)
	a := app{markerFocus: map[api.ID]markerAt{}}
	var l ui.ListState
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Box(c).Grow(1).Children(func() {
			a.turnMarkers(c, api.ID{}, rows{es: es, turns: turns}, &l)
			ui.Button(c.Key("after"), "After markers")
		})
	}, 480, 160)
	tt.Frame()
	if err := tt.Click("After markers"); err != nil {
		t.Fatal(err)
	}
	if !tt.Focused("After markers") {
		t.Fatal("setup: the button after the markers did not get focus")
	}
	tt.Key(ui.Shift, ui.KeyTab)
	tt.Frame()
	if !tt.Focused("Go to: Turn 79") {
		for i := range 80 {
			if name := fmt.Sprintf("Go to: Turn %d", i); tt.Focused(name) {
				t.Fatalf("Shift-Tab from after the markers focused %q, want the last turn (79)", name)
			}
		}
		t.Fatal("Shift-Tab from after the markers did not focus the last turn (79)")
	}
}

// TestAFarMarkerKeepsTheFocus tabs to a marker past the window, resizes,
// and visits other threads: back, the marker has the focus.
func TestAFarMarkerKeepsTheFocus(t *testing.T) {
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(false)))
	seed(t, d, "/work/glitched", "Long thread")
	seed(t, d, "/work/glitched", "One-turn thread")
	seed(t, d, "/work/glitched", "Two-turn thread")
	h := start(t, dir)
	initThread := func(title string, turns int) thread {
		th := h.thread(title)
		h.a.select_(th)
		h.until("initial snapshot", func() bool { return th.Transcript != nil && th.Transcript.Cursor.Seq > 0 })
		for i := range turns {
			th.Transcript.Entries = append(th.Transcript.Entries, &client.UserEntry{Message: api.NewID(), Text: fmt.Sprintf("%s turn %d", title, i), State: client.Sent})
		}
		h.a.changed()
		return th
	}
	short := initThread("One-turn thread", 1)
	two := initThread("Two-turn thread", 2)
	long := initThread("Long thread", 90)
	h.tt.Advance(time.Second)
	h.click("Go to: Long thread turn 0")
	h.tt.Move(1000, 200)
	for i := 1; i <= 55; i++ {
		h.tt.Key(0, ui.KeyTab)
		h.tt.Frame()
		if !h.tt.Focused(fmt.Sprintf("Go to: Long thread turn %d", i)) {
			t.Fatalf("forward Tab %d did not move to turn %d", i, i)
		}
	}
	label := "Go to: Long thread turn 55"
	for _, height := range []int{750, 1100, 860} {
		h.tt.SetSize(1320, height)
		h.tt.Frame()
		if !h.tt.Focused(label) {
			t.Fatalf("turn 55 lost focus at height %d", height)
		}
	}
	h.a.select_(short)
	h.tt.Frame()
	if _, ok := h.tt.Access("Go to: One-turn thread turn 0"); ok {
		t.Fatal("a one-turn thread built a marker")
	}
	h.a.select_(long)
	h.tt.Frame()
	if !h.tt.Focused(label) {
		t.Fatal("after visiting a one-turn thread, the far marker did not regain focus")
	}
	h.a.select_(two)
	h.tt.Frame()
	if _, ok := h.tt.Access("Go to: Two-turn thread turn 1"); !ok {
		t.Fatal("setup: a two-turn thread did not build its second marker")
	}
	h.a.select_(long)
	h.tt.Frame()
	if !h.tt.Focused(label) {
		t.Fatal("after visiting a two-turn thread, the far marker did not regain focus")
	}
}

// TestTabWalksPastTheWindow walks the markers of 80 turns with Tab and
// Shift-Tab, a frame after each key and no more, as on the Mac: from a
// turn clicked, from the controls before and after the markers, to both
// ends, each key moves one turn, and with the window not having the keys.
func TestTabWalksPastTheWindow(t *testing.T) {
	const n = 80
	turn := func(k int) string { return fmt.Sprintf("Go to: Turn %d", k) }
	// markers is a window of the markers between two buttons, which tab
	// keys walk.
	markers := func(t *testing.T) (*ui.Tester, func(ui.Modifiers, int)) {
		var es []entry
		for i := range n {
			es = append(es, entry{kind: entryUser, key: fmt.Sprint(i), text: fmt.Sprintf("Turn %d", i)})
		}
		turns := turnsOf(es)
		a := app{markerFocus: map[api.ID]markerAt{}}
		var l ui.ListState
		tt := ui.NewTester(func(c *ui.Context) {
			ui.Column(c).Grow(1).Children(func() {
				ui.Button(c.Key("before"), "Before markers")
				ui.Box(c).Grow(1).Children(func() { a.turnMarkers(c, api.ID{}, rows{es: es, turns: turns}, &l) })
				ui.Button(c.Key("after"), "After markers")
			})
		}, 480, 220)
		tt.Frame()
		focused := func() string {
			for k := range n {
				if tt.Focused(turn(k)) {
					return turn(k)
				}
			}
			for _, b := range []string{"Before markers", "After markers"} {
				if tt.Focused(b) {
					return b
				}
			}
			return "nothing"
		}
		// A key repeated, as held down: one frame follows it, as one does a
		// key that comes up before the next frame.
		press := func(mods ui.Modifiers, want int) {
			t.Helper()
			tt.KeyRepeat(mods, ui.KeyTab)
			name := "After markers"
			switch {
			case want < 0:
				name = "Before markers"
			case want < n:
				name = turn(want)
			}
			if !tt.Focused(name) {
				t.Fatalf("the key focused %s, want %s", focused(), name)
			}
		}
		return tt, press
	}
	t.Run("Tab from a turn clicked to the last and on", func(t *testing.T) {
		tt, press := markers(t)
		if err := tt.Click(turn(3)); err != nil {
			t.Fatal(err)
		}
		tt.Move(400, 80)
		for k := 4; k <= n; k++ {
			press(0, k)
		}
	})
	t.Run("Tab in from before the markers", func(t *testing.T) {
		tt, press := markers(t)
		if err := tt.Click("Before markers"); err != nil {
			t.Fatal(err)
		}
		for k := range n {
			press(0, k)
		}
	})
	t.Run("Shift-Tab in from after the markers to the first and on", func(t *testing.T) {
		tt, press := markers(t)
		if err := tt.Click("After markers"); err != nil {
			t.Fatal(err)
		}
		for k := n - 1; k >= -1; k-- {
			press(ui.Shift, k)
		}
	})
	t.Run("Shift-Tab from the last turn, and Tab back", func(t *testing.T) {
		tt, press := markers(t)
		if err := tt.Click("After markers"); err != nil {
			t.Fatal(err)
		}
		press(ui.Shift, n-1)
		for k := n - 2; k >= 30; k-- {
			press(ui.Shift, k)
		}
		for k := 31; k < n; k++ {
			press(0, k)
		}
	})
	t.Run("with the window not having the keys", func(t *testing.T) {
		tt, press := markers(t)
		if err := tt.Click(turn(3)); err != nil {
			t.Fatal(err)
		}
		tt.SetFocused(false)
		for k := 4; k < 40; k++ {
			press(0, k)
		}
		tt.SetFocused(true)
		for k := 40; k < n; k++ {
			press(0, k)
		}
	})
}
