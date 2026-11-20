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
