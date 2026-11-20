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
		`{"body": {"type": "native", "name": "settings.servers"}}`:                               "settings.servers is off limits to plugins",
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

// The chrome's widgets: sizes and weights, paddings, keycaps, links and
// menu separators, each only where it belongs.
func TestTheChromeWidgetsAreChecked(t *testing.T) {
	good := `{"body": {"type": "column", "padding": [24, 0], "children": [
		{"type": "text", "text": "G", "fontSize": 20, "weight": 650, "padding": [1, 2, 3, 4], "grow": true},
		{"type": "icon", "name": "sparkles", "fontSize": 30},
		{"type": "keys", "keys": ["⌘", "K"]},
		{"type": "link", "text": "Open", "href": "https://example.com"},
		{"type": "image", "src": "app:icon"},
		{"type": "card", "style": "list", "children": [{"type": "text", "text": "row"}]},
		{"type": "input", "id": "q", "style": "search"},
		{"type": "button", "id": "b", "label": "Check", "style": "bordered", "icon": "refresh"},
		{"type": "button", "id": "g", "label": "Go", "progress": true},
		{"type": "menu", "id": "m", "label": "M", "options": ["a", {"separator": true}, "b"]}
	]}}`
	if _, err := Decode([]byte(good)); err != nil {
		t.Fatal(err)
	}
	bad := map[string]string{
		`{"body": {"type": "column", "padding": [1, 2, 3]}}`:                                                     "padding is 1, 2 or 4",
		`{"body": {"type": "column", "padding": [80]}}`:                                                          "gap and padding are 0 to 64",
		`{"body": {"type": "badge", "text": "x", "fontSize": 12}}`:                                               "a badge has no fontSize or weight",
		`{"body": {"type": "text", "text": "x", "fontSize": 200}}`:                                               "fontSize is 8 to 48",
		`{"body": {"type": "keys", "keys": []}}`:                                                                 "keys are a keys widget's",
		`{"body": {"type": "link", "text": "x", "href": "javascript:alert(1)"}}`:                                 "an http or https href",
		`{"body": {"type": "image", "src": "app:secrets"}}`:                                                      "or app:icon",
		`{"body": {"type": "select", "id": "s", "options": [{"separator": true}]}}`:                              "only a menu has separators",
		`{"body": {"type": "button", "id": "b", "label": "B", "style": "ghost", "icon": "x", "progress": true}}`: "progress is a plain button's",
	}
	for s, want := range bad {
		_, err := Decode([]byte(s))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", s, err, want)
		}
	}
}

// Settings' controls: switches, segmented controls, selects of a width
// and color swatches, each only where it belongs.
func TestTheSettingsWidgetsAreChecked(t *testing.T) {
	good := `{"body": {"type": "column", "children": [
		{"type": "toggle", "id": "s", "style": "switch", "label": "Stream"},
		{"type": "tabs", "id": "m", "style": "control", "label": "Mode", "options": ["Local", "New worktree"]},
		{"type": "select", "id": "t", "options": ["a", "b"], "width": 160},
		{"type": "swatch", "id": "v", "color": "#8b5cf6", "label": "Violet", "active": true, "onClick": {"handler": "accent", "payload": "Violet"}}
	]}}`
	if _, err := Decode([]byte(good)); err != nil {
		t.Fatal(err)
	}
	bad := map[string]string{
		`{"body": {"type": "swatch", "id": "v", "color": "$accent", "label": "Violet"}}`:             "a swatch has a color as #rrggbb, and a label",
		`{"body": {"type": "swatch", "id": "v", "color": "#8b5cf6"}}`:                                "a swatch has a color as #rrggbb, and a label",
		`{"body": {"type": "text", "text": "x", "color": "#8b5cf6"}}`:                                "is none of",
		`{"body": {"type": "select", "id": "t", "options": ["a"], "width": 900}}`:                    "a select's width is 40 to 480",
		`{"body": {"type": "input", "id": "t", "width": 100}}`:                                       "has no width",
		`{"body": {"type": "toggle", "id": "s", "style": "control"}}`:                                "unknown style",
		`{"body": {"type": "swatch", "id": "v", "color": "#8b5cf6", "label": "V", "onChange": "x"}}`: "has no onChange",
	}
	for s, want := range bad {
		_, err := Decode([]byte(s))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", s, err, want)
		}
	}
}

// The sidebar's widgets: a ghost button's popover, color and own content,
// a clicked row, a project's tile, wrapping, dimming and Glitched's
// menus, each only where it belongs.
func TestTheSidebarWidgetsAreChecked(t *testing.T) {
	good := `{"body": {"type": "row", "wrap": true, "children": [
		{"type": "button", "id": "z", "style": "ghost", "icon": "clock", "color": "#fbbf24", "open": true, "onClose": "close",
			"popover": {"type": "column", "width": 220, "children": [
				{"type": "row", "id": "r", "onClick": "go", "contextMenu": "thread:x", "children": [{"type": "text", "text": "Later", "italic": true}]}]}},
		{"type": "button", "id": "p", "style": "ghost", "opacity": 0.45, "contextMenu": "project::/work/a", "padding": [3, 7, 3, 3], "gap": 5,
			"children": [{"type": "tile", "icon": "code", "color": "#8b5cf6", "width": 16}, {"type": "text", "text": "a"}]}
	]}}`
	if _, err := Decode([]byte(good)); err != nil {
		t.Fatal(err)
	}
	bad := map[string]string{
		`{"body": {"type": "button", "id": "b", "label": "x", "popover": {"type": "column", "width": 220}}}`:                  "only a ghost button has a popover",
		`{"body": {"type": "button", "id": "b", "style": "ghost", "icon": "x", "popover": {"type": "row", "width": 220}}}`:    "a popover is a column",
		`{"body": {"type": "button", "id": "b", "style": "ghost", "icon": "x", "popover": {"type": "column", "width": 90}}}`:  "a popover's width is 120 to 480",
		`{"body": {"type": "button", "id": "b", "style": "ghost", "icon": "x", "open": true}}`:                                "doesn't open",
		`{"body": {"type": "button", "id": "b", "label": "x", "onClose": "c"}}`:                                               "only a button with a popover has onClose",
		`{"body": {"type": "button", "id": "b", "style": "ghost", "icon": "x", "children": [{"type": "text", "text": "y"}]}}`: "an icon or its children, not both",
		`{"body": {"type": "button", "id": "b", "label": "x", "children": [{"type": "text", "text": "y"}]}}`:                  "has no children",
		`{"body": {"type": "button", "id": "b", "label": "x", "color": "#fbbf24"}}`:                                           "is none of",
		`{"body": {"type": "row", "onClick": "go"}}`:                                                                          "a row that is clicked needs an id",
		`{"body": {"type": "row", "contextMenu": "thread:x"}}`:                                                                "only a ghost button or a clicked row has a contextMenu",
		`{"body": {"type": "button", "id": "b", "style": "ghost", "icon": "x", "contextMenu": "file:/etc"}}`:                  "a contextMenu is thread:<id> or project:<key>",
		`{"body": {"type": "tile", "icon": "code", "color": "$accent", "width": 16}}`:                                         "a tile has a color as #rrggbb",
		`{"body": {"type": "tile", "icon": "code", "color": "#8b5cf6", "width": 100}}`:                                        "a width of 8 to 64",
		`{"body": {"type": "column", "wrap": true}}`:                                                                          "only a row wraps",
		`{"body": {"type": "text", "text": "x", "opacity": 0.5}}`:                                                             "opacity is a button's",
		`{"body": {"type": "button", "id": "b", "label": "x", "opacity": 0.01}}`:                                              "0.2 to 1",
		`{"body": {"type": "button", "id": "b", "style": "ghost", "label": "x", "children": [{"type": "input", "id": "i"}]}}`: "a ghost button's children are",
		`{"body": {"type": "tile", "icon": "code", "color": "#8b5cf6", "width": 16, "background": "#ff0000"}}`:                "is none of",
		`{"body": {"type": "button", "id": "b", "style": "ghost", "icon": "x", "color": "#ff0000", "background": "#ff0000"}}`: "is none of",
		`{"body": {"type": "badge", "text": "x", "italic": true}}`:                                                            "isn't italic",
	}
	for s, want := range bad {
		_, err := Decode([]byte(s))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", s, err, want)
		}
	}
}
