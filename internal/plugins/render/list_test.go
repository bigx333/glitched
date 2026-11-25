package render

import (
	"fmt"
	"slices"
	"strconv"
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
		s.taking()
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
		s.taking()
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
		s.taking()
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
		s.taking()
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
		s.taking()
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

// taking syncs the screen's controls with its view, as a plugin that
// takes each event the moment it's sent: the keys pressed on a list wait
// for nothing.
func (s *screen) taking() {
	s.cs.Sync(s.v, s.rev, s.processed)
	s.cs.processed = max(s.cs.processed, len(s.events))
}

// Keys pressed on a list while the plugin hasn't taken what was sent
// before them wait for it, then move and submit the list it draws: a
// query typed and Enter at once submit what the query finds, as on
// Glitched's own palette.
func TestKeysWaitForThePluginToTakeWhatWasSentBefore(t *testing.T) {
	s := &screen{}
	view := func(value string, keys ...string) string {
		var items []string
		for _, k := range keys {
			items = append(items, `{"key": "`+k+`", "label": "Row `+k+`"}`)
		}
		return `{"body": {"type": "column", "children": [{"type": "input", "id": "q", "label": "Query", "onChange": "query"},
			{"type": "list", "id": "l", "label": "Results", "height": 200, "input": "q", "controlled": true, "value": "` + value + `", "onChange": "chose", "onSubmit": "open", "items": [` + strings.Join(items, ",") + `]}]}}`
	}
	s.show(t, view("a", "a", "b", "c"))
	tt := ui.NewTester(func(c *ui.Context) {
		s.cs.Sync(s.v, s.rev, s.processed)
		View(c, s.v, &s.cs, Style{Rows: func(_ string, it widget.ListItem) *widget.Node {
			return &widget.Node{Type: "text", Text: it.Label}
		}}, &s.md, func(ev plugins.UIEvent, _ map[string]any) int {
			s.events = append(s.events, ev)
			return len(s.events)
		})
	}, 400, 400)
	tt.Frame()
	in, ok := tt.AccessByRole(ui.RoleTextField, "")
	if !ok {
		t.Fatal("no search field")
	}
	tt.ClickAt(in.Bounds.X+5, in.Bounds.Y+in.Bounds.H/2)
	tt.Type("x")
	tt.Key(0, ui.KeyDown)
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if ev := s.last(t); ev.Type != "change" || ev.Handler != "query" {
		t.Fatalf("keys acted on the list before the plugin took the query: %+v", s.events)
	}
	// The plugin takes the query and draws what it finds.
	s.show(t, view("x1", "x1", "x2", "x3"))
	s.processed = len(s.events)
	tt.Frame()
	if ev := s.last(t); ev.Type != "change" || ev.Value != "x2" {
		t.Fatalf("Down after the plugin took the query: %+v, want x2 chosen", ev)
	}
	tt.Frame()
	if ev := s.last(t); ev.Type != "change" {
		t.Fatalf("Enter went before the plugin took the move: %+v", ev)
	}
	s.show(t, view("x2", "x1", "x2", "x3"))
	s.processed = len(s.events)
	tt.Frame()
	if ev := s.last(t); ev.Type != "submit" || ev.Value != "x2" {
		t.Fatalf("Enter after the plugin took the move: %+v, want x2 submitted", ev)
	}
}

// What the input types behind a key still waiting for the plugin waits
// behind it: Enter submits what the query before it found, and End goes
// to the end of the list the plugin draws by then.
func TestTypingBehindAWaitingKeyWaitsBehindIt(t *testing.T) {
	s := &screen{}
	view := func(value string, keys ...string) string {
		var items []string
		for _, k := range keys {
			items = append(items, `{"key": "`+k+`", "label": "Row `+k+`"}`)
		}
		return `{"body": {"type": "column", "children": [{"type": "input", "id": "q", "label": "Query", "onChange": "query"},
			{"type": "list", "id": "l", "label": "Results", "height": 300, "input": "q", "controlled": true, "value": "` + value + `", "onChange": "chose", "onSubmit": "open", "items": [` + strings.Join(items, ",") + `]}]}}`
	}
	s.show(t, view("a", "a", "b"))
	tt := ui.NewTester(func(c *ui.Context) {
		s.cs.Sync(s.v, s.rev, s.processed)
		View(c, s.v, &s.cs, Style{Rows: func(_ string, it widget.ListItem) *widget.Node {
			return &widget.Node{Type: "text", Text: it.Label}
		}}, &s.md, func(ev plugins.UIEvent, _ map[string]any) int {
			s.events = append(s.events, ev)
			return len(s.events)
		})
	}, 400, 400)
	tt.Frame()
	in, ok := tt.AccessByRole(ui.RoleTextField, "")
	if !ok {
		t.Fatal("no search field")
	}
	tt.ClickAt(in.Bounds.X+5, in.Bounds.Y+in.Bounds.H/2)
	tt.Type("x")
	tt.Key(0, ui.KeyEnter)
	tt.Type("y")
	tt.Frame()
	if len(s.events) != 1 || s.events[0].Value != "x" {
		t.Fatalf("what was typed behind Enter went before it: %+v", s.events)
	}
	s.show(t, view("x1", "x1", "x2"))
	s.processed = len(s.events)
	tt.Frame()
	if ev := s.last(t); ev.Type != "submit" || ev.Value != "x1" {
		t.Fatalf("Enter after the plugin took x: %+v, want x1 submitted", ev)
	}
	tt.Frame()
	if len(s.events) != 2 {
		t.Fatalf("what was typed behind Enter went before the plugin took it: %+v", s.events)
	}
	s.processed = len(s.events)
	s.rev++
	tt.Frame()
	if ev := s.last(t); ev.Type != "change" || ev.Value != "xy" {
		t.Fatalf("what was typed behind Enter: %+v, want xy sent after it", ev)
	}

	// End, pressed before the plugin took the query, goes to the end of
	// the longer list it draws for it.
	r, _ := tt.Find("Row x1")
	tt.ClickAt(r.X+2, r.Y+2)
	tt.Frame()
	s.show(t, view("x1", "x1", "x2"))
	s.processed = len(s.events) - 1
	tt.Frame()
	tt.Key(0, ui.KeyEnd)
	s.show(t, view("x1", "x1", "x2", "x3", "x4", "x5", "x6", "x7"))
	s.processed = len(s.events)
	tt.Frame()
	if ev := s.last(t); ev.Type != "change" || ev.Value != "x7" {
		t.Fatalf("End before the plugin caught up: %+v, want x7 chosen", ev)
	}
}

func TestThePointerMovingBehindAWaitingEnterWaitsBehindIt(t *testing.T) {
	s := &screen{}
	view := func(chosen string) string {
		return `{"body": {"type": "column", "children": [{"type": "input", "id": "q", "label": "Query", "onChange": "query"},
			{"type": "list", "id": "l", "input": "q", "label": "Results", "height": 300, "chooseOnHover": true, "controlled": true, "value": "` + chosen + `", "onChange": "chose", "onSubmit": "open", "items": [{"key": "a", "label": "Alpha"}, {"key": "b", "label": "Bravo"}]}]}}`
	}
	s.show(t, view("a"))
	tt := ui.NewTester(func(c *ui.Context) {
		s.cs.Sync(s.v, s.rev, s.processed)
		View(c, s.v, &s.cs, Style{Rows: func(_ string, it widget.ListItem) *widget.Node {
			return &widget.Node{Type: "text", Text: it.Label}
		}}, &s.md, func(ev plugins.UIEvent, _ map[string]any) int {
			s.events = append(s.events, ev)
			return len(s.events)
		})
	}, 400, 400)
	tt.Frame()
	in, ok := tt.AccessByRole(ui.RoleTextField, "")
	if !ok {
		t.Fatal("no search field")
	}
	tt.ClickAt(in.Bounds.X+5, in.Bounds.Y+in.Bounds.H/2)
	tt.Type("x")
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	row, _ := tt.Find("Bravo")
	tt.Move(row.X+row.W/2, row.Y+row.H/2)
	tt.Frame()
	if len(s.events) != 1 || s.events[0].Value != "x" {
		t.Fatalf("the pointer moving went before the waiting Enter: %+v", s.events)
	}
	s.processed = len(s.events)
	s.rev++
	tt.Frame()
	if ev := s.last(t); ev.Type != "submit" || ev.Value != "a" {
		t.Fatalf("Enter: %+v, want a, chosen as it was pressed, submitted", ev)
	}
	s.processed = len(s.events)
	s.rev++
	tt.Frame()
	if ev := s.last(t); ev.Type != "change" || ev.Value != "b" {
		t.Fatalf("the pointer moving over Bravo behind Enter: %+v, want b chosen after it", ev)
	}
}

func TestKeysPressedInADialogShutGoWithIt(t *testing.T) {
	view := func(open bool, key string) string {
		return `{"body": {"type": "dialog", "id": "d", "title": "Picker", "onClose": "dismiss", "open": ` + strconv.FormatBool(open) + `, "children": [{"type": "input", "id": "q", "label": "Query", "onChange": "query"},
			{"type": "list", "id": "l", "input": "q", "label": "Results", "height": 120, "controlled": true, "value": "` + key + `", "onChange": "chose", "onSubmit": "run", "items": [{"key": "` + key + `", "label": "Row ` + key + `"}]}]}}`
	}
	s := &screen{}
	s.show(t, view(true, "a"))
	tt := ui.NewTester(s.draw, 800, 700)
	tt.Frame()
	in, ok := tt.AccessByRole(ui.RoleTextField, "")
	if !ok {
		t.Fatal("no search field")
	}
	tt.ClickAt(in.Bounds.X+5, in.Bounds.Y+in.Bounds.H/2)
	tt.Type("x")
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if len(s.events) != 2 || s.events[1].Type != "close" || tt.HasText("Picker") {
		t.Fatalf("Escape: events %+v, screen %q, want the dialog shut", s.events, tt.Texts())
	}
	s.processed = len(s.events)
	s.show(t, view(false, "a"))
	tt.Frame()
	s.show(t, view(true, "b"))
	tt.Frame()
	tt.Frame()
	if !tt.HasText("Picker") {
		t.Fatal("the dialog didn't open again")
	}
	if len(s.events) != 2 {
		t.Errorf("the dialog opened again ran a key pressed before it shut: %+v", s.events[2:])
	}
}

func TestTypingBehindAWaitingKeyGoesToTheInputTheViewHasThen(t *testing.T) {
	view := func(input string) string {
		drive := ""
		if strings.Contains(input, `"type": "input"`) {
			drive = `"input": "q", `
		}
		return `{"body": {"type": "column", "children": [` + input + `
			{"type": "list", "id": "l", "label": "Results", "height": 300, ` + drive + `"controlled": true, "value": "a", "onChange": "chose", "onSubmit": "open", "items": [{"key": "a", "label": "Row a"}]}]}}`
	}
	for _, c := range []struct {
		name, input, want string
		// then is the view's next value of q, once every event is taken.
		then any
	}{
		{"gone", "", "", nil},
		{"handled anew", `{"type": "input", "id": "q", "label": "Query", "onChange": "search"},`, "search", nil},
		{"handled by none", `{"type": "input", "id": "q", "label": "Query", "controlled": true, "value": "READY"},`, "", "READY"},
		{"a toggle now", `{"type": "toggle", "id": "q", "label": "Query", "controlled": true, "value": true},`, "", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := &screen{}
			s.show(t, view(`{"type": "input", "id": "q", "label": "Query", "onChange": "query"},`))
			tt := ui.NewTester(s.draw, 400, 400)
			tt.Frame()
			in, ok := tt.AccessByRole(ui.RoleTextField, "")
			if !ok {
				t.Fatal("no search field")
			}
			tt.ClickAt(in.Bounds.X+5, in.Bounds.Y+in.Bounds.H/2)
			tt.Type("x")
			tt.Key(0, ui.KeyEnter)
			tt.Type("y")
			tt.Frame()
			if len(s.events) != 1 {
				t.Fatalf("what was typed behind Enter went before it: %+v", s.events)
			}
			s.show(t, view(c.input))
			s.processed = len(s.events)
			tt.Frame()
			if ev := s.last(t); ev.Type != "submit" {
				t.Fatalf("Enter after the plugin took x: %+v, want a submit", ev)
			}
			s.processed = len(s.events)
			s.rev++
			tt.Frame()
			var got []plugins.UIEvent
			for _, ev := range s.events[2:] {
				if ev.Type == "change" {
					got = append(got, ev)
				}
			}
			switch {
			case c.want == "" && len(got) > 0:
				t.Errorf("an input the view took away got what was typed in it: %+v", got)
			case c.want != "" && (len(got) != 1 || got[0].Handler != c.want || got[0].Value != "xy"):
				t.Errorf("what was typed behind Enter: %+v, want xy to %s, the input's handler now", got, c.want)
			}
			if c.then != nil {
				s.rev++
				tt.Frame()
				if v := s.cs.Values()["q"]; v != c.then {
					t.Errorf("q, once the plugin took every event: %#v, want the view's %#v", v, c.then)
				}
			}
		})
	}
}

func TestAListWithNoKeysMovesOnlyByThePointer(t *testing.T) {
	s := &screen{}
	s.show(t, `{"body": {"type": "list", "id": "l", "label": "Threads", "height": 200, "noKeys": true, "onChange": "chose", "onSubmit": "open",
		"items": [{"key": "a", "label": "Row a"}, {"key": "b", "label": "Row b"}]}}`)
	tt := ui.NewTester(s.draw, 400, 400)
	tt.Frame()
	r, _ := tt.Find("Row a")
	tt.ClickAt(r.X+2, r.Y+2)
	tt.Frame()
	if ev := s.last(t); ev.Type != "change" || ev.Value != "a" {
		t.Fatalf("a click: %+v, want a chosen", ev)
	}
	n := len(s.events)
	// A click under the rows, on the list itself, and the keys a list
	// takes, and Tab to the next row.
	tt.ClickAt(r.X+2, r.Y+150)
	tt.Frame()
	for _, k := range []ui.Key{ui.KeyDown, ui.KeyUp, ui.KeyEnd, ui.KeyHome, ui.KeyTab, ui.KeyDown} {
		tt.Key(0, k)
		tt.Frame()
	}
	if len(s.events) != n {
		t.Errorf("keys moved a list with noKeys: %+v", s.events[n:])
	}
	if l, ok := tt.AccessByRole(ui.RoleList, "Threads"); !ok || l.Focused {
		t.Errorf("a list with noKeys took the focus: %+v", l)
	}
}

// A row keeps its widgets as the pointer comes and goes, though those
// shown on hover or idle alone come and go with it: a button the user
// focused keeps the focus.
func TestARowsWidgetsKeepTheirIdentityAsThePointerComesAndGoes(t *testing.T) {
	s := &screen{}
	s.show(t, `{"body": {"type": "list", "id": "l", "label": "Rows", "height": 200, "items": [{"key": "a", "label": "Row a"}]}}`)
	st := Style{Rows: func(list string, it widget.ListItem) *widget.Node {
		row, err := widget.DecodeRow([]byte(`{"type": "row", "children": [
			{"type": "text", "text": "idle", "when": "idle"},
			{"type": "button", "id": "star", "label": "Star", "when": "hover", "onClick": "star"},
			{"type": "button", "id": "open", "label": "Open", "onClick": "open"},
			{"type": "button", "id": "pin", "label": "Pin", "when": "hover", "onClick": "pin"}
		]}`))
		if err != nil {
			t.Fatal(err)
		}
		return row
	}}
	tt := ui.NewTester(func(c *ui.Context) {
		s.taking()
		ui.Column(c).Fill().Padding(20).Children(func() {
			View(c, s.v, &s.cs, st, &s.md, func(ev plugins.UIEvent, values map[string]any) int {
				s.events = append(s.events, ev)
				return len(s.events)
			})
		})
	}, 600, 400)
	tt.Frame()
	in, ok := tt.Find("Open")
	if !ok {
		t.Fatal("the row's button isn't drawn")
	}
	tt.Move(in.X+2, in.Y+2)
	tt.Frame()
	if err := tt.Click("Open"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("Star") || !tt.Focused("Open") {
		t.Fatal("pointed at, the row shows its hover widgets and the button has the focus")
	}
	for _, at := range [][2]float32{{590, 390}, {in.X + 2, in.Y + 2}, {590, 390}} {
		tt.Move(at[0], at[1])
		tt.Frame()
		tt.Frame()
		if !tt.Focused("Open") {
			t.Fatalf("with the pointer at %v, the button lost the focus; the window shows %q", at, tt.Texts())
		}
	}
	if tt.HasText("Star") || !tt.HasText("idle") {
		t.Fatalf("left, the row shows its idle widgets alone: %q", tt.Texts())
	}
}

// foldList is a list of the items given, each 30 tall and 2 apart, in a
// box of height.
func foldList(height int, items ...string) string {
	return `{"body": {"type": "list", "id": "l", "label": "Folds", "height": ` + strconv.Itoa(height) + `, "rowHeight": 30, "gap": 2,
		"onChange": "chose", "items": [` + strings.Join(items, ",") + `]}}`
}

// foldItems are the header's item, then the rows keys name, in the
// section sec, "" for none.
func foldItems(header, sec string, keys ...string) []string {
	out := []string{`{"key": "` + header + `", "label": "Head ` + header + `", "header": true}`}
	for _, k := range keys {
		s := ""
		if sec != "" {
			s = `, "section": "` + sec + `"`
		}
		out = append(out, `{"key": "`+k+`", "label": "Row `+k+`"`+s+`}`)
	}
	return out
}

// foldRows draws each item's row as "text " and its key.
func foldRows(_ string, it widget.ListItem) *widget.Node {
	return &widget.Node{Type: "text", Text: "text " + it.Key}
}

// foldTester draws s's view with st in a window the lists fit.
func foldTester(s *screen, st Style) *ui.Tester {
	return ui.NewTester(func(c *ui.Context) {
		s.taking()
		ui.Column(c).Fill().Padding(20).Children(func() {
			View(c, s.v, &s.cs, st, &s.md, func(ev plugins.UIEvent, values map[string]any) int {
				s.events = append(s.events, ev)
				return len(s.events)
			})
		})
	}, 600, 700)
}

// near says whether a and b are the same to a hundredth.
func near(a, b float32) bool { return a-b < 0.01 && b-a < 0.01 }

// A section folding shows of its rows what the curtain uncovered, from
// the top, clipped rather than squashed: a row it covers has no row, but
// the section's first, which stays at no height, keeping the header's
// spacing; the header above stays, the list is as tall as what shows,
// and the header under it moves with it.
func TestAFoldingSectionShowsWhatItUncovered(t *testing.T) {
	s := &screen{}
	items := append(foldItems("ha", "a", "a1", "a2", "a3"), foldItems("hb", "b", "b1", "b2")...)
	s.show(t, foldList(400, items...))
	p, asked := float32(1), map[string]bool{}
	var hooked, waited []string
	st := Style{
		Rows: func(list string, it widget.ListItem) *widget.Node {
			asked[it.Key] = true
			return foldRows(list, it)
		},
		Fold: func(l *widget.Node, section string) (float32, bool) {
			if section == "a" {
				return p, true
			}
			return 1, false
		},
		ListRow: func(c *ui.Context, l *widget.Node, i int, e ui.Element) bool {
			hooked = append(hooked, l.Items[i].Key)
			return false
		},
		WaitRows: func(list string, items []widget.ListItem) {
			waited = waited[:0]
			for _, it := range items {
				waited = append(waited, it.Key)
			}
		},
	}
	tt := foldTester(s, st)
	tt.Frame()
	list := func() ui.Rect {
		t.Helper()
		l, ok := tt.AccessByRole(ui.RoleList, "Folds")
		if !ok {
			t.Fatal("the list isn't drawn")
		}
		return l.Bounds
	}
	find := func(s string) ui.Rect {
		t.Helper()
		r, ok := tt.Find(s)
		if !ok {
			t.Fatalf("p %v: %q isn't drawn: %q", p, s, tt.Texts())
		}
		return r
	}
	open := list()
	headA, headB := find("text ha"), find("text hb")
	for _, c := range []struct {
		p       float32
		rows    map[string]float32 // the height each row of a shows
		covered []string
		shown   float32
	}{
		{0, map[string]float32{"a1": 0}, []string{"a2", "a3"}, 128},
		{0.25, map[string]float32{"a1": 22}, []string{"a2", "a3"}, 150},
		{0.5, map[string]float32{"a1": 30, "a2": 14}, []string{"a3"}, 174},
		{1, map[string]float32{"a1": 30, "a2": 30, "a3": 30}, nil, 222},
	} {
		p, hooked = c.p, nil
		clear(asked)
		tt.Frame()
		l := list()
		if !near(l.H, c.shown) {
			t.Errorf("p %v: the list is %v tall, want %v", c.p, l.H, c.shown)
		}
		for k, h := range c.rows {
			if h == 0 {
				continue // nothing to find: see below
			}
			if r := find("Row " + k); !near(r.H, h) {
				t.Errorf("p %v: row %s shows %v, want %v", c.p, k, r.H, h)
			}
			if h > 0 && !tt.HasText("text "+k) {
				t.Errorf("p %v: row %s shows without its content", c.p, k)
			}
		}
		if r, ok := tt.Find("Row a1"); c.p > 0 && (!ok || !near(r.Y-l.Y, 32)) {
			t.Errorf("p %v: the first row is at %v, want 32 under the list's top, as at rest", c.p, r.Y-l.Y)
		}
		if c.p == 0 && (asked["a1"] || tt.HasText("text a1")) {
			t.Errorf("p 0: the first row, at no height, drew its content")
		}
		if c.p == 0 {
			for _, a := range tt.AccessAll() {
				if strings.Contains(a.Name, "a1") {
					t.Errorf("p 0: the first row, at no height, is something to assistive tools: %+v", a)
				}
			}
		}
		for _, k := range c.covered {
			if _, ok := tt.Find("Row " + k); ok || asked[k] || slices.Contains(hooked, k) {
				t.Errorf("p %v: row %s, covered, is built (asked %v, hooked %v)", c.p, k, asked[k], hooked)
			}
		}
		if r := find("text ha"); r != headA {
			t.Errorf("p %v: the header above is at %+v, want %+v", c.p, r, headA)
		}
		if r := find("text hb"); !near(r.Y-headB.Y, l.H-open.H) || r.H != headB.H {
			t.Errorf("p %v: the header under it is at %v, moved %v, want moved %v", c.p, r.Y, r.Y-headB.Y, l.H-open.H)
		}
		if !slices.Contains(hooked, "hb") || !slices.Contains(hooked, "b2") {
			t.Errorf("p %v: the hook got %v, want each item drawn by its own index", c.p, hooked)
		}
		if c.p == 0 && !slices.Contains(waited, "a3") {
			t.Errorf("p 0: the rows about to be uncovered aren't drawn first: waited for %v", waited)
		}
	}
}

// A header naming a section, as the line under its rows telling of more,
// folds with it as a row does: clipped from the top, its text where it is
// with the section open, and gone once covered.
func TestAHeaderInASectionFoldsWithIt(t *testing.T) {
	s := &screen{}
	items := append(foldItems("ha", "a", "a1", "a2"), `{"key": "fa", "label": "Head fa", "header": true, "section": "a"}`)
	items = append(items, foldItems("hb", "", "b1")...)
	s.show(t, foldList(400, items...))
	p := float32(1)
	tt := foldTester(s, Style{Rows: foldRows, Fold: func(l *widget.Node, section string) (float32, bool) {
		return p, section == "a"
	}})
	tt.Frame()
	row, _ := tt.Find("Head fa")
	text, _ := tt.Find("text fa")
	at := text.Y - row.Y
	// The section is 96 tall: 72 of it leaves 6 of the header's 30 under
	// its gap; 48 covers it.
	p = 0.75
	tt.Frame()
	r, ok := tt.Find("Head fa")
	if !ok || !near(r.H, 6) {
		t.Fatalf("p 0.75: the header in the section shows %v (drawn %v), want 6", r.H, ok)
	}
	if x, ok := tt.Find("text fa"); !ok || !near(x.Y-r.Y, at) {
		t.Fatalf("p 0.75: the header's text is %v under its top, want %v, as with the section open", x.Y-r.Y, at)
	}
	p = 0.5
	tt.Frame()
	if _, ok := tt.Find("Head fa"); ok || tt.HasText("text fa") {
		t.Fatalf("p 0.5: the header in the section, covered, is built: %q", tt.Texts())
	}
}

// A section taller than the box takes as long to show what the box has
// room for as a short one does to show all of it: past that, rows are
// simply there.
func TestAFoldingSectionTallerThanTheBoxFoldsOverTheRoom(t *testing.T) {
	s := &screen{}
	// a's 3 rows stay; b's 10 fold, in a box with 42 of room for them.
	items := append(foldItems("ha", "a", "a1", "a2", "a3"), foldItems("hb", "b", "b1", "b2", "b3", "b4", "b5", "b6", "b7", "b8", "b9", "b10")...)
	s.show(t, foldList(200, items...))
	p, folding := float32(1), false
	tt := foldTester(s, Style{Rows: foldRows, Fold: func(l *widget.Node, section string) (float32, bool) {
		if section == "b" {
			return p, folding
		}
		return 1, false
	}})
	tt.Frame()
	tt.Frame()
	const fixed, room = 158, 42
	for _, c := range []struct{ p, b1, b2 float32 }{{0.5, 19, -1}, {1, 30, 8}} {
		p, folding = c.p, true
		tt.Frame()
		l, _ := tt.AccessByRole(ui.RoleList, "Folds")
		if !near(l.Bounds.H-fixed, c.p*room) {
			t.Errorf("p %v: the box shows %v of the section, want %v of its room", c.p, l.Bounds.H-fixed, c.p*room)
		}
		if r, _ := tt.Find("Row b1"); !near(r.H, c.b1) {
			t.Errorf("p %v: b1 shows %v, want %v", c.p, r.H, c.b1)
		}
		if r, ok := tt.Find("Row b2"); c.b2 < 0 && ok || c.b2 >= 0 && !near(r.H, c.b2) {
			t.Errorf("p %v: b2 shows %v (drawn %v), want %v", c.p, r.H, ok, c.b2)
		}
	}
}

// A section above others, in a box it overfills, folds over what the box
// shows of it under its top: open, the rows and the header under it stand
// where they stand at rest.
func TestAFoldingSectionAboveOthersFoldsOverWhatTheBoxShowsOfIt(t *testing.T) {
	var keys []string
	for i := range 10 {
		keys = append(keys, fmt.Sprintf("a%d", i+1))
	}
	items := append(foldItems("ha", "a", keys...), foldItems("hb", "", "b1", "b2", "b3")...)
	s := &screen{}
	s.show(t, foldList(300, items...))
	p, folding := float32(1), false
	tt := foldTester(s, Style{Rows: foldRows, Fold: func(l *widget.Node, section string) (float32, bool) { return p, folding }})
	tt.Frame()
	tt.Frame()
	// at is where each row shows, under the list's top, and how tall.
	at := func() map[string]ui.Rect {
		l, _ := tt.AccessByRole(ui.RoleList, "Folds")
		out := map[string]ui.Rect{}
		for _, k := range append(keys, "hb", "b1") {
			if r, ok := tt.Find("text " + k); ok && r.Y < l.Bounds.Y+l.Bounds.H {
				out[k] = ui.Rect{X: r.X - l.Bounds.X, Y: r.Y - l.Bounds.Y, W: r.W, H: r.H}
			}
		}
		return out
	}
	rest := at()
	// Under its header, the box shows 270 of the section.
	for _, c := range []struct {
		p    float32
		a5   float32 // what a5 shows, -1 for not drawn
		full int     // a's rows that show whole
	}{{0, -1, 0}, {0.5, 5, 4}, {1, 30, 8}} {
		p, folding = c.p, true
		tt.Frame()
		full := 0
		for _, k := range keys {
			if r, ok := tt.Find("Row " + k); ok && near(r.H, 30) {
				full++
			}
		}
		r, ok := tt.Find("Row a5")
		if full != c.full || c.a5 < 0 && ok || c.a5 >= 0 && !near(r.H, c.a5) {
			t.Errorf("p %v: %d of a's rows show whole, a5 shows %v (drawn %v); want %d and %v", c.p, full, r.H, ok, c.full, c.a5)
		}
	}
	last := at()
	if _, ok := last["hb"]; ok {
		t.Errorf("open, still folding: the header under the section shows in the box, at %+v, as it doesn't at rest", last["hb"])
	}
	folding = false
	tt.Frame()
	for k, r := range at() {
		if was, ok := last[k]; ok && was.Y != r.Y {
			t.Errorf("as the fold ends, %s moves from %v to %v", k, was.Y, r.Y)
		}
		if was, ok := rest[k]; !ok || was != r {
			t.Errorf("at rest after the fold, %s is at %+v, want %+v", k, r, was)
		}
	}
}

// What the box shows of a section as it begins to fold holds till it
// stops: the list scrolled meanwhile uncovers no more of it at the same p.
func TestAFoldsReachHoldsAsTheListScrolls(t *testing.T) {
	var keys []string
	for i := range 10 {
		keys = append(keys, fmt.Sprintf("a%d", i+1))
	}
	items := append(foldItems("ha", "a", keys...), foldItems("hb", "", "b1", "b2", "b3")...)
	s := &screen{}
	s.show(t, foldList(300, items...))
	p, folding := float32(1), false
	tt := foldTester(s, Style{Rows: foldRows, Fold: func(l *widget.Node, section string) (float32, bool) { return p, folding }})
	tt.Frame()
	tt.Frame()
	// uncovered is the last row of the section drawn, and what it shows.
	uncovered := func() (last string, h float32) {
		for _, k := range keys {
			if r, ok := tt.Find("Row " + k); ok {
				last, h = k, r.H
			}
		}
		return last, h
	}
	p, folding = 0.75, true
	tt.Frame()
	tt.Frame()
	before, beforeH := uncovered()
	// Scrolled down, the box shows more of the section under its top:
	// the curtain goes on to it.
	l, _ := tt.AccessByRole(ui.RoleList, "Folds")
	tt.Scroll(l.Bounds.X+10, l.Bounds.Y+100, 0, 20)
	tt.Frame()
	tt.Frame()
	was, wasH := uncovered()
	if was == before && near(wasH, beforeH) {
		t.Fatalf("scrolled down mid-fold, the curtain stands at %s, %v of it shown, as before", was, wasH)
	}
	// Scrolled back, the box shows less of it: the curtain stays.
	tt.Scroll(l.Bounds.X+10, l.Bounds.Y+100, 0, -20)
	tt.Frame()
	tt.Frame()
	if r, _ := tt.Find("text ha"); r.Y < l.Bounds.Y {
		t.Fatalf("the list didn't scroll back: its header is at %v, the list at %v", r.Y, l.Bounds.Y)
	}
	if got, h := uncovered(); got != was || !near(h, wasH) {
		t.Fatalf("scrolled back mid-fold, the curtain stands at %s, %v of it shown, as it stood at %s, %v shown, before", got, h, was, wasH)
	}
}

// showing is where each row of the labels given shows in the box, under
// its top, and how much of it, clipped to the box.
func showing(tt *ui.Tester, labels ...string) map[string][2]float32 {
	l, _ := tt.AccessByRole(ui.RoleList, "Folds")
	b := l.Bounds
	out := map[string][2]float32{}
	for _, x := range labels {
		r, ok := tt.Find(x)
		if top, bottom := max(r.Y, b.Y), min(r.Y+r.H, b.Y+b.H); ok && bottom > top {
			out[x] = [2]float32{top - b.Y, bottom - top}
		}
	}
	return out
}

// still says what moved, came or went from was to now.
func still(t *testing.T, what string, was, now map[string][2]float32) {
	t.Helper()
	for k, a := range was {
		if b, ok := now[k]; !ok || !near(a[0], b[0]) || !near(a[1], b[1]) {
			t.Errorf("%s: %s shows at %v, then %v (shown %v)", what, k, a, b, ok)
		}
	}
	for k, b := range now {
		if _, ok := was[k]; !ok {
			t.Errorf("%s: %s comes, at %v", what, k, b)
		}
	}
}

// labelsOf are the labels of the rows of items, made as foldItems does.
func labelsOf(items []string) []string {
	var out []string
	for _, it := range items {
		_, rest, _ := strings.Cut(it, `"label": "`)
		label, _, _ := strings.Cut(rest, `"`)
		out = append(out, label)
	}
	return out
}

// The list's height follows p without a step: as the curtain reaches a
// row, the gap it brings shows as fast as the curtain goes, whatever the
// gap, from shut to open.
func TestAFoldingListsHeightHasNoStep(t *testing.T) {
	for _, gap := range []string{"2", "12"} {
		items := append(foldItems("ha", "a", "a1", "a2", "a3"), foldItems("hb", "", "b1")...)
		s := &screen{}
		s.show(t, strings.Replace(foldList(400, items...), `"gap": 2,`, `"gap": `+gap+`,`, 1))
		p := float32(0)
		tt := foldTester(s, Style{Rows: foldRows, Fold: func(l *widget.Node, section string) (float32, bool) { return p, true }})
		height := func() float32 {
			tt.Frame()
			l, _ := tt.AccessByRole(ui.RoleList, "Folds")
			return l.Bounds.H
		}
		const steps = 1000
		shut := height()
		was, most, at := shut, float32(0), float32(0)
		for i := 1; i <= steps; i++ {
			p = float32(i) / steps
			h := height()
			if h < was {
				t.Fatalf("gap %s, p %v: the list shrinks as it opens, %v to %v", gap, p, was, h)
			}
			if h-was > most {
				most, at = h-was, p
			}
			was = h
		}
		smooth := (was - shut) / steps
		if most > 3*smooth {
			t.Errorf("gap %s: the list steps by %v at p %v; it moves %v a step smoothly", gap, most, at, smooth)
		}
		p = 0
		if h := height(); !near(h, shut) {
			t.Errorf("gap %s: shut again, the list is %v, want %v", gap, h, shut)
		}
	}
}

// The box shows its rows through its padding: a section filling it, as
// it begins to close, shows all it showed at rest.
func TestAFoldUncoversTheBoxThroughItsPadding(t *testing.T) {
	var keys []string
	for i := range 20 {
		keys = append(keys, fmt.Sprintf("a%d", i+1))
	}
	items := append(foldItems("ha", "a", keys...), foldItems("hb", "", "b1", "b2")...)
	s := &screen{}
	s.show(t, strings.Replace(foldList(300, items...), `"gap": 2,`, `"gap": 2, "padding": [6, 8],`, 1))
	p, folding := float32(1), false
	tt := foldTester(s, Style{Rows: foldRows, Fold: func(l *widget.Node, section string) (float32, bool) { return p, folding }})
	tt.Frame()
	tt.Frame()
	rest := showing(tt, labelsOf(items)...)
	folding = true
	tt.Frame()
	still(t, "at rest, then closing at 1", rest, showing(tt, labelsOf(items)...))
}

// A shut section's header, a gap taller with no rows under it, shrinks
// as the section opens: the opening ends where the section rests.
func TestAnOpeningSectionsHeaderShrinkingEndsWhereItRests(t *testing.T) {
	var keys []string
	for i := range 20 {
		keys = append(keys, fmt.Sprintf("a%d", i+1))
	}
	shut := append([]string{`{"key": "ha", "label": "Head ha", "header": true, "height": 32}`}, foldItems("hb", "", "b1")...)
	open := append(foldItems("ha", "a", keys...), foldItems("hb", "", "b1")...)
	s := &screen{}
	s.show(t, foldList(300, shut...))
	p, folding := float32(0), false
	tt := foldTester(s, Style{Rows: foldRows, Fold: func(l *widget.Node, section string) (float32, bool) { return p, folding }})
	tt.Frame()
	tt.Frame()
	s.show(t, foldList(300, open...))
	folding = true
	for _, x := range []float32{0, 0.3, 0.7, 1} {
		p = x
		tt.Frame()
	}
	last := showing(tt, labelsOf(open)...)
	folding = false
	tt.Frame()
	still(t, "open, then at rest", last, showing(tt, labelsOf(open)...))
}

// Two sections folding at once, one above closing as the one under it
// opens, past the box: the one under it ends where it rests.
func TestTwoFoldsAtOnceEndWhereTheyRest(t *testing.T) {
	var keys []string
	for i := range 20 {
		keys = append(keys, fmt.Sprintf("b%d", i+1))
	}
	a := foldItems("ha", "a", "a1", "a2", "a3", "a4", "a5")
	b := foldItems("hb", "b", keys...)
	c := foldItems("hc", "", "c1", "c2")
	join := func(xs ...[]string) (out []string) {
		for _, x := range xs {
			out = append(out, x...)
		}
		return out
	}
	pa, pb, fa, fb := float32(1), float32(0), false, false
	s := &screen{}
	s.show(t, foldList(300, join(a, b[:1], c)...))
	tt := foldTester(s, Style{Rows: foldRows, Fold: func(l *widget.Node, section string) (float32, bool) {
		if section == "a" {
			return pa, fa
		}
		return pb, fb
	}})
	tt.Frame()
	tt.Frame()
	// a begins to close; b begins to open, its rows come; a shuts, its
	// rows go; b opens.
	fa = true
	for _, x := range []float32{1, 0.8, 0.6} {
		pa = x
		tt.Frame()
	}
	s.show(t, foldList(300, join(a, b, c)...))
	fb = true
	for _, x := range [][2]float32{{0.4, 0}, {0.2, 0.2}, {0, 0.4}} {
		pa, pb = x[0], x[1]
		tt.Frame()
	}
	fa = false
	s.show(t, foldList(300, join(a[:1], b, c)...))
	for _, x := range []float32{0.6, 0.8, 1} {
		pb = x
		tt.Frame()
	}
	all := labelsOf(join(a, b, c))
	last := showing(tt, all...)
	fb = false
	tt.Frame()
	still(t, "b open, then at rest", last, showing(tt, all...))
}

// A list that grows folds over what it showed of a section under the
// section's top, as one of a height does.
func TestAGrowingListFoldsOverWhatItShows(t *testing.T) {
	var keys []string
	for i := range 50 {
		keys = append(keys, fmt.Sprintf("a%d", i+1))
	}
	items := append(foldItems("ha", "a", keys...), foldItems("hb", "", "b1")...)
	s := &screen{}
	s.show(t, `{"body": {"type": "column", "height": 300, "children": [{"type": "list", "id": "l", "label": "Folds", "grow": true, "rowHeight": 30, "gap": 2,
		"onChange": "chose", "items": [`+strings.Join(items, ",")+`]}]}}`)
	p, folding := float32(1), false
	tt := foldTester(s, Style{Rows: foldRows, Fold: func(l *widget.Node, section string) (float32, bool) { return p, folding }})
	tt.Frame()
	tt.Frame()
	rest := showing(tt, labelsOf(items)...)
	folding = true
	tt.Frame()
	still(t, "at rest, then closing at 1", rest, showing(tt, labelsOf(items)...))
	// 135 of the 270 it shows under the header: 4 rows whole and 5 of a5.
	p = 0.5
	tt.Frame()
	if r, ok := tt.Find("Row a5"); !ok || !near(r.H, 5) {
		t.Fatalf("p 0.5: a5 shows %v (drawn %v), want 5", r.H, ok)
	}
}

// A section above more than the box shows still folds over the whole of
// p, over what the box shows of it.
func TestAFoldingSectionAboveAFullBoxFoldsOverAllOfIt(t *testing.T) {
	var a, b []string
	for i := range 200 {
		a = append(a, fmt.Sprintf("a%d", i+1))
	}
	for i := range 20 {
		b = append(b, fmt.Sprintf("b%d", i+1))
	}
	items := append(foldItems("ha", "a", a...), foldItems("hb", "", b...)...)
	s := &screen{}
	s.show(t, foldList(300, items...))
	p, folding := float32(1), false
	tt := foldTester(s, Style{Rows: foldRows, Fold: func(l *widget.Node, section string) (float32, bool) { return p, folding && section == "a" }})
	tt.Frame()
	tt.Frame()
	p, folding = 0, true
	tt.Frame()
	p = 0.5
	tt.Frame()
	// 135 of the 270 the box shows of it: 4 rows whole and 5 of a5.
	r, ok := tt.Find("Row a5")
	if _, six := tt.Find("Row a6"); !ok || !near(r.H, 5) || six {
		t.Fatalf("p 0.5: a5 shows %v (drawn %v), a6 drawn %v: want half of what the box shows of the section uncovered", r.H, ok, six)
	}
	if _, ok := tt.Find("text hb"); !ok {
		t.Fatal("p 0.5: the header under the section isn't drawn: the curtain went past what the box shows")
	}
}

// The keys go on from a choice the curtain covers.
func TestTheKeysGoOnFromAChoiceACurtainCovers(t *testing.T) {
	items := append(foldItems("ha", "a", "a1", "a2", "a3"), foldItems("hb", "", "b1")...)
	s := &screen{}
	s.show(t, strings.Replace(foldList(400, items...), `"onChange": "chose",`, `"onChange": "chose", "onSubmit": "opened",`, 1))
	p := float32(1)
	tt := foldTester(s, Style{Rows: foldRows, Fold: func(l *widget.Node, section string) (float32, bool) { return p, true }})
	tt.Frame()
	if err := tt.Click("Row a2"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	p = 0
	tt.Frame()
	tt.Frame()
	tt.Key(0, ui.KeyDown)
	tt.Frame()
	if ev := s.last(t); ev.Type != "change" || ev.Value != "b1" {
		t.Fatalf("Down from a covered choice: %+v, want b1 chosen", ev)
	}
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if ev := s.last(t); ev.Type != "submit" || ev.Value != "b1" {
		t.Fatalf("Enter then: %+v, want b1 submitted", ev)
	}
}

// Nothing folding, a list draws as it did before sections: with no hook,
// with one that says nothing folds, and with items naming sections.
func TestAListFoldsNothingWhereNoSectionFolds(t *testing.T) {
	appRow := func(c *ui.Context, key any, selected bool) ui.Element {
		return ui.ButtonBase(c.Key(key)).Gap(8).AlignItems(ui.Center).Justify(ui.Start).Padding(6, 8).Radius(7)
	}
	plain := append(foldItems("ha", "", "a1", "a2"), foldItems("hb", "", "b1")...)
	named := append(foldItems("ha", "a", "a1", "a2"), foldItems("hb", "b", "b1")...)
	type box struct {
		text string
		r    ui.Rect
	}
	draw := func(row func(*ui.Context, any, bool) ui.Element, items []string, fold func(*widget.Node, string) (float32, bool)) []box {
		s := &screen{}
		s.show(t, foldList(300, items...))
		tt := foldTester(s, Style{Rows: foldRows, Row: row, Fold: fold})
		tt.Frame()
		tt.Frame()
		var out []box
		for _, x := range tt.Texts() {
			r, _ := tt.Find(x)
			out = append(out, box{x, r})
		}
		l, _ := tt.AccessByRole(ui.RoleList, "Folds")
		return append(out, box{"list", l.Bounds})
	}
	none := func(*widget.Node, string) (float32, bool) { return 0.5, false }
	for _, row := range []func(*ui.Context, any, bool) ui.Element{nil, appRow} {
		today := draw(row, plain, nil)
		for name, got := range map[string][]box{
			"a hook folding nothing":             draw(row, plain, none),
			"items in sections, no hook":         draw(row, named, nil),
			"items in sections, nothing folding": draw(row, named, none),
		} {
			if !slices.Equal(got, today) {
				t.Errorf("app row %v, %s: drew\n%v\nwant\n%v", row != nil, name, got, today)
			}
		}
	}
}

// A covered item doesn't scroll into view; one past it does, where its
// row is.
func TestRevealSkipsACoveredItem(t *testing.T) {
	var keys []string
	for i := range 20 {
		keys = append(keys, fmt.Sprintf("a%d", i))
	}
	items := append(foldItems("ha", "", keys...), foldItems("hb", "b", "b1", "b2", "b3", "b4", "b5")...)
	items = append(items, foldItems("hc", "", "c1", "c2", "c3", "c4", "c5")...)
	s := &screen{}
	s.show(t, foldList(100, items...))
	reveal := "b3"
	tt := foldTester(s, Style{
		Rows:   foldRows,
		Fold:   func(l *widget.Node, section string) (float32, bool) { return 0, true },
		Reveal: func(l *widget.Node, it widget.ListItem) bool { return it.Key == reveal },
	})
	tt.Frame()
	tt.Frame()
	if !tt.HasText("text a0") || tt.HasText("text c3") {
		t.Fatalf("revealing a covered item scrolled the list: %q", tt.Texts())
	}
	reveal = "c3"
	tt.Frame()
	tt.Frame()
	l, _ := tt.AccessByRole(ui.RoleList, "Folds")
	if r, ok := tt.Find("Row c3"); !ok || r.H != 30 || r.Y < l.Bounds.Y || r.Y+r.H > l.Bounds.Y+l.Bounds.H+0.01 {
		t.Fatalf("revealing c3, past the covered rows: it shows at %+v (drawn %v) in the list at %+v", r, ok, l.Bounds)
	}
}

// The keys move the choice past a section's covered items, as past a
// header.
func TestMovesSkipCoveredItems(t *testing.T) {
	items := append(foldItems("ha", "", "a1", "a2"), foldItems("hb", "b", "b1", "b2", "b3")...)
	items = append(items, foldItems("hc", "", "c1")...)
	items = append(items, foldItems("hd", "d", "d1", "d2")...)
	s := &screen{}
	s.show(t, foldList(400, items...))
	tt := foldTester(s, Style{Rows: foldRows, Fold: func(l *widget.Node, section string) (float32, bool) { return 0, true }})
	tt.Frame()
	if err := tt.Click("Row a2"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	for _, step := range []struct {
		key  ui.Key
		want string
	}{{ui.KeyDown, "c1"}, {ui.KeyUp, "a2"}, {ui.KeyEnd, "c1"}} {
		tt.Key(0, step.key)
		tt.Frame()
		if ev := s.last(t); ev.Type != "change" || ev.Value != step.want {
			t.Fatalf("after %v: %+v, want the choice at %s", step.key, ev, step.want)
		}
	}
}
