package render

import (
	"encoding/json"
	"fmt"
	"image"
	"strings"
	"testing"
	"time"

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
	leaves := []string{
		`{"type": "text", "text": "t"}`, `{"type": "text", "id": "t1", "text": "t", "style": "code", "color": "$accent", "selectable": true}`,
		`{"type": "markdown", "text": "**m**"}`, `{"type": "badge", "text": "b", "style": "danger"}`, `{"type": "badge", "id": "b1", "text": "b", "color": "$success"}`,
		`{"type": "divider"}`, `{"type": "spacer", "id": "sp"}`, `{"type": "spinner"}`,
		`{"type": "button", "id": "b2", "label": "B", "style": "primary", "onClick": "h"}`, `{"type": "button", "id": "b3", "label": "B", "style": "danger", "disabled": true}`,
		`{"type": "input", "id": "i1", "label": "L", "value": "v", "onChange": "h", "onSubmit": "h"}`, `{"type": "input", "id": "i2", "multiline": true, "controlled": true, "value": ""}`,
		`{"type": "toggle", "id": "g1", "label": "G", "value": true}`,
		`{"type": "select", "id": "s1", "label": "S", "options": ["a", "b"], "value": "b"}`, `{"type": "select", "id": "s2", "options": ["a"]}`,
		`{"type": "tabs", "id": "tb", "options": [{"value": "x", "label": "X"}, "y"], "value": "y", "onChange": "h"}`,
		`{"type": "menu", "id": "mn", "label": "More", "options": ["edit", "delete"], "onChange": "h"}`,
		`{"type": "dialog", "id": "dl", "title": "Sure?", "open": true, "onClose": "h", "children": [{"type": "text", "text": "in"}]}`,
		`{"type": "dialog", "id": "dl2", "title": "Closed"}`,
		`{"type": "table", "columns": [{"id": "a", "title": "A"}, {"id": "b", "title": "B", "width": 80}], "rows": [{"key": "1", "cells": {"a": "x"}}, {"key": "2", "cells": {"b": "y"}}]}`,
		`{"type": "image", "src": "plugin:x.png", "width": 20, "radius": 4}`, `{"type": "image", "id": "im", "src": "https://example.com/x.png"}`,
		`{"type": "icon", "name": "check"}`, `{"type": "icon", "id": "ic", "name": "nope", "color": "$warning"}`,
	}
	all := strings.Join(leaves, ",")
	for _, box := range []string{`"column", "background": "$surface"`, `"row"`, `"card", "title": "C"`} {
		s := &screen{}
		view := `{"body": {"type": ` + box + `, "children": [` + all + `]}}`
		s.show(t, view)
		tt := ui.NewTester(s.draw, 900, 3000)
		tt.Frame()
		s.show(t, view)
		tt.Frame()
	}
}

func TestTabsMenusAndDialogsDispatch(t *testing.T) {
	s := &screen{}
	s.show(t, `{"body": {"type": "column", "children": [
		{"type": "tabs", "id": "tb", "options": [{"value": "one", "label": "One"}, {"value": "two", "label": "Two"}], "value": "one", "onChange": "tab"},
		{"type": "menu", "id": "mn", "label": "More", "options": [{"value": "rm", "label": "Remove"}], "onChange": "menu"},
		{"type": "table", "columns": [{"id": "n", "title": "Name"}], "rows": [{"key": "r", "cells": {"n": "Ada"}}]},
		{"type": "dialog", "id": "dl", "title": "Sure?", "open": true, "onClose": "closed", "children": [{"type": "text", "text": "Inside"}]}]}}`)
	tt := ui.NewTester(s.draw, 900, 700)
	tt.Frame()
	for _, want := range []string{"One", "Two", "More", "Name", "Ada", "Sure?", "Inside"} {
		if !tt.HasText(want) {
			t.Errorf("no %q in %q", want, tt.Texts())
		}
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if ev := s.last(t); ev.Type != "close" || ev.Handler != "closed" || ev.Node != "dl" {
		t.Fatalf("dismissing: %+v", ev)
	}
	if tt.HasText("Inside") {
		t.Fatal("a dismissed dialog still shows")
	}
	if err := tt.Click("Two"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if ev := s.last(t); ev.Type != "change" || ev.Handler != "tab" || ev.Value != "two" {
		t.Fatalf("tabs: %+v", ev)
	}
	if err := tt.Click("More"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.ChooseMenuItem("Remove"); err != nil {
		t.Fatalf("%v: %q", err, tt.Menu())
	}
	tt.Frame()
	if ev := s.last(t); ev.Type != "change" || ev.Handler != "menu" || ev.Value != "rm" {
		t.Fatalf("menu: %+v", ev)
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

// Two views' dialogs of one id are two dialogs: their overlays are keyed
// by their views' scopes.
func TestDialogsOfTwoViewsDontMeet(t *testing.T) {
	v, err := widget.Decode([]byte(`{"body": {"type": "dialog", "id": "d", "title": "Sure?", "open": true, "children": [{"type": "text", "text": "x"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	var one, two Controls
	one.Sync(v, 1, 0)
	two.Sync(v, 1, 0)
	var md markdown.Options
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Children(func() {
			View(c, v, &one, Style{Scope: "a/home"}, &md, nil)
			View(c, v, &two, Style{Scope: "b/home"}, &md, nil)
		})
	}, 800, 600)
	tt.Frame()
}

// An item fires its click and shows its actions while pointed at; a
// segmented tabs' disabled option can't be chosen; a native widget draws
// only where the host draws it, and its changes reach the plugin.
func TestItemsSegmentsAndNatives(t *testing.T) {
	s := &screen{}
	s.show(t, `{"body": {"type": "column", "children": [
		{"type": "item", "id": "f", "icon": "file", "text": "a.go", "style": "code", "label": "Diff of a.go", "onClick": "show",
			"trailing": [{"type": "text", "text": "+1", "size": "small"}],
			"actions": [{"type": "button", "id": "copy", "style": "ghost", "size": "small", "icon": "copy", "label": "Copy path", "tooltip": "Copy path", "onClick": "copy"}]},
		{"type": "tabs", "id": "layout", "style": "segmented", "value": "unified", "onChange": "layout",
			"options": [{"value": "unified", "label": "Unified"}, {"value": "split", "label": "Split", "disabled": true, "tooltip": "No room"}]},
		{"type": "native", "id": "n", "name": "diff.body", "props": {"path": "a.go"}, "onChange": "marked"}
	]}}`)
	tt := ui.NewTester(s.draw, 800, 700)
	tt.Frame()
	if !tt.HasText("+1") || tt.HasText("Copy path") {
		t.Fatalf("an item not pointed at shows %q, want its trailing text and no actions", tt.Texts())
	}
	if !tt.HasText("diff.body draws only in a component of Glitched's.") {
		t.Errorf("a native outside a component shows %q", tt.Texts())
	}
	if err := tt.Click("Diff of a.go"); err != nil {
		t.Fatal(err)
	}
	if ev := s.last(t); ev.Handler != "show" || ev.Type != "click" {
		t.Errorf("the item fired %+v", ev)
	}
	r, _ := tt.Find("Diff of a.go")
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	tt.Frame()
	if _, ok := tt.Find("Copy path"); !ok {
		t.Errorf("an item pointed at shows %q, want its actions", tt.Texts())
	}
	n := len(s.events)
	tt.Click("Split")
	if len(s.events) != n {
		t.Errorf("a disabled option fired %+v", s.last(t))
	}
	// The option shown is chosen again: the plugin may hold another.
	if err := tt.Click("Unified"); err != nil {
		t.Fatal(err)
	}
	if ev := s.last(t); ev.Handler != "layout" || ev.Value != "unified" {
		t.Errorf("choosing the option shown fired %+v", ev)
	}

	var drawn []string
	native := Style{Native: func(c *ui.Context, n *widget.Node, _ string, fire func(any)) {
		drawn = append(drawn, n.Name+" "+string(n.Props["path"]))
		if ui.Button(c, "Mark").Clicked() {
			fire(map[string]any{"remaining": 2})
		}
	}}
	s2 := &screen{}
	s2.show(t, `{"body": {"type": "native", "id": "n", "name": "diff.body", "props": {"path": "a.go"}, "onChange": "marked"}}`)
	tt2 := ui.NewTester(func(c *ui.Context) {
		s2.cs.Sync(s2.v, s2.rev, s2.processed)
		View(c, s2.v, &s2.cs, native, &s2.md, func(ev plugins.UIEvent, values map[string]any) int {
			s2.events = append(s2.events, ev)
			return len(s2.events)
		})
	}, 400, 300)
	tt2.Frame()
	if len(drawn) == 0 || drawn[0] != `diff.body "a.go"` {
		t.Fatalf("the host drew %q", drawn)
	}
	if err := tt2.Click("Mark"); err != nil {
		t.Fatal(err)
	}
	if ev := s2.last(t); ev.Handler != "marked" || ev.Type != "change" || fmt.Sprint(ev.Value) != "map[remaining:2]" {
		t.Errorf("the native fired %+v", ev)
	}
}

// A menu's separator is a line between its options; a progress button,
// once clicked, runs till a view answers after the click.
func TestMenuSeparatorsAndProgressButtons(t *testing.T) {
	s := &screen{}
	view := `{"body": {"type": "row", "children": [
		{"type": "menu", "id": "m", "label": "Open", "options": ["a", {"separator": true}, "b"], "onChange": "open"},
		{"type": "button", "id": "go", "label": "Go", "progress": true, "onClick": "go"}
	]}}`
	s.show(t, view)
	tt := ui.NewTester(s.draw, 800, 300)
	tt.Frame()
	if err := tt.Click("Open"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(tt.Menu(), "|"); got != "a||b" && got != "a|-|b" {
		t.Errorf("the menu has %q, want a, a line, b", tt.Menu())
	}
	tt.CloseMenu()
	if err := tt.Click("Go"); err != nil {
		t.Fatal(err)
	}
	n := len(s.events)
	tt.Click("Go")
	tt.Frame()
	if len(s.events) != n {
		t.Error("a running progress button fired again")
	}
	s.processed = n
	s.show(t, view)
	tt.Frame()
	if err := tt.Click("Go"); err != nil || len(s.events) != n+1 {
		t.Errorf("once answered, the button doesn't fire: %v, %d events", err, len(s.events))
	}
}

// A triple-click on a progress button is one click, though the plugin
// answers each click before the next comes; a click after it is one again.
func TestATripleClickOnAProgressButtonIsOneClick(t *testing.T) {
	s := &screen{}
	view := `{"body": {"type": "button", "id": "go", "label": "Go", "progress": true, "onClick": "go"}}`
	s.show(t, view)
	tt := ui.NewTester(s.draw, 800, 300)
	tt.Frame()
	for range 3 {
		if err := tt.Click("Go"); err != nil {
			t.Fatal(err)
		}
		s.processed = len(s.events)
		s.show(t, view)
		tt.Frame()
	}
	if len(s.events) != 1 {
		t.Fatalf("a triple-click fired %d clicks, want 1", len(s.events))
	}
	tt.Advance(time.Second)
	if err := tt.Click("Go"); err != nil || len(s.events) != 2 {
		t.Fatalf("a click after it: %v, %d clicks, want 2", err, len(s.events))
	}
}

// Progress buttons side by side, each clicked once inside a double-click's
// time and place, are a click each: the second click begins its own
// button's clicks, as the system counted it the second of the window's.
func TestNeighbouringProgressButtonsClickedQuicklyAreAClickEach(t *testing.T) {
	s := &screen{}
	view := `{"body": {"type": "row", "gap": 2, "children": [
		{"type": "button", "id": "one", "label": "One", "progress": true, "onClick": "one"},
		{"type": "button", "id": "two", "label": "Two", "progress": true, "onClick": "two"}
	]}}`
	s.show(t, view)
	tt := ui.NewTester(s.draw, 800, 300)
	tt.Frame()
	one, _ := tt.Find("One")
	two, _ := tt.Find("Two")
	mid, y := (one.X+one.W+two.X)/2, one.Y+one.H/2 // the 2 pt between the buttons
	tt.ClickAt(mid-2, y)
	s.processed = len(s.events)
	s.show(t, view)
	tt.Frame()
	tt.ClickAt(mid+2, y)
	var got []string
	for _, ev := range s.events {
		got = append(got, ev.Handler)
	}
	if strings.Join(got, " ") != "one two" {
		t.Fatalf("clicking One then Two, 4 pt apart, fired %q, want one, then two", got)
	}
}

// A second press on a progress button let go off it clicks nothing, and
// leaves the next Enter its click.
func TestAPressLetGoOffAProgressButtonLeavesEnterItsClick(t *testing.T) {
	s := &screen{}
	view := `{"body": {"type": "button", "id": "go", "label": "Go", "progress": true, "onClick": "go"}}`
	s.show(t, view)
	tt := ui.NewTester(s.draw, 800, 300)
	tt.Frame()
	r, _ := tt.Find("Go")
	x, y := r.X+r.W/2, r.Y+r.H/2
	tt.ClickAt(x, y)
	s.processed = len(s.events)
	s.show(t, view)
	tt.Frame()
	tt.Press(x, y)
	tt.Release(x, y+100)
	tt.Advance(time.Second)
	if !tt.Focused("Go") {
		t.Fatal("Go lost the focus")
	}
	tt.Key(0, ui.KeyEnter)
	if len(s.events) != 2 {
		t.Fatalf("the click, a press let go off Go, then Enter fired %d clicks, want 2", len(s.events))
	}
}

// A progress button keeps the focus while it runs and once answered: the
// spinner before it takes a key of its own.
func TestAProgressButtonKeepsTheFocus(t *testing.T) {
	s := &screen{}
	view := `{"body": {"type": "button", "id": "go", "label": "Go", "progress": true, "onClick": "go"}}`
	s.show(t, view)
	tt := ui.NewTester(s.draw, 800, 300)
	tt.Frame()
	if err := tt.Click("Go"); err != nil {
		t.Fatal(err)
	}
	if !tt.Focused("Go") {
		t.Fatal("running, the button lost the focus")
	}
	s.processed = len(s.events)
	s.show(t, view)
	tt.Frame()
	if !tt.Focused("Go") {
		t.Fatal("answered, the button lost the focus")
	}
}

// A border at borderOpacity 0 draws nothing; one with none set is opaque.
func TestABorderAtOpacityZeroIsClear(t *testing.T) {
	red := func(props string) int {
		t.Helper()
		v, err := widget.Decode([]byte(`{"body":{"type":"row","padding":16,"children":[{"type":"text","text":"label"}]` + props + `}}`))
		if err != nil {
			t.Fatal(err)
		}
		tt := ui.NewTester(func(c *ui.Context) {
			ui.Column(c).Fill().Padding(16).Background(ui.RGBA(0, 0, 0, 1)).Children(func() {
				View(c, v, &Controls{}, Style{Red: ui.RGBA(1, 0, 0, 1)}, nil, nil)
			})
		}, 160, 80)
		tt.Frame()
		img, n := tt.Image(), 0
		b := img.Bounds()
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				if r, g, b, a := img.At(x, y).RGBA(); r > 0 && g == 0 && b == 0 && a > 0 {
					n++
				}
			}
		}
		return n
	}
	if red("") != 0 || red(`,"border":"$danger"`) == 0 {
		t.Fatal("no border should draw no red, and a $danger one some")
	}
	if n := red(`,"border":"$danger","borderOpacity":0`); n != 0 {
		t.Errorf("a border at opacity 0 draws %d red pixels", n)
	}
}

// A sized column is as big as it says, a vertical divider is a line as
// tall as its row, and a popover's column is as wide as the panel holds,
// not wider: its width is the panel's.
func TestSizedBoxesVerticalDividersAndPopoverColumns(t *testing.T) {
	red := func(view string, st Style) (n int, box image.Rectangle) {
		t.Helper()
		v, err := widget.Decode([]byte(view))
		if err != nil {
			t.Fatal(err)
		}
		st.Border, st.Red = ui.RGBA(1, 0, 0, 1), ui.RGBA(1, 0, 0, 1)
		tt := ui.NewTester(func(c *ui.Context) {
			ui.Column(c).Fill().Background(ui.RGBA(0, 0, 0, 1)).Children(func() {
				View(c, v, &Controls{}, st, nil, nil)
			})
		}, 400, 200)
		tt.Frame()
		tt.Frame()
		img := tt.Image()
		b := img.Bounds()
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				if r, g, bl, a := img.At(x, y).RGBA(); r > 0 && g == 0 && bl == 0 && a > 0 {
					n++
					if px := image.Rect(x, y, x+1, y+1); n == 1 {
						box = px
					} else {
						box = box.Union(px)
					}
				}
			}
		}
		return n, box
	}
	for _, align := range []string{"", "center", "stretch"} {
		if _, box := red(`{"body": {"type": "row", "align": "`+align+`", "children": [
			{"type": "column", "width": 150, "height": 60},
			{"type": "divider", "style": "vertical"}]}}`, Style{}); box != image.Rect(150, 0, 151, 60) {
			t.Errorf("in a row aligned %q, the vertical divider after a 150 by 60 column covers %v, want x 150 for 60 points", align, box)
		}
	}
	if _, box := red(`{"body": {"type": "column", "width": 120, "height": 50, "border": "$danger"}}`, Style{}); box.Dx() != 120 || box.Dy() != 50 {
		t.Errorf("a 120 by 50 column's border covers %v", box)
	}
	pop := Style{Popover: func(c *ui.Context, anchor ui.Element, open *bool, width float32, body func()) {
		ui.Column(c).Width(width).Padding(20).Children(body)
	}}
	if _, box := red(`{"body": {"type": "button", "id": "b", "style": "ghost", "icon": "x", "open": true,
		"popover": {"type": "column", "width": 220, "border": "$danger"}}}`, pop); box.Dx() != 180 {
		t.Errorf("a popover's column in a 220-point panel padded by 20 covers %v, want 180 wide", box)
	}
}
