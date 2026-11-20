package render

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"glitched/internal/plugins"
	"glitched/internal/plugins/widget"
)

// longList is a view of a list of n rows under a header every 1000, and
// the search field that moves it.
func longList(n int, value string) string {
	items := make([]string, 0, n+n/1000)
	for i := range n {
		if i%1000 == 0 {
			items = append(items, fmt.Sprintf(`{"key": "h%d", "label": "From %d", "header": true, "height": 30}`, i/1000, i))
		}
		items = append(items, fmt.Sprintf(`{"key": "r%d", "label": "Row %d", "thread": "t%d"}`, i, i, i))
	}
	return `{"body": {"type": "column", "children": [
		{"type": "input", "id": "find", "label": "Find"},
		{"type": "list", "id": "l", "label": "Rows", "height": 400, "controlled": true, "value": "` + value + `", "input": "find",
		 "onChange": "chose", "onSubmit": "opened", "items": [` + strings.Join(items, ",") + `]}
	]}}`
}

func rowOf(key string) *widget.Node {
	n := strings.TrimPrefix(key, "r")
	if strings.HasPrefix(key, "h") {
		n, _ = strings.CutPrefix(key, "h")
		row, _ := widget.DecodeRow([]byte(`{"type": "text", "text": "From ` + n + `000"}`))
		return row
	}
	row, err := widget.DecodeRow([]byte(`{"type": "row", "children": [
		{"type": "text", "text": "Row ` + n + `"},
		{"type": "text", "text": "idle ` + n + `", "when": "idle"},
		{"type": "button", "id": "star", "label": "Star ` + n + `", "when": "hover", "onClick": "star"}
	]}`))
	if err != nil {
		panic(err)
	}
	return row
}

// A list of 10,000 rows draws those in view, and asks only for them; the
// choice is Glitched's, which clicks, the keys and a search field move.
func TestAListDrawsTheRowsInViewAndKeepsTheChoice(t *testing.T) {
	s := &screen{}
	s.show(t, longList(10000, ""))
	asked := map[string]bool{}
	var menus []string
	st := Style{
		Rows: func(list string, it widget.ListItem) *widget.Node {
			asked[it.Key] = true
			return rowOf(it.Key)
		},
		ContextMenu: func(c *ui.Context, e ui.Element, name string) {
			e.ContextMenu(func(m *ui.Menu) {
				menus = append(menus, name)
				m.Item("Rename")
			})
		},
	}
	tt := ui.NewTester(func(c *ui.Context) {
		s.cs.Sync(s.v, s.rev, s.processed)
		ui.Column(c).Fill().Padding(20).Children(func() {
			View(c, s.v, &s.cs, st, &s.md, func(ev plugins.UIEvent, values map[string]any) int {
				s.events = append(s.events, ev)
				return len(s.events)
			})
		})
	}, 600, 600)
	tt.Frame()
	if len(asked) == 0 || len(asked) > 60 || !asked["r0"] || asked["r9999"] {
		t.Fatalf("asked for %d rows of 10,000, r0 %v, r9999 %v: a list asks for the rows in view", len(asked), asked["r0"], asked["r9999"])
	}
	if !tt.HasText("Row 3") || tt.HasText("Row 9999") || !tt.HasText("From 0000") {
		t.Fatal("the rows in view aren't drawn as the plugin drew them")
	}

	chose := func(want string) {
		t.Helper()
		ev := s.last(t)
		if ev.Type != "change" || ev.Handler != "chose" || ev.Node != "l" || ev.Value != want {
			t.Fatalf("got %+v, want a change to %s", ev, want)
		}
	}
	if err := tt.Click("Row 3"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	chose("r3")
	tt.Key(0, ui.KeyDown)
	tt.Frame()
	chose("r4")
	tt.Key(0, ui.KeyUp)
	tt.Key(0, ui.KeyUp)
	tt.Frame()
	chose("r2")

	tt.Key(0, ui.KeyEnd)
	tt.Frame()
	tt.Frame()
	chose("r9999")
	if !tt.HasText("Row 9999") || !asked["r9999"] || len(asked) > 150 {
		t.Fatalf("End shows the last row: drawn %v, asked %v, %d asked", tt.HasText("Row 9999"), asked["r9999"], len(asked))
	}
	tt.Key(0, ui.KeyHome)
	tt.Frame()
	chose("r0")
	n := len(s.events)
	tt.Key(0, ui.KeyUp)
	tt.Frame()
	if len(s.events) != n {
		t.Fatalf("Up past the first row, over its header, moved the choice: %+v", s.last(t))
	}

	// The plugin chooses r999, the last before a header: Down goes over it.
	s.show(t, longList(10000, "r999"))
	s.processed = len(s.events)
	tt.Frame()
	tt.Frame()
	tt.Key(0, ui.KeyDown)
	tt.Frame()
	chose("r1000")

	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if ev := s.last(t); ev.Type != "submit" || ev.Handler != "opened" || ev.Value != "r1000" || len(ev.Modifiers) != 0 {
		t.Fatalf("Enter: %+v", ev)
	}
	tt.Key(ui.Cmd, ui.KeyEnter)
	tt.Frame()
	if ev := s.last(t); ev.Type != "submit" || !slices.Equal(ev.Modifiers, []string{"cmd"}) {
		t.Fatalf("Cmd-Enter: %+v", ev)
	}

	// Pointed at, a row shows its hover widgets in its idle ones' place,
	// and what they fire says which row they were in.
	r, ok := tt.Find("Row 1000")
	if !ok {
		t.Fatal("Row 1000 isn't drawn")
	}
	if !tt.HasText("idle 1000") || tt.HasText("Star 1000") {
		t.Fatal("a row not pointed at shows its idle widgets alone")
	}
	tt.Move(r.X+2, r.Y+2)
	tt.Frame()
	tt.Frame()
	if tt.HasText("idle 1000") || !tt.HasText("Star 1000") {
		t.Fatal("a row pointed at shows its hover widgets alone")
	}
	if err := tt.Click("Star 1000"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if ev := s.last(t); ev.Handler != "star" || ev.Row == nil || *ev.Row != (plugins.RowRef{List: "l", Key: "r1000"}) {
		t.Fatalf("a button in a row: %+v", ev)
	}

	if err := tt.RightClick("Row 999"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !slices.Contains(menus, "thread:t999") || !slices.Contains(tt.Menu(), "Rename") {
		t.Fatalf("a thread's row opens the thread's menu: %v, %v", menus, tt.Menu())
	}
	tt.CloseMenu()

	// The search field moves the list and submits it, keeping the keyboard.
	if err := tt.Click("Find"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.Focused("Find") {
		t.Fatal("the search field doesn't have the keyboard")
	}
	tt.Key(0, ui.KeyDown)
	tt.Frame()
	chose("r1001")
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if ev := s.last(t); ev.Type != "submit" || ev.Node != "l" || ev.Value != "r1001" {
		t.Fatalf("Enter in the search field: %+v", ev)
	}

	// A view without the item chosen leaves no choice to submit, though the
	// plugin doesn't hold the choice: Down chose r1, which goes.
	short := func(n int) string {
		return strings.Replace(longList(n, "r0"), `"controlled": true,`, "", 1)
	}
	s.show(t, short(3))
	s.processed = len(s.events)
	tt.Frame()
	tt.Key(0, ui.KeyDown)
	tt.Frame()
	chose("r1")
	s.show(t, short(1))
	s.processed = len(s.events)
	tt.Frame()
	n = len(s.events)
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if len(s.events) != n {
		t.Fatalf("Enter submitted an item the view no longer has: %+v", s.last(t))
	}
}

// A palette's list moves its choice to the row pointed at, and its Up and
// Down go on from one end to the other, past headers; Home and End stop.
func TestAPalettesListFollowsThePointerAndWrapsAround(t *testing.T) {
	s := &screen{}
	s.show(t, `{"body": {"type": "list", "id": "l", "label": "Results", "height": 400, "chooseOnHover": true, "wrapAround": true, "onChange": "chose", "items": [
		{"key": "h0", "label": "Actions", "header": true},
		{"key": "a", "label": "Alpha"}, {"key": "b", "label": "Bravo"},
		{"key": "h1", "label": "Threads", "header": true},
		{"key": "c", "label": "Charlie"}]}}`)
	st := Style{Rows: func(list string, it widget.ListItem) *widget.Node {
		row, _ := widget.DecodeRow([]byte(`{"type": "text", "text": "` + it.Label + `"}`))
		return row
	}}
	tt := ui.NewTester(func(c *ui.Context) {
		s.cs.Sync(s.v, s.rev, s.processed)
		View(c, s.v, &s.cs, st, &s.md, func(ev plugins.UIEvent, values map[string]any) int {
			s.events = append(s.events, ev)
			return len(s.events)
		})
	}, 400, 500)
	tt.Frame()
	if err := tt.Click("Alpha"); err != nil {
		t.Fatal(err)
	}
	tt.Move(390, 490) // the pointer would hold the choice where it rests
	tt.Frame()
	for _, step := range []struct {
		key  ui.Key
		want string
	}{{ui.KeyUp, "c"}, {ui.KeyDown, "a"}, {ui.KeyDown, "b"}, {ui.KeyDown, "c"}, {ui.KeyDown, "a"}, {ui.KeyEnd, "c"}, {ui.KeyHome, "a"}} {
		tt.Key(0, step.key)
		tt.Frame()
		if ev := s.last(t); ev.Type != "change" || ev.Value != step.want {
			t.Fatalf("after %v: %+v, want the choice at %s", step.key, ev, step.want)
		}
	}
	n := len(s.events)
	tt.Key(0, ui.KeyHome)
	tt.Frame()
	if len(s.events) != n {
		t.Fatalf("Home at the first row moved the choice: %+v", s.last(t))
	}
	r, ok := tt.Find("Bravo")
	if !ok {
		t.Fatal("Bravo isn't drawn")
	}
	tt.Move(r.X+2, r.Y+2)
	tt.Frame()
	if ev := s.last(t); ev.Type != "change" || ev.Value != "b" {
		t.Fatalf("pointing at Bravo: %+v, want the choice there", ev)
	}
	n = len(s.events)
	tt.Frame()
	if len(s.events) != n {
		t.Fatal("the row still pointed at fired again")
	}

	// Without the options, the pointer leaves the choice and the ends stop.
	s.show(t, strings.NewReplacer(`"chooseOnHover": true, `, "", `"wrapAround": true, `, "").Replace(`{"body": {"type": "list", "id": "l", "label": "Results", "height": 400, "chooseOnHover": true, "wrapAround": true, "onChange": "chose", "items": [
		{"key": "a", "label": "Alpha"}, {"key": "c", "label": "Charlie"}]}}`))
	s.processed = len(s.events)
	tt.Frame()
	if r, ok = tt.Find("Charlie"); !ok {
		t.Fatal("Charlie isn't drawn")
	}
	n = len(s.events)
	tt.Move(r.X+2, r.Y+2)
	tt.Frame()
	if len(s.events) != n {
		t.Fatalf("a list that doesn't follow the pointer did: %+v", s.last(t))
	}
	for range 3 {
		tt.Key(0, ui.KeyDown)
		tt.Frame()
	}
	if len(s.events) != n+2 || s.last(t).Value != "c" {
		t.Fatalf("a list that doesn't wrap went on past its end: %+v", s.events[n:])
	}
}

// The app's hook gets each row a list draws, and a row it holds shows its
// hover widgets though the pointer is elsewhere; a native in a row draws
// about the thread its item names.
func TestTheAppHoldsAListsRowsAndItsNativesDrawAboutTheirThread(t *testing.T) {
	s := &screen{}
	s.show(t, `{"body": {"type": "list", "id": "l", "label": "Threads", "height": 300, "items": [
		{"key": "a", "label": "Alpha", "thread": "ta"}, {"key": "b", "label": "Bravo", "thread": "tb"}, {"key": "c", "label": "Charlie"}]}}`)
	var hooked []string
	var natives []string
	st := Style{
		Rows: func(list string, it widget.ListItem) *widget.Node {
			row, err := widget.DecodeRow([]byte(`{"type": "row", "children": [
				{"type": "text", "text": "` + it.Label + `"},
				{"type": "text", "text": "idle ` + it.Key + `", "when": "idle"},
				{"type": "native", "name": "thread.status", "props": {"dot": true}, "when": "hover"}]}`))
			if err != nil {
				t.Fatal(err)
			}
			return row
		},
		ListRow: func(c *ui.Context, l *widget.Node, i int, e ui.Element) bool {
			hooked = append(hooked, l.ID+"/"+l.Items[i].Key)
			return l.Items[i].Key == "b"
		},
		Native: func(c *ui.Context, n *widget.Node, thread string, fire func(any)) {
			natives = append(natives, n.Name+" "+thread)
			ui.Text(c, "native "+thread)
		},
	}
	tt := ui.NewTester(func(c *ui.Context) {
		s.cs.Sync(s.v, s.rev, s.processed)
		View(c, s.v, &s.cs, st, &s.md, nil)
	}, 400, 400)
	tt.Frame()
	slices.Sort(hooked)
	if hooked = slices.Compact(hooked); !slices.Equal(hooked, []string{"l/a", "l/b", "l/c"}) {
		t.Fatalf("the hook got %v, want each row drawn", hooked)
	}
	if !tt.HasText("idle a") || tt.HasText("idle b") || !tt.HasText("native tb") || tt.HasText("native ta") {
		t.Fatalf("only the held row shows its hover widgets: %q", tt.Texts())
	}
	r, _ := tt.Find("Charlie")
	tt.Move(r.X+2, r.Y+2)
	tt.Frame()
	if !slices.Contains(natives, "thread.status ") || !tt.HasText("native ") {
		t.Fatalf("a native in a row of no thread is told none: %v", natives)
	}
}

// The pointer coming onto a row chooses it once: resting there leaves the
// keys, and a plugin that keeps its own value, to move the choice.
func TestAPalettesListChoosesTheRowThePointerComesOnto(t *testing.T) {
	const list = `{"body": {"type": "list", "id": "l", "label": "Results", "height": 300, "chooseOnHover": true, "controlled": true, "value": "a", "onChange": "chose", "onSubmit": "open",
		"items": [{"key": "a", "label": "Alpha"}, {"key": "b", "label": "Bravo"}, {"key": "c", "label": "Charlie"}]}}`
	s := &screen{}
	s.show(t, list)
	tt := ui.NewTester(func(c *ui.Context) {
		s.cs.Sync(s.v, s.rev, s.processed)
		View(c, s.v, &s.cs, Style{Rows: func(_ string, it widget.ListItem) *widget.Node {
			return &widget.Node{Type: "text", Text: it.Label}
		}}, &s.md, func(ev plugins.UIEvent, _ map[string]any) int {
			s.events = append(s.events, ev)
			return len(s.events)
		})
	}, 400, 400)
	r, _ := tt.Find("Bravo")
	tt.Move(r.X+2, r.Y+2)
	tt.Frame()
	if ev := s.last(t); ev.Type != "change" || ev.Value != "b" {
		t.Fatalf("pointing at Bravo: %+v", ev)
	}
	// The plugin keeps its own value, a: the pointer resting doesn't ask
	// again.
	n := len(s.events)
	for range 4 {
		s.processed = len(s.events)
		s.show(t, list)
		tt.Frame()
	}
	if len(s.events) != n {
		t.Fatalf("the pointer resting on Bravo asked again: %+v", s.events[n:])
	}
	// The keys move the choice from under the resting pointer.
	s.show(t, strings.Replace(list, `"value": "a"`, `"value": "b"`, 1))
	s.processed = len(s.events)
	tt.Frame()
	if err := tt.Click("Bravo"); err != nil { // the keyboard, where the pointer rests
		t.Fatal(err)
	}
	tt.Frame()
	tt.Key(0, ui.KeyDown)
	tt.Frame()
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if ev := s.last(t); ev.Type != "submit" || ev.Value != "c" {
		t.Fatalf("Down then Enter under a resting pointer: %+v, want c submitted", s.events[n:])
	}
	// Coming onto another row chooses it.
	r, _ = tt.Find("Alpha")
	tt.Move(r.X+2, r.Y+2)
	tt.Frame()
	if ev := s.last(t); ev.Type != "change" || ev.Value != "a" {
		t.Fatalf("pointing at Alpha: %+v", ev)
	}
}

// A pointer at rest chooses nothing, though the rows move under it: a
// wrap the keys scroll to, or rows the plugin reorders, keep the choice.
func TestAPointerAtRestChoosesNothingAsRowsMoveUnderIt(t *testing.T) {
	items := make([]string, 100)
	for i := range items {
		items[i] = fmt.Sprintf(`{"key": "k%d", "label": "Row %02d"}`, i, i)
	}
	s := &screen{}
	s.show(t, `{"body": {"type": "list", "id": "l", "label": "Results", "height": 108, "rowHeight": 36, "chooseOnHover": true, "wrapAround": true, "onChange": "choose", "onSubmit": "open", "items": [`+strings.Join(items, ",")+`]}}`)
	draw := func(c *ui.Context) {
		s.cs.Sync(s.v, s.rev, s.processed)
		View(c, s.v, &s.cs, Style{Rows: func(_ string, it widget.ListItem) *widget.Node {
			return &widget.Node{Type: "text", Text: it.Label}
		}}, &s.md, func(ev plugins.UIEvent, _ map[string]any) int {
			s.events = append(s.events, ev)
			return len(s.events)
		})
	}
	tt := ui.NewTester(draw, 400, 400)
	if err := tt.Click("Row 00"); err != nil {
		t.Fatal(err)
	}
	tt.Key(0, ui.KeyUp)
	tt.Frame()
	tt.Frame()
	tt.Key(0, ui.KeyEnter)
	if ev := s.last(t); ev.Type != "submit" || ev.Value != "k99" {
		t.Fatalf("Up wrapped to the last row, scrolled under the pointer, then Enter: %+v", ev)
	}

	const start = `{"body": {"type": "list", "id": "l", "label": "Results", "height": 300, "controlled": true, "value": "a", "chooseOnHover": true, "onChange": "choose", "items": [{"key": "a", "label": "Alpha"}, {"key": "b", "label": "Bravo"}, {"key": "c", "label": "Charlie"}]}}`
	reordered := strings.Replace(start, `{"key": "b", "label": "Bravo"}, {"key": "c", "label": "Charlie"}`, `{"key": "c", "label": "Charlie"}, {"key": "b", "label": "Bravo"}`, 1)
	s.show(t, start)
	s.processed = len(s.events)
	tt.Frame()
	r, _ := tt.Find("Bravo")
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	if ev := s.last(t); ev.Type != "change" || ev.Value != "b" {
		t.Fatalf("moving onto Bravo: %+v", ev)
	}
	n := len(s.events)
	for i := range 6 {
		s.processed = len(s.events)
		s.show(t, []string{reordered, start}[i%2])
		tt.Frame()
	}
	if len(s.events) != n {
		t.Fatalf("rows reordered under a pointer at rest chose %+v", s.events[n:])
	}
}
