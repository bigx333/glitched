// Package widget is the native UI contract of Glitched's plugins: the tree
// of widgets a plugin's view export answers, as data. The host decodes and
// checks it here, then draws it with its own components; a plugin never
// touches the UI.
//
// Interactive widgets name handlers, not functions: a handler is a name
// and an optional JSON payload, which come back to the plugin's update
// export when the user acts on the widget.
package widget

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
)

// Version is the version of the tree's shape. A change that breaks a
// plugin bumps it.
const Version = 1

// The bounds of a view. A view beyond them is refused, not cut.
const (
	MaxBytes   = 1 << 20 // the view's JSON
	MaxNodes   = 2000
	MaxDepth   = 24
	MaxText    = 32 << 10 // one string
	MaxOptions = 200      // one select's options
	MaxPayload = 4 << 10  // one handler's payload, as JSON
	MaxSpacing = 64       // gap and padding, in points
)

// Types are the widgets this version draws.
var Types = map[string]kind{
	"column":   container,
	"row":      container,
	"card":     container,
	"text":     leaf,
	"markdown": leaf,
	"badge":    leaf,
	"divider":  leaf,
	"spacer":   leaf,
	"spinner":  leaf,
	"button":   control,
	"input":    control,
	"toggle":   control,
	"select":   control,
}

type kind uint8

const (
	leaf kind = iota
	container
	control // needs an id, which keys its state and its events
)

// View is what a view export answers: a page's heading and its body.
type View struct {
	Title    string `json:"title,omitempty"`
	Subtitle string `json:"subtitle,omitempty"`
	Body     *Node  `json:"body"`
}

// Node is one widget. Which fields it reads depends on its type; the
// others must be left out.
type Node struct {
	Type     string  `json:"type"`
	ID       string  `json:"id,omitempty"`
	Children []*Node `json:"children,omitempty"`

	Text        string `json:"text,omitempty"`
	Title       string `json:"title,omitempty"`
	Label       string `json:"label,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	// Style is a text's look: body, muted, heading, title, code, error or
	// success; a button's: default, primary or danger; a badge's: default,
	// accent, success, warning or danger.
	Style     string          `json:"style,omitempty"`
	Value     json.RawMessage `json:"value,omitempty"`
	Options   []Option        `json:"options,omitempty"`
	Multiline bool            `json:"multiline,omitempty"`
	// Controlled says the plugin holds the control's value: once the
	// plugin took the user's changes, the view's value is the value.
	Controlled bool `json:"controlled,omitempty"`
	Disabled   bool `json:"disabled,omitempty"`
	Selectable bool `json:"selectable,omitempty"`

	Gap     int    `json:"gap,omitempty"`
	Padding int    `json:"padding,omitempty"`
	Align   string `json:"align,omitempty"`
	Justify string `json:"justify,omitempty"`
	Grow    bool   `json:"grow,omitempty"`

	OnClick  *Handler `json:"onClick,omitempty"`
	OnChange *Handler `json:"onChange,omitempty"`
	OnSubmit *Handler `json:"onSubmit,omitempty"`
}

// Option is one choice of a select: what the plugin gets, and what the
// user sees, which is the value when left out.
type Option struct {
	Value string `json:"value"`
	Label string `json:"label,omitempty"`
}

// UnmarshalJSON takes an option as an object, or as a string that is both.
func (o *Option) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		return json.Unmarshal(b, &o.Value)
	}
	type plain Option
	return json.Unmarshal(b, (*plain)(o))
}

// Shown is what the user sees of the option.
func (o Option) Shown() string {
	if o.Label != "" {
		return o.Label
	}
	return o.Value
}

// Handler names what the plugin's update does with an event: Name, and a
// payload of the plugin's own that comes back with it.
type Handler struct {
	Name    string          `json:"handler"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// UnmarshalJSON takes a handler as an object, or as a string that is its
// name.
func (h *Handler) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		return json.Unmarshal(b, &h.Name)
	}
	type plain Handler
	return json.Unmarshal(b, (*plain)(h))
}

var (
	idPattern      = regexp.MustCompile(`^[A-Za-z0-9_.:/-]{1,128}$`)
	handlerPattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)
	styles         = map[string][]string{
		"text":   {"", "body", "muted", "heading", "title", "code", "error", "success"},
		"button": {"", "default", "primary", "danger"},
		"badge":  {"", "default", "accent", "success", "warning", "danger"},
	}
	aligns   = []string{"", "start", "center", "end", "stretch"}
	justifys = []string{"", "start", "center", "end", "between"}
)

// Decode reads a view from a view export's JSON and checks it.
func Decode(b []byte) (*View, error) { return DecodeWithin(b, MaxNodes) }

// DecodeWithin is Decode with a bound of its own on the widgets: a place's
// copy is a badge, not a page.
func DecodeWithin(b []byte, maxNodes int) (*View, error) {
	if len(b) > MaxBytes {
		return nil, fmt.Errorf("view is %d bytes, over the %d allowed", len(b), MaxBytes)
	}
	var v View
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(&v); err != nil {
		return nil, fmt.Errorf("view: %w", err)
	}
	if err := v.check(maxNodes); err != nil {
		return nil, err
	}
	return &v, nil
}

// Check reports the first thing wrong with the view, by its path in the
// tree, or nil.
func (v *View) Check() error { return v.check(MaxNodes) }

func (v *View) check(maxNodes int) error {
	if v.Body == nil {
		return fmt.Errorf("view: no body")
	}
	if len(v.Title) > MaxText || len(v.Subtitle) > MaxText {
		return fmt.Errorf("view: title or subtitle over %d bytes", MaxText)
	}
	c := checker{ids: map[string]bool{}, max: maxNodes}
	return c.node(v.Body, "body", 1)
}

type checker struct {
	max   int
	nodes int
	ids   map[string]bool
}

func (c *checker) node(n *Node, path string, depth int) error {
	if n == nil {
		return fmt.Errorf("%s: null widget", path)
	}
	c.nodes++
	if c.nodes > c.max {
		return fmt.Errorf("%s: over %d widgets", path, c.max)
	}
	if depth > MaxDepth {
		return fmt.Errorf("%s: nested over %d deep", path, MaxDepth)
	}
	k, ok := Types[n.Type]
	if !ok {
		return fmt.Errorf("%s: unknown widget type %q", path, n.Type)
	}
	if n.ID != "" {
		path += fmt.Sprintf(" (%s %q)", n.Type, n.ID)
	}
	fail := func(format string, args ...any) error {
		return fmt.Errorf("%s: %s", path, fmt.Sprintf(format, args...))
	}
	if n.ID != "" || k == control {
		if !idPattern.MatchString(n.ID) {
			return fail("a %s needs an id of 1 to 128 letters, digits or _.:/-, not %q", n.Type, n.ID)
		}
		if c.ids[n.ID] {
			return fail("the id %q is used twice", n.ID)
		}
		c.ids[n.ID] = true
	}
	if k != container && len(n.Children) > 0 {
		return fail("a %s has no children", n.Type)
	}
	for _, s := range []string{n.Text, n.Title, n.Label, n.Placeholder} {
		if len(s) > MaxText {
			return fail("a string over %d bytes", MaxText)
		}
	}
	if allowed, ok := styles[n.Type]; ok {
		if !contains(allowed, n.Style) {
			return fail("unknown style %q", n.Style)
		}
	} else if n.Style != "" {
		return fail("a %s has no style", n.Type)
	}
	if !contains(aligns, n.Align) || !contains(justifys, n.Justify) {
		return fail("unknown align %q or justify %q", n.Align, n.Justify)
	}
	if n.Gap < 0 || n.Gap > MaxSpacing || n.Padding < 0 || n.Padding > MaxSpacing {
		return fail("gap and padding are 0 to %d", MaxSpacing)
	}
	if err := c.handlers(n); err != nil {
		return fail("%v", err)
	}
	if err := c.value(n); err != nil {
		return fail("%v", err)
	}
	for i, ch := range n.Children {
		if err := c.node(ch, path+".children["+strconv.Itoa(i)+"]", depth+1); err != nil {
			return err
		}
	}
	return nil
}

// handlers checks that the widget names only the handlers its type fires.
func (c *checker) handlers(n *Node) error {
	fires := map[string][]string{
		"button": {"onClick"},
		"input":  {"onChange", "onSubmit"},
		"toggle": {"onChange"},
		"select": {"onChange"},
	}[n.Type]
	for name, h := range map[string]*Handler{"onClick": n.OnClick, "onChange": n.OnChange, "onSubmit": n.OnSubmit} {
		if h == nil {
			continue
		}
		if !contains(fires, name) {
			return fmt.Errorf("a %s has no %s", n.Type, name)
		}
		if !handlerPattern.MatchString(h.Name) {
			return fmt.Errorf("%s: a handler is 1 to 64 letters, digits or _.:-, not %q", name, h.Name)
		}
		if len(h.Payload) > MaxPayload {
			return fmt.Errorf("%s: payload over %d bytes", name, MaxPayload)
		}
	}
	return nil
}

// value checks the widget's value and options against its type.
func (c *checker) value(n *Node) error {
	if n.Type != "select" && len(n.Options) > 0 {
		return fmt.Errorf("a %s has no options", n.Type)
	}
	if n.Multiline && n.Type != "input" {
		return fmt.Errorf("a %s is not multiline", n.Type)
	}
	if n.Controlled && n.Type != "input" && n.Type != "toggle" && n.Type != "select" {
		return fmt.Errorf("a %s is not controlled", n.Type)
	}
	if n.Type == "select" && (len(n.Options) == 0 || len(n.Options) > MaxOptions) {
		return fmt.Errorf("a select has 1 to %d options", MaxOptions)
	}
	if len(n.Value) == 0 || string(n.Value) == "null" {
		return nil
	}
	switch n.Type {
	case "input":
		var s string
		if json.Unmarshal(n.Value, &s) != nil {
			return fmt.Errorf("an input's value is a string")
		}
		if len(s) > MaxText {
			return fmt.Errorf("value over %d bytes", MaxText)
		}
	case "toggle":
		var b bool
		if json.Unmarshal(n.Value, &b) != nil {
			return fmt.Errorf("a toggle's value is true or false")
		}
	case "select":
		var s string
		if json.Unmarshal(n.Value, &s) != nil {
			return fmt.Errorf("a select's value is a string")
		}
		found := false
		for _, o := range n.Options {
			found = found || o.Value == s
		}
		if !found {
			return fmt.Errorf("value %q is none of the options", s)
		}
	default:
		return fmt.Errorf("a %s has no value", n.Type)
	}
	return nil
}

// Walk calls fn on each widget, parents before their children.
func (n *Node) Walk(fn func(*Node)) {
	if n == nil {
		return
	}
	fn(n)
	for _, ch := range n.Children {
		ch.Walk(fn)
	}
}

// Find is the widget with the id, or nil.
func (v *View) Find(id string) *Node {
	var found *Node
	v.Body.Walk(func(n *Node) {
		if n.ID == id {
			found = n
		}
	})
	return found
}

func contains(list []string, s string) bool { return slices.Contains(list, s) }
