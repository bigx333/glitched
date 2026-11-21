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
