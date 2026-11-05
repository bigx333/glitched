package mygoscript

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

// node is one widget of the tree a script's view returns, as the prelude
// encodes it: its kind, its key, its children, and every other field as a
// prop, decoded only when the widget that takes it asks for it.
type node struct {
	Kind     string                     `json:"kind"`
	Key      string                     `json:"key"`
	Props    map[string]json.RawMessage `json:"props"`
	Children []node                     `json:"children"`
}

// fnRef names a Luau function the prelude registered for the render that
// encoded it.
type fnRef struct {
	Fn string `json:"fn"`
}

// props hands out a node's props once each, so whatever a widget and the
// element props leave over is an unknown prop.
type props struct {
	kind string
	m    map[string]json.RawMessage
	err  error
}

func newProps(n node) *props {
	m := make(map[string]json.RawMessage, len(n.Props))
	for k, v := range n.Props {
		m[k] = v
	}
	return &props{kind: n.Kind, m: m}
}

func (p *props) fail(name string, err error) {
	if p.err == nil {
		p.err = fmt.Errorf("%s.%s: %w", p.kind, name, err)
	}
}

func (p *props) take(name string) (json.RawMessage, bool) {
	raw, ok := p.m[name]
	delete(p.m, name)
	return raw, ok && string(raw) != "null"
}

func (p *props) has(name string) bool {
	_, ok := p.m[name]
	return ok
}

func decodeProp[T any](p *props, name string, def T) T {
	raw, ok := p.take(name)
	if !ok {
		return def
	}
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		p.fail(name, fmt.Errorf("want %s: %s", typeName[T](), raw))
		return def
	}
	return v
}

func typeName[T any]() string {
	var v T
	switch any(v).(type) {
	case string:
		return "a string"
	case float64, float32, int:
		return "a number"
	case bool:
		return "a boolean"
	case []string:
		return "a list of strings"
	case fnRef:
		return "a function"
	}
	return fmt.Sprintf("%T", v)
}

func (p *props) str(name, def string) string          { return decodeProp(p, name, def) }
func (p *props) num(name string, def float64) float64 { return decodeProp(p, name, def) }
func (p *props) f32(name string, def float32) float32 { return decodeProp(p, name, def) }
func (p *props) bool(name string, def bool) bool      { return decodeProp(p, name, def) }
func (p *props) strs(name string) []string            { return decodeProp[[]string](p, name, nil) }

func (p *props) int(name string, def int) int {
	return int(math.Round(p.num(name, float64(def))))
}

func (p *props) fn(name string) *fnRef {
	raw, ok := p.take(name)
	if !ok {
		return nil
	}
	var f fnRef
	if err := json.Unmarshal(raw, &f); err != nil || f.Fn == "" {
		p.fail(name, fmt.Errorf("want a function or an action name"))
		return nil
	}
	return &f
}

func (p *props) color(name string) (ui.Color, bool) {
	raw, ok := p.take(name)
	if !ok {
		return ui.Color{}, false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		p.fail(name, fmt.Errorf("want a color string: %s", raw))
		return ui.Color{}, false
	}
	c, err := parseColor(s)
	if err != nil {
		p.fail(name, err)
		return ui.Color{}, false
	}
	return c, true
}

// node decodes a prop that holds one widget, as a popover's anchor.
func (p *props) node(name string) (node, bool) {
	raw, ok := p.take(name)
	if !ok {
		return node{}, false
	}
	var n node
	if err := json.Unmarshal(raw, &n); err != nil || n.Kind == "" {
		p.fail(name, fmt.Errorf("want a widget"))
		return node{}, false
	}
	return n, true
}

// floats decodes a number or a list of numbers, as padding's.
func (p *props) floats(name string) []float32 {
	raw, ok := p.take(name)
	if !ok {
		return nil
	}
	var one float32
	if json.Unmarshal(raw, &one) == nil {
		return []float32{one}
	}
	var many []float32
	if err := json.Unmarshal(raw, &many); err != nil || len(many) == 0 || len(many) > 4 {
		p.fail(name, fmt.Errorf("want a number or a list of 1 to 4 numbers: %s", raw))
		return nil
	}
	return many
}

// leftover is an error naming the props nothing took.
func (p *props) leftover() error {
	if p.err != nil {
		return p.err
	}
	if len(p.m) == 0 {
		return nil
	}
	names := make([]string, 0, len(p.m))
	for k := range p.m {
		names = append(names, k)
	}
	sort.Strings(names)
	return fmt.Errorf("%s: unknown prop %s", p.kind, strings.Join(names, ", "))
}

// parseColor reads "#rgb", "#rrggbb" or "#rrggbbaa".
func parseColor(s string) (ui.Color, error) {
	h := strings.TrimPrefix(s, "#")
	if len(h) == 3 {
		h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
	}
	if len(h) == 6 {
		h += "ff"
	}
	v, err := strconv.ParseUint(h, 16, 32)
	if len(h) != 8 || err != nil || !strings.HasPrefix(s, "#") {
		return ui.Color{}, fmt.Errorf("%q is not a #rrggbb or #rrggbbaa color", s)
	}
	return ui.Color{R: uint8(v >> 24), G: uint8(v >> 16), B: uint8(v >> 8), A: uint8(v)}, nil
}

func formatColor(c ui.Color) string {
	return fmt.Sprintf("#%02x%02x%02x%02x", c.R, c.G, c.B, c.A)
}

func parseTime(s string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04", "2006-01-02", "15:04"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("%q is not a date (2006-01-02), a time (15:04) or RFC 3339", s)
}

var aligns = map[string]ui.Align{
	"start": ui.Start, "center": ui.Center, "end": ui.End, "stretch": ui.Stretch,
	"spaceBetween": ui.SpaceBetween, "spaceAround": ui.SpaceAround, "spaceEvenly": ui.SpaceEvenly,
}

func (p *props) align(name string) (ui.Align, bool) {
	if !p.has(name) {
		return 0, false
	}
	s := p.str(name, "")
	a, ok := aligns[s]
	if !ok {
		p.fail(name, fmt.Errorf("%q is not one of start, center, end, stretch, spaceBetween, spaceAround, spaceEvenly", s))
	}
	return a, ok
}
