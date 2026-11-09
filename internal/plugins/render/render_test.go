package render

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"

	"glitched/internal/plugins"
	"glitched/internal/plugins/widget"
)

// screen draws one view, as a page does, and keeps what it dispatched.
type screen struct {
	v         *widget.View
	rev       int
	processed int
	cs        Controls
	events    []plugins.UIEvent
	values    []map[string]any
	md        markdown.Options
}

func (s *screen) show(t *testing.T, view string) {
	t.Helper()
	v, err := widget.Decode([]byte(view))
	if err != nil {
		t.Fatal(err)
	}
	s.v = v
	s.rev++
}

func (s *screen) draw(c *ui.Context) {
	s.cs.Sync(s.v, s.rev, s.processed)
	ui.Column(c).Fill().Padding(20).Children(func() {
		View(c, s.v, &s.cs, Style{Card: func(c *ui.Context, title string, body func()) {
			ui.Column(c).Children(func() {
				ui.Text(c, title)
				body()
			})
		}}, &s.md, func(ev plugins.UIEvent, values map[string]any) int {
			s.events = append(s.events, ev)
			s.values = append(s.values, values)
			return len(s.events)
		})
	})
}

func (s *screen) last(t *testing.T) plugins.UIEvent {
	t.Helper()
	if len(s.events) == 0 {
		t.Fatal("nothing dispatched")
	}
	return s.events[len(s.events)-1]
}

const form = `{"body": {"type": "column", "gap": 8, "children": [
	{"type": "card", "title": "Form", "children": [
		{"type": "text", "text": "Hello"},
		{"type": "markdown", "text": "Some **bold** words"},
		{"type": "badge", "text": "New", "style": "accent"},
		{"type": "input", "id": "name", "placeholder": "Name", "value": %s, "onChange": "typed", "onSubmit": "sent"},
		{"type": "toggle", "id": "pin", "label": "Pin it", "value": false, "onChange": "pinned"},
		{"type": "select", "id": "mode", "options": [{"value": "a", "label": "Apple"}, {"value": "b", "label": "Banana"}], "value": "a", "onChange": "picked"},
		{"type": "button", "id": "go", "label": "Go", "style": "primary", "onClick": {"handler": "go", "payload": {"n": 7}}},
		{"type": "button", "id": "off", "label": "Off", "disabled": true, "onClick": "off"}
	]}]}}`

func formWith(value string) string {
	b, _ := json.Marshal(value)
	return fmt.Sprintf(form, string(b))
}

func TestWidgetsDrawAndDispatchTheirHandlers(t *testing.T) {
	s := &screen{}
	s.show(t, formWith(""))
	tt := ui.NewTester(s.draw, 800, 700)
	tt.Frame()
	for _, want := range []string{"Form", "Hello", "New", "Pin it", "Apple", "Go", "Off"} {
		if !tt.HasText(want) {
			t.Errorf("no %q in %q", want, tt.Texts())
		}
	}
	if err := tt.Click("Go"); err != nil {
		t.Fatal(err)
	}
	ev := s.last(t)
	if ev.Type != "click" || ev.Handler != "go" || ev.Node != "go" || !jsonEqual(ev.Payload, `{"n":7}`) {
		t.Fatalf("click: %+v", ev)
	}
	n := len(s.events)
	if err := tt.Click("Off"); err != nil {
		t.Fatal(err)
	}
	if len(s.events) != n {
		t.Fatalf("a disabled button dispatched %+v", s.last(t))
	}
	if err := tt.Click("Pin it"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if ev := s.last(t); ev.Type != "change" || ev.Handler != "pinned" || ev.Value != true {
		t.Fatalf("toggle: %+v", ev)
	}
	if err := tt.Click("Apple"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.ChooseMenuItem("Banana"); err != nil {
		if err := tt.Click("Banana"); err != nil {
			t.Fatalf("%v; %q", err, tt.Texts())
		}
	}
	tt.Frame()
	if ev := s.last(t); ev.Type != "change" || ev.Handler != "picked" || ev.Value != "b" {
		t.Fatalf("select: %+v", ev)
	}
	if v := s.values[len(s.values)-1]; v["pin"] != true || v["name"] != "" || v["mode"] != "b" {
		t.Fatalf("values: %v", v)
	}
}

// typeName types into the form's input, in two goes.
func typeName(t *testing.T, tt *ui.Tester, first, rest string) {
	t.Helper()
	r, ok := tt.Find("New")
	if !ok {
		t.Fatal(tt.Texts())
	}
	tt.ClickAt(r.X+40, r.Y+r.H+20)
	tt.Type(first)
	tt.Frame()
	tt.Type(rest)
	tt.Frame()
}

func controlled(value string) string {
	return strings.Replace(formWith(value), `"onChange": "typed"`, `"controlled": true, "onChange": "typed"`, 1)
}

// An input whose onChange only validates keeps what is typed, however
// often the plugin answers with the same value.
func TestAnInputKeepsWhatIsTypedAcrossViews(t *testing.T) {
	s := &screen{}
	s.show(t, formWith(""))
	tt := ui.NewTester(s.draw, 800, 700)
	tt.Frame()
	typeName(t, tt, "A", "da")
	if ev := s.last(t); ev.Type != "change" || ev.Value != "Ada" || ev.Handler != "typed" {
		t.Fatalf("typing: %+v", ev)
	}
	for _, processed := range []int{0, 1, len(s.events)} {
		s.processed = processed
		s.show(t, formWith(""))
		tt.Frame()
		if got := s.cs.Values()["name"]; got != "Ada" {
			t.Fatalf("a view of the same value, %d taken, took the text to %q", processed, got)
		}
	}
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if ev := s.last(t); ev.Type != "submit" || ev.Value != "Ada" || ev.Handler != "sent" {
		t.Fatalf("submit: %+v", ev)
	}
	// A value the plugin changes is the value.
	s.processed = len(s.events)
	s.show(t, formWith("Bob"))
	tt.Frame()
	if got := s.cs.Values()["name"]; got != "Bob" {
		t.Fatalf("the plugin's value didn't take: %q", got)
	}
}

// A controlled input never loses typing the plugin hasn't taken, takes
// the plugin's own spelling of it once it has, and clears when the plugin
// clears it in the batch that took the submit.
func TestAControlledInputFollowsThePluginOnceItCaughtUp(t *testing.T) {
	s := &screen{}
	s.show(t, controlled(""))
	tt := ui.NewTester(s.draw, 800, 700)
	tt.Frame()
	typeName(t, tt, "a", "b")
	if n := len(s.events); n != 2 {
		t.Fatalf("%d events", n)
	}
	s.processed = 1
	s.show(t, controlled("A")) // derived from the first change, while "b" waits
	tt.Frame()
	if got := s.cs.Values()["name"]; got != "ab" {
		t.Fatalf("a view behind the typing took it to %q", got)
	}
	s.processed = 2
	s.show(t, controlled("AB"))
	tt.Frame()
	if got := s.cs.Values()["name"]; got != "AB" {
		t.Fatalf("the plugin's value, caught up: %q", got)
	}
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	s.processed = len(s.events)
	s.show(t, controlled(""))
	tt.Frame()
	if got := s.cs.Values()["name"]; got != "" {
		t.Fatalf("not cleared after the batch: %q", got)
	}
}

func TestControlsGoWithTheirWidgets(t *testing.T) {
	var cs Controls
	v := func(view string) *widget.View {
		w, err := widget.Decode([]byte(view))
		if err != nil {
			t.Fatal(err)
		}
		return w
	}
	cs.Sync(v(`{"body": {"type": "input", "id": "a", "value": "one"}}`), 1, 0)
	cs.m["a"].text = "typed"
	cs.Sync(v(`{"body": {"type": "text", "text": "gone"}}`), 2, 0)
	cs.Sync(v(`{"body": {"type": "input", "id": "a", "value": "one"}}`), 3, 0)
	if got := cs.Values()["a"]; got != "one" {
		t.Fatalf("an input back after it went kept %q", got)
	}
	cs.m["a"].text = "typed"
	cs.Sync(v(`{"body": {"type": "input", "id": "a", "value": "two"}}`), 3, 0)
	if got := cs.Values()["a"]; got != "typed" {
		t.Fatalf("the same view, synced twice, took %q", got)
	}
}

func jsonEqual(raw json.RawMessage, want string) bool {
	var a, b any
	return json.Unmarshal(raw, &a) == nil && json.Unmarshal([]byte(want), &b) == nil && fmt.Sprint(a) == fmt.Sprint(b)
}

// Every widget, in every style, with an id and without, in every
// container, draws: a view that checks can't take the window down.
func TestEveryWidgetDraws(t *testing.T) {
	var leaves []string
	for typ := range widget.Types {
		styles := map[string][]string{"text": {"body", "muted", "heading", "title", "code", "error", "success"}, "button": {"default", "primary", "danger"}, "badge": {"default", "accent", "success", "warning", "danger"}}[typ]
		if styles == nil {
			styles = []string{""}
		}
		for j, st := range styles {
			n := map[string]any{"type": typ, "text": "t", "label": "l"}
			if st != "" {
				n["style"] = st
			}
			switch typ {
			case "button":
				n["onClick"] = "h"
			case "input":
				n["onChange"], n["onSubmit"] = "h", "h"
			case "toggle":
				n["value"] = true
			case "select":
				n["options"] = []string{"a", "b"}
			}
			delete(n, "text")
			if typ == "text" || typ == "markdown" || typ == "badge" {
				n["text"] = "t"
			}
			if typ != "text" && typ != "markdown" && typ != "badge" && typ != "button" && typ != "input" && typ != "toggle" && typ != "select" {
				delete(n, "label")
			}
			if typ == "text" || typ == "markdown" || typ == "badge" || typ == "column" || typ == "row" || typ == "card" || typ == "divider" || typ == "spacer" || typ == "spinner" {
				delete(n, "label")
			}
			for _, withID := range []bool{true, false} {
				if !withID && (typ == "button" || typ == "input" || typ == "toggle" || typ == "select") {
					continue
				}
				m := map[string]any{}
				for k, v := range n {
					m[k] = v
				}
				if withID {
					m["id"] = fmt.Sprintf("%s-%d", typ, j)
				}
				b, _ := json.Marshal(m)
				leaves = append(leaves, string(b))
			}
		}
	}
	all := strings.Join(leaves, ",")
	for _, box := range []string{"column", "row", "card"} {
		s := &screen{}
		s.show(t, `{"body": {"type": "`+box+`", "children": [`+all+`]}}`)
		tt := ui.NewTester(s.draw, 900, 3000)
		tt.Frame()
		s.show(t, `{"body": {"type": "`+box+`", "children": [`+all+`]}}`)
		tt.Frame()
	}
}

// Generated keys can't meet a widget's: an input id'd x and a button
// id'd x#input draw side by side.
func TestKeysDontCollide(t *testing.T) {
	s := &screen{}
	s.show(t, `{"body": {"type": "column", "children": [
		{"type": "input", "id": "name"}, {"type": "input", "id": "name/input"},
		{"type": "select", "id": "s", "options": ["a"]}, {"type": "select", "id": "s/select", "options": ["b"]}]}}`)
	ui.NewTester(s.draw, 800, 700).Frame()
}
