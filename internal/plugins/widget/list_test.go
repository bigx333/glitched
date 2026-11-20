package widget

import (
	"fmt"
	"strings"
	"testing"
)

func listView(fields string) string {
	return `{"body": {"type": "column", "children": [
		{"type": "input", "id": "find"},
		{"type": "list", "id": "l", "label": "Threads", "grow": true` + fields + `}
	]}}`
}

func TestAListIsCheckedItemByItem(t *testing.T) {
	items := `, "items": [{"key": "h", "label": "Inbox", "header": true, "height": 30}, {"key": "a", "label": "Fix it", "version": "3", "thread": "t1"}, {"key": "b", "label": "Ship it"}]`
	ok := []string{
		listView(items),
		listView(items + `, "value": "a", "controlled": true, "rowHeight": 28, "gap": 2, "padding": [4, 8], "input": "find", "submitOnClick": true, "onChange": "chose", "onSubmit": "opened"`),
		`{"body": {"type": "list", "id": "l", "label": "L", "height": 200, "items": []}}`,
	}
	for _, s := range ok {
		if _, err := Decode([]byte(s)); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
	bad := map[string]string{
		listView(`, "items": [{"key": "a", "label": "A"}, {"key": "a", "label": "B"}]`):    `the key "a" is used twice`,
		listView(`, "items": [{"key": "", "label": "A"}]`):                                 "a key is 1 to 256 bytes",
		listView(`, "items": [{"key": "a"}]`):                                              "a label is 1 to 1024 bytes",
		listView(`, "items": [{"key": "a", "label": "A", "height": 401}]`):                 "a height is 8 to 400",
		listView(`, "items": [{"key": "a", "label": "A", "header": true, "thread": "t"}]`): "a header has no thread",
		listView(items + `, "value": "h"`):                                                 `value "h" is no item of the list, or a header`,
		listView(items + `, "value": "zz"`):                                                `value "zz" is no item`,
		listView(items + `, "value": 3`):                                                   "a list's value is the key",
		listView(items + `, "rowHeight": 4`):                                               "a rowHeight is 8 to 400",
		listView(items + `, "input": "nope"`):                                              `its input "nope" is no single-line input`,
		listView(items + `, "onClick": "x"`):                                               "a list has no onClick",
		`{"body": {"type": "list", "id": "l", "grow": true}}`:                              "a list needs a label",
		`{"body": {"type": "list", "id": "l", "label": "L"}}`:                              "a list grows or has a height",
		`{"body": {"type": "list", "label": "L", "grow": true}}`:                           "a list needs an id",
		`{"body": {"type": "column", "rowHeight": 20}}`:                                    "a column has no items, rowHeight",
		`{"body": {"type": "text", "text": "x", "when": "hover"}}`:                         "when is for a widget in a list's row",
		`{"body": {"type": "column", "children": [{"type": "input", "id": "i", "onSubmit": "s"}, {"type": "list", "id": "l", "label": "L", "grow": true, "input": "i"}]}}`:                                                       "without an onSubmit",
		`{"body": {"type": "column", "children": [{"type": "input", "id": "i"}, {"type": "list", "id": "l", "label": "L", "grow": true, "input": "i"}, {"type": "list", "id": "m", "label": "M", "grow": true, "input": "i"}]}}`: "the input \"i\" moves another list",
	}
	for view, want := range bad {
		if _, err := Decode([]byte(view)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", view, err, want)
		}
	}
}

// A view with a list may pass the bound of other views, as its items are
// data, which neither the widgets nor the depth count.
func TestAListOfTwentyThousandFitsAView(t *testing.T) {
	items := make([]string, MaxListItems)
	for i := range items {
		items[i] = fmt.Sprintf(`{"key": "thread-%05d", "label": "Thread number %d of the list", "version": "v%d"}`, i, i, i)
	}
	view := `{"body": {"type": "list", "id": "l", "label": "L", "grow": true, "items": [` + strings.Join(items, ",") + `]}}`
	if len(view) <= MaxBytes {
		t.Fatalf("the view is %d bytes: no bigger than another view may be", len(view))
	}
	if _, err := Decode([]byte(view)); err != nil {
		t.Fatal(err)
	}
	padded := `{"body": {"type": "column", "children": [` + strings.Repeat(`{"type": "text", "text": "`+strings.Repeat("x", 30000)+`"},`, 40) + `{"type": "list", "id": "l", "label": "L", "grow": true, "items": [{"key": "a", "label": "A"}]}]}}`
	if _, err := Decode([]byte(padded)); err == nil || !strings.Contains(err.Error(), "besides a list's items") {
		t.Errorf("a view over 1 MiB of widgets, with a list of one item: %v", err)
	}
	over := strings.Replace(view, "[", `[{"key": "one more", "label": "One more"},`, 1)
	if _, err := Decode([]byte(over)); err == nil || !strings.Contains(err.Error(), "up to 20000 items") {
		t.Errorf("20001 items: %v", err)
	}
}

func TestARowHoldsWhatScrollsAway(t *testing.T) {
	ok := `{"type": "row", "gap": 9, "padding": [0, 6, 0, 8], "children": [
		{"type": "text", "text": "Fix it"},
		{"type": "text", "text": "2m", "style": "muted", "when": "idle"},
		{"type": "button", "id": "settle", "style": "ghost", "icon": "check", "label": "Settle", "when": "hover", "onClick": {"handler": "settle", "payload": {"key": "a"}}}
	]}`
	if _, err := DecodeRow([]byte(ok)); err != nil {
		t.Fatal(err)
	}
	bad := map[string]string{
		`{"type": "input", "id": "i"}`: "a list's row holds no input",
		`{"type": "row", "children": [{"type": "toggle", "id": "t", "label": "T"}]}`: "a list's row holds no toggle",
		`{"type": "dialog", "id": "d", "title": "D"}`:                                "a list's row holds no dialog",
		`{"type": "list", "id": "l", "label": "L", "grow": true}`:                    "a list's row holds no list",
		`{"type": "native", "name": "diff.body"}`:                                    "a list's row holds no native",
		`{"type": "button", "id": "b", "label": "B", "progress": true}`:              "no popover or progress button",
		`{"type": "text", "text": "x", "when": "always"}`:                            `when is hover or idle`,
		`{"type": "text", "text": "x", "html": "<b>"}`:                               `unknown field "html"`,
		`null`: "no widget",
		`{"type": "button", "id": "b", "style": "ghost", "label": "B", "children": [null]}`: "a ghost button's children",
		`{"type": "text", "text": "` + strings.Repeat("x", MaxRowBytes) + `"}`:              "over the 16384 allowed",
	}
	for row, want := range bad {
		if _, err := DecodeRow([]byte(row)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", row, err, want)
		}
	}
	many := strings.Repeat(`{"type": "spacer"},`, MaxRowNodes)
	if _, err := DecodeRow([]byte(`{"type": "row", "children": [` + many[:len(many)-1] + `]}`)); err == nil || !strings.Contains(err.Error(), "over 200 widgets") {
		t.Errorf("a row of %d widgets: %v", MaxRowNodes+1, err)
	}
}
