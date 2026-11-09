package plugins

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// Setting is one setting a plugin declares, which Settings › Plugins
// draws and only the user sets.
type Setting struct {
	ID          string          `json:"id"`
	Kind        string          `json:"kind"`
	Label       string          `json:"label"`
	Description string          `json:"description,omitempty"`
	Section     string          `json:"section,omitempty"`
	Default     json.RawMessage `json:"default,omitempty"`
	// Choice
	Options []SettingOption `json:"options,omitempty"`
	Style   string          `json:"style,omitempty"`
	// Text
	Placeholder string `json:"placeholder,omitempty"`
	// Number
	Min  *float64 `json:"min,omitempty"`
	Max  *float64 `json:"max,omitempty"`
	Step float64  `json:"step,omitempty"`
	Unit string   `json:"unit,omitempty"`
}

// SettingOption is one choice of a choice setting.
type SettingOption struct {
	Value       string `json:"value"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// SettingKinds are the kinds of setting Glitched draws.
var SettingKinds = []string{"switch", "choice", "text", "number", "shortcut", "folder"}

// MaxSettingText is the longest a text setting may be.
const MaxSettingText = 2000

func (s Setting) check() error {
	if !slices.Contains(SettingKinds, s.Kind) {
		return fmt.Errorf("kind %q is none of %s", s.Kind, strings.Join(SettingKinds, ", "))
	}
	if s.Label == "" {
		return errors.New("no label")
	}
	if s.Kind == "choice" && len(s.Options) == 0 {
		return errors.New("a choice needs options")
	}
	if s.Kind == "choice" && s.Style != "" && s.Style != "select" && s.Style != "segmented" {
		return fmt.Errorf("style %q is select or segmented", s.Style)
	}
	if s.Min != nil && s.Max != nil && *s.Min > *s.Max {
		return errors.New("min over max")
	}
	if len(s.Default) > 0 {
		if _, err := s.Parse(s.Default); err != nil {
			return fmt.Errorf("default: %w", err)
		}
	}
	return nil
}

// Parse checks a value against the setting and gives it as its kind's Go
// value: bool, string or float64.
func (s Setting) Parse(raw json.RawMessage) (any, error) {
	switch s.Kind {
	case "switch":
		var b bool
		if json.Unmarshal(raw, &b) != nil {
			return nil, errors.New("a switch is true or false")
		}
		return b, nil
	case "number":
		var f float64
		if json.Unmarshal(raw, &f) != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, errors.New("not a number")
		}
		if s.Min != nil {
			f = math.Max(f, *s.Min)
		}
		if s.Max != nil {
			f = math.Min(f, *s.Max)
		}
		return f, nil
	}
	var v string
	if json.Unmarshal(raw, &v) != nil {
		return nil, errors.New("not text")
	}
	switch s.Kind {
	case "choice":
		if !slices.ContainsFunc(s.Options, func(o SettingOption) bool { return o.Value == v }) {
			return nil, fmt.Errorf("%q is none of the options", v)
		}
	case "text":
		if len([]rune(v)) > MaxSettingText {
			return nil, fmt.Errorf("over %d characters", MaxSettingText)
		}
	case "shortcut":
		if v != "" {
			if _, err := parseKey(v); err != nil {
				return nil, err
			}
		}
	case "folder":
		if v != "" && !filepath.IsAbs(v) {
			return nil, errors.New("a folder is an absolute path")
		}
	}
	return v, nil
}

// Zero is the setting's value before the user and the plugin's default
// set one.
func (s Setting) Zero() any {
	switch s.Kind {
	case "switch":
		return false
	case "number":
		if s.Min != nil {
			return *s.Min
		}
		return 0.0
	case "choice":
		return s.Options[0].Value
	}
	return ""
}

// Value is what the user set, or the default, or the kind's zero: a value
// that no longer fits (an option the plugin dropped) reads the default
// again.
func (s Setting) Value(set json.RawMessage) any {
	if len(set) > 0 {
		if v, err := s.Parse(set); err == nil {
			return v
		}
	}
	if len(s.Default) > 0 {
		if v, err := s.Parse(s.Default); err == nil {
			return v
		}
	}
	return s.Zero()
}

// SettingStatus is what a plugin says of how a setting stands, drawn
// beside it: a badge in a fixed tone, and words in its description's
// place.
type SettingStatus struct {
	Badge string `json:"badge,omitempty"`
	Tone  string `json:"tone,omitempty"`
	Text  string `json:"text,omitempty"`
}

var tones = []string{"", "neutral", "success", "warning", "danger"}

// Key is a parsed shortcut: its modifiers and its key.
type Key struct {
	Cmd, Ctrl, Alt, Shift bool
	Name                  string
}

// parseKey reads "cmd+shift+t". A shortcut needs Cmd, Ctrl or Alt: a key
// alone, or Shift on a character, would take that key from every field.
func parseKey(s string) (Key, error) {
	var k Key
	parts := strings.Split(strings.ToLower(strings.TrimSpace(s)), "+")
	for i, p := range parts {
		last := i == len(parts)-1
		switch {
		case p == "cmd" && !last:
			k.Cmd = true
		case p == "ctrl" && !last:
			k.Ctrl = true
		case (p == "alt" || p == "option") && !last:
			k.Alt = true
		case p == "shift" && !last:
			k.Shift = true
		case last && (len(p) == 1 && strings.ContainsAny(p, "abcdefghijklmnopqrstuvwxyz0123456789") || isNamedKey(p)):
			k.Name = p
		default:
			return Key{}, fmt.Errorf("%q is modifiers (cmd, ctrl, alt, shift) and one key, as cmd+shift+t", s)
		}
	}
	if !k.Cmd && !k.Ctrl && !k.Alt {
		return Key{}, fmt.Errorf("%q needs cmd, ctrl or alt", s)
	}
	return k, nil
}

func isNamedKey(p string) bool {
	if n, err := strconv.Atoi(strings.TrimPrefix(p, "f")); err == nil && strings.HasPrefix(p, "f") {
		return n >= 1 && n <= 12
	}
	return slices.Contains([]string{"enter", "space", "tab", "up", "down", "left", "right", "comma", "period", "slash"}, p)
}

// String is the shortcut as plugin.json writes it, modifiers in one order.
func (k Key) String() string {
	var parts []string
	for _, m := range []struct {
		on   bool
		name string
	}{{k.Cmd, "cmd"}, {k.Ctrl, "ctrl"}, {k.Alt, "alt"}, {k.Shift, "shift"}} {
		if m.on {
			parts = append(parts, m.name)
		}
	}
	return strings.Join(append(parts, k.Name), "+")
}

// ValidShortcut reports whether s is a shortcut a plugin's setting may
// hold.
func ValidShortcut(s string) bool {
	_, err := parseKey(s)
	return err == nil
}
