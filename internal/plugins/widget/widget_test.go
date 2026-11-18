package widget

import (
	"fmt"
	"strings"
	"testing"
)

func TestDecodeChecksTheTree(t *testing.T) {
	ok := []string{
		`{"body": {"type": "text", "text": "hi"}}`,
		`{"title": "T", "body": {"type": "column", "gap": 8, "padding": 64, "align": "center", "justify": "between", "children": [
			{"type": "card", "title": "C", "children": [{"type": "markdown", "text": "**b**"}]},
			{"type": "row", "children": [{"type": "badge", "text": "b", "style": "success"}, {"type": "spacer"}, {"type": "divider"}, {"type": "spinner"}]},
			{"type": "button", "id": "b", "label": "Go", "style": "primary", "onClick": {"handler": "go", "payload": {"n": 1}}},
			{"type": "input", "id": "i", "value": "x", "multiline": true, "onChange": "typed", "onSubmit": "sent"},
			{"type": "toggle", "id": "t", "label": "On", "value": true, "controlled": true, "onChange": "flip"},
			{"type": "select", "id": "s", "options": ["a", {"value": "b", "label": "Bee"}], "value": "b", "onChange": "pick"}
		]}}`,
	}
	for _, s := range ok {
		if _, err := Decode([]byte(s)); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
	bad := map[string]string{
		`{}`: "no body",
		`{"body": {"type": "button", "id": "a", "label": "A", "style": "ghost"}}`:                                 "a ghost button shows an icon",
		`{"body": {"type": "button", "id": "a", "label": "A", "icon": "code"}}`:                                   "only a ghost or outline button has an icon",
		`{"body": {"type": "button", "id": "a", "label": "A", "style": "outline", "badge": "3"}}`:                 "only a ghost button is active or has a badge",
		`{"body": {"type": "text", "text": "x", "tooltip": "t"}}`:                                                 "a text has no tooltip",
		`{"body": {"type": "menu", "id": "m", "label": "M", "options": ["a"], "style": "ghost", "icon": "code"}}`: "unknown style",
		`{"body": {"type": "text", "text": "x"}, "html": "<b>"}`:                                                  `unknown field "html"`,
		`{"body": {"type": "frame"}}`:                                                                                      `unknown widget type "frame"`,
		`{"body": {"type": "text", "children": [{"type": "text"}]}}`:                                                       "a text has no children",
		`{"body": {"type": "button", "label": "x"}}`:                                                                       "a button needs an id",
		`{"body": {"type": "button", "id": "a b", "label": "x"}}`:                                                          "a button needs an id",
		`{"body": {"type": "text", "style": "blink"}}`:                                                                     `unknown style "blink"`,
		`{"body": {"type": "column", "style": "x"}}`:                                                                       "a column has no style",
		`{"body": {"type": "column", "gap": 65}}`:                                                                          "gap and padding are 0 to 64",
		`{"body": {"type": "column", "align": "left"}}`:                                                                    "unknown align",
		`{"body": {"type": "button", "id": "a", "onChange": "x"}}`:                                                         "a button has no onChange",
		`{"body": {"type": "text", "onClick": "x"}}`:                                                                       "a text has no onClick",
		`{"body": {"type": "button", "id": "a", "onClick": "no spaces"}}`:                                                  "a handler is",
		`{"body": {"type": "input", "id": "a", "value": 3}}`:                                                               "an input's value is a string",
		`{"body": {"type": "toggle", "id": "a", "value": "yes"}}`:                                                          "a toggle's value is true or false",
		`{"body": {"type": "select", "id": "a"}}`:                                                                          "a select has 1 to 200 options",
		`{"body": {"type": "select", "id": "a", "options": ["x"], "value": "y"}}`:                                          "none of the options",
		`{"body": {"type": "text", "value": "y"}}`:                                                                         "a text has no value",
		`{"body": {"type": "toggle", "id": "a", "multiline": true}}`:                                                       "not multiline",
		`{"body": {"type": "text", "options": ["x"]}}`:                                                                     "a text has no options",
		`{"body": {"type": "button", "id": "a", "controlled": true}}`:                                                      "a button is not controlled",
		`{"body": {"type": "tabs", "id": "t"}}`:                                                                            "a tabs has 1 to 200 options",
		`{"body": {"type": "menu", "id": "m", "options": ["a"], "value": "a"}}`:                                            "a menu has no value",
		`{"body": {"type": "dialog", "id": "d"}}`:                                                                          "a dialog needs a title",
		`{"body": {"type": "dialog", "title": "T"}}`:                                                                       "a dialog needs an id",
		`{"body": {"type": "text", "open": true}}`:                                                                         "a text doesn't open",
		`{"body": {"type": "table", "columns": []}}`:                                                                       "a table has 1 to 20 columns",
		`{"body": {"type": "table", "columns": [{"id": "a", "title": "A"}, {"id": "a", "title": "B"}]}}`:                   "an id of their own",
		`{"body": {"type": "table", "columns": [{"id": "a", "title": "A"}], "rows": [{"key": "1", "cells": {"b": "x"}}]}}`: `cell "b" is no column`,
		`{"body": {"type": "image", "src": "file:///etc/passwd"}}`:                                                         "plugin:<path> or an https address",
		`{"body": {"type": "image", "src": "plugin:../../secret.png"}}`:                                                    "may not leave the plugin's folder",
		`{"body": {"type": "image", "src": "plugin:a.png", "width": 5000}}`:                                                "0 to 2000",
		`{"body": {"type": "icon"}}`:                                                                                       "an icon needs a name",
		`{"body": {"type": "text", "color": "#ff0000"}}`:                                                                   `color "#ff0000" is none of`,
		`{"body": {"type": "text", "width": 3}}`:                                                                           "a text has no width",
		`{"body": {"type": "column", "children": [null]}}`:                                                                 "null widget",
		`{"body": {"type": "column", "children": [{"type": "button", "id": "a"}, {"type": "toggle", "id": "a"}]}}`:         `the id "a" is used twice`,
	}
	for s, want := range bad {
		_, err := Decode([]byte(s))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", s, err, want)
		}
	}
}

func TestDecodeBoundsTheTree(t *testing.T) {
	deep := `{"type": "text"}`
	for range MaxDepth {
		deep = `{"type": "column", "children": [` + deep + `]}`
	}
	if _, err := Decode([]byte(`{"body": ` + deep + `}`)); err == nil || !strings.Contains(err.Error(), "deep") {
		t.Errorf("%d deep: %v", MaxDepth+1, err)
	}
	many := make([]string, MaxNodes)
	for i := range many {
		many[i] = `{"type": "spacer"}`
	}
	if _, err := Decode([]byte(`{"body": {"type": "column", "children": [` + strings.Join(many, ",") + `]}}`)); err == nil || !strings.Contains(err.Error(), "over 2000 widgets") {
		t.Errorf("%d widgets: %v", MaxNodes+1, err)
	}
	long := strings.Repeat("x", MaxText+1)
	if _, err := Decode(fmt.Appendf(nil, `{"body": {"type": "text", "text": %q}}`, long)); err == nil || !strings.Contains(err.Error(), "string over") {
		t.Errorf("a long text: %v", err)
	}
	payload := strings.Repeat("x", MaxPayload)
	if _, err := Decode(fmt.Appendf(nil, `{"body": {"type": "button", "id": "b", "onClick": {"handler": "h", "payload": %q}}}`, payload)); err == nil || !strings.Contains(err.Error(), "payload over") {
		t.Errorf("a big payload: %v", err)
	}
	if _, err := Decode(make([]byte, MaxBytes+1)); err == nil || !strings.Contains(err.Error(), "bytes, over") {
		t.Errorf("a big view: %v", err)
	}
}

// A native widget is one of Natives, passed only the props it takes; a
// locked part of the UI is never one, whatever a plugin names.
func TestANativeWidgetIsOneGlitchedOffers(t *testing.T) {
	good := []string{
		`{"body": {"type": "native", "name": "diff.body", "props": {"path": "a.go"}, "grow": true, "onChange": "marked"}}`,
		`{"body": {"type": "native", "name": "diff.body"}}`,
	}
	for _, s := range good {
		if _, err := Decode([]byte(s)); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
	bad := map[string]string{
		`{"body": {"type": "native", "name": "terminal"}}`:                                       `no native widget "terminal"`,
		`{"body": {"type": "native", "name": "approval.card"}}`:                                  "approval.card is off limits to plugins: an approval answers for you",
		`{"body": {"type": "native", "name": "composer.send"}}`:                                  "composer.send is off limits to plugins",
		`{"body": {"type": "native", "name": "diff.body", "props": {"thread": "t1"}}}`:           `diff.body takes no prop "thread"`,
		`{"body": {"type": "native", "name": "diff.body", "props": {"path": 3}}}`:                "diff.body's prop path is a string",
		`{"body": {"type": "native", "name": "diff.body", "onClick": "x"}}`:                      "a native has no onClick",
		`{"body": {"type": "text", "text": "x", "props": {"path": "a"}}}`:                        "a text has no props",
		`{"body": {"type": "item", "id": "i", "text": "x"}}`:                                     "an item shows an icon",
		`{"body": {"type": "text", "text": "x", "trailing": [{"type": "text"}]}}`:                "a text has no trailing or actions",
		`{"body": {"type": "item", "id": "i", "icon": "file", "actions": [{"type": "button"}]}}`: "a button needs an id",
		`{"body": {"type": "tabs", "id": "t", "options": [{"value": "a", "disabled": true}]}}`:   "only segmented tabs' options are disabled",
		`{"body": {"type": "button", "id": "b", "label": "B", "size": "small"}}`:                 "size is small, of a text or a ghost button",
	}
	for s, want := range bad {
		_, err := Decode([]byte(s))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", s, err, want)
		}
	}
	for name := range Natives {
		if _, ok := Locked[name]; ok {
			t.Errorf("%s is both a native widget and locked", name)
		}
	}
}
