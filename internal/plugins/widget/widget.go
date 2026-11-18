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
	"strings"
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
	MaxTooltip = 200      // a button's or menu's tooltip
	MaxBadge   = 4        // a ghost button's count
	MaxColumns = 20       // one table's
	MaxRows    = 500      // one table's
	MaxSize    = 2000     // an image's width or height, in points
)

// Colors are the theme's tokens a widget's color and background take: a
// plugin follows the user's theme, never raw values.
var Colors = []string{"$text", "$text.muted", "$text.faint", "$accent", "$success", "$warning", "$danger", "$border", "$surface"}

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
	"tabs":     control,
	"menu":     control,
	"dialog":   dialog,
	"table":    leaf,
	"image":    leaf,
	"icon":     leaf,
	"item":     control,
	"native":   leaf,
	"keys":     leaf,
	"link":     leaf,
	"swatch":   control,
}

// Native is a widget Glitched draws itself, which a plugin only places:
// a surface too heavy or too live for widgets, as the diff of a file.
type Native struct {
	// Props are what a plugin may pass it, by name, each "string", "bool"
	// or "number".
	Props map[string]string
	// In are the components it draws in; anywhere else it says so in its
	// place. Thread says it draws about the copy's own thread.
	In     []string
	Thread bool
	// Fires are the handlers it fires.
	Fires []string
}

// Natives are the native widgets a plugin may place, by name. Only these
// draw: a name that isn't one, a locked one included, refuses the view.
var Natives = map[string]Native{
	"diff.body":  {Props: map[string]string{"path": "string"}, In: []string{"thread.panel.diff"}, Thread: true, Fires: []string{"onChange"}},
	"theme.card": {Props: map[string]string{"theme": "string"}, In: []string{"settings.page.appearance"}, Fires: []string{"onChange"}},
}

// Locked are the parts of Glitched's UI no plugin draws, replaces or
// places, and why. No manifest or grant changes them.
var Locked = map[string]string{
	"approval.card":       "an approval answers for you, so only Glitched draws it",
	"question.card":       "an answer to an agent's question is yours, so only Glitched draws it",
	"composer.access":     "the access picker sets what agents may do, so only Glitched draws it",
	"composer.send":       "send and stop run and stop agents, so only Glitched draws them",
	"settings.plugins":    "it is where you turn plugins off, so no plugin draws it",
	"settings.servers":    "pairing an engine trusts a machine, so only Glitched draws it",
	"confirm.destructive": "a confirm before something is deleted must be Glitched's own",
}

type kind uint8

const (
	leaf kind = iota
	container
	control // needs an id, which keys its state and its events
	dialog  // a container with an id, drawn over the window while open
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
	// Style is a text's look: body, muted, strong, heading, title, code,
	// error or success; a button's: default, primary or danger; a badge's:
	// default, accent, success, warning, danger or count; tabs' default,
	// segmented or control (Settings' segmented control); a toggle's
	// default or switch; an item's text plain or code.
	Style string `json:"style,omitempty"`
	// Size small is a text's or a ghost button's smaller size.
	Size      string          `json:"size,omitempty"`
	Value     json.RawMessage `json:"value,omitempty"`
	Options   []Option        `json:"options,omitempty"`
	Multiline bool            `json:"multiline,omitempty"`
	// Controlled says the plugin holds the control's value: once the
	// plugin took the user's changes, the view's value is the value.
	Controlled bool `json:"controlled,omitempty"`
	Disabled   bool `json:"disabled,omitempty"`
	Selectable bool `json:"selectable,omitempty"`

	Gap     int     `json:"gap,omitempty"`
	Padding Spacing `json:"padding,omitempty"`
	Align   string  `json:"align,omitempty"`
	Justify string  `json:"justify,omitempty"`
	Grow    bool    `json:"grow,omitempty"`

	OnClick  *Handler `json:"onClick,omitempty"`
	OnChange *Handler `json:"onChange,omitempty"`
	OnSubmit *Handler `json:"onSubmit,omitempty"`
	// OnClose is a dialog's, as the user dismisses it.
	OnClose *Handler `json:"onClose,omitempty"`

	// Open shows a dialog.
	Open bool `json:"open,omitempty"`
	// A table's columns, and its rows: each a key and a text per column.
	Columns []Column `json:"columns,omitempty"`
	Rows    []Row    `json:"rows,omitempty"`
	// An image's source: "plugin:<path>" in the plugin's folder, or an
	// https address, which needs the network grant. Width and height are
	// in points; Radius rounds its corners.
	Src    string `json:"src,omitempty"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
	Radius int    `json:"radius,omitempty"`
	// Name is an icon's: one of the app's, or "plugin:<path>.svg".
	Name string `json:"name,omitempty"`
	// Color and Background are theme tokens (Colors); a swatch's color
	// is the color it shows, as #rrggbb. A select's Width is in points.
	Color      string `json:"color,omitempty"`
	Background string `json:"background,omitempty"`

	// Tooltip is a button's or menu's tooltip, and what it is called to
	// assistive tools. Icon is an icon before the label of a ghost or
	// outline button or menu, and Compact shows it alone. Active shows a
	// ghost button on, as a panel's toggle; Badge is a count over its
	// corner.
	Tooltip string `json:"tooltip,omitempty"`
	Icon    string `json:"icon,omitempty"`
	Compact bool   `json:"compact,omitempty"`
	Active  bool   `json:"active,omitempty"`
	Badge   string `json:"badge,omitempty"`

	// An item is a list's row: its icon and text, with Active showing it
	// selected. Trailing follow its text, and while it is pointed at
	// Actions show in their place.
	Trailing []*Node `json:"trailing,omitempty"`
	Actions  []*Node `json:"actions,omitempty"`

	// FontSize and Weight are a text's size in points and weight, over
	// its style's; FontSize is also an icon's size.
	FontSize float64 `json:"fontSize,omitempty"`
	Weight   int     `json:"weight,omitempty"`
	// Progress shows a button runs, once clicked, till the plugin
	// answers: for a click whose update takes a while, as a git push.
	Progress bool `json:"progress,omitempty"`
	// Keys are a shortcut's keycaps, as "⌘" and "K".
	Keys []string `json:"keys,omitempty"`
	// Href is a link's http or https address.
	Href string `json:"href,omitempty"`

	// Name is also a native widget's (Natives), and Props what it is
	// passed.
	Props map[string]json.RawMessage `json:"props,omitempty"`
}

// Column is a table's column.
type Column struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Width int    `json:"width,omitempty"`
}

// Row is a table's row: its key, and its cells by column id.
type Row struct {
	Key   string            `json:"key"`
	Cells map[string]string `json:"cells"`
}

// Option is one choice of a select: what the plugin gets, and what the
// user sees, which is the value when left out.
type Option struct {
	Value string `json:"value"`
	Label string `json:"label,omitempty"`
	// Disabled and Tooltip are a segmented tabs' option's: why it can't
	// be chosen.
	Disabled bool   `json:"disabled,omitempty"`
	Tooltip  string `json:"tooltip,omitempty"`
	// Separator is a menu's line between options, with no value.
	Separator bool `json:"separator,omitempty"`
}

// Spacing is a padding: one value for every side, two for vertical and
// horizontal, or four for top, right, bottom and left. JSON has it as a
// number or a list.
type Spacing []int

// UnmarshalJSON takes a number or a list of numbers.
func (s *Spacing) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] != '[' {
		var n int
		if err := json.Unmarshal(b, &n); err != nil {
			return err
		}
		*s = Spacing{n}
		return nil
	}
	return json.Unmarshal(b, (*[]int)(s))
}

// Sides are the padding's top, right, bottom and left.
func (s Spacing) Sides() (t, r, b, l int) {
	switch len(s) {
	case 1:
		return s[0], s[0], s[0], s[0]
	case 2:
		return s[0], s[1], s[0], s[1]
	case 4:
		return s[0], s[1], s[2], s[3]
	}
	return 0, 0, 0, 0
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
	hexColor       = regexp.MustCompile("^#[0-9a-fA-F]{6}$")
	handlerPattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)
	styles         = map[string][]string{
		"text":   {"", "body", "muted", "strong", "heading", "title", "code", "error", "success"},
		"button": {"", "default", "primary", "danger", "ghost", "outline", "bordered"},
		"menu":   {"", "default", "outline"},
		"badge":  {"", "default", "accent", "success", "warning", "danger", "count"},
		"tabs":   {"", "default", "segmented", "control"},
		"toggle": {"", "default", "switch"},
		"item":   {"", "code"},
		"card":   {"", "default", "list"},
		"input":  {"", "default", "search"},
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
	if n.ID != "" || k == control || k == dialog {
		if !idPattern.MatchString(n.ID) {
			return fail("a %s needs an id of 1 to 128 letters, digits or _.:/-, not %q", n.Type, n.ID)
		}
		if c.ids[n.ID] {
			return fail("the id %q is used twice", n.ID)
		}
		c.ids[n.ID] = true
	}
	if k != container && k != dialog && len(n.Children) > 0 {
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
	if n.Gap < 0 || n.Gap > MaxSpacing || len(n.Padding) == 3 || len(n.Padding) > 4 || slices.ContainsFunc(n.Padding, func(p int) bool { return p < 0 || p > MaxSpacing }) {
		return fail("gap and padding are 0 to %d, and padding is 1, 2 or 4 of them", MaxSpacing)
	}
	if n.Type == "native" {
		// What it is comes first: a locked or unknown name says so.
		if err := checkNative(n); err != nil {
			return fail("%v", err)
		}
	}
	if err := c.handlers(n); err != nil {
		return fail("%v", err)
	}
	if err := c.extra(n); err != nil {
		return fail("%v", err)
	}
	if err := c.value(n); err != nil {
		return fail("%v", err)
	}
	for field, list := range map[string][]*Node{"children": n.Children, "trailing": n.Trailing, "actions": n.Actions} {
		for i, ch := range list {
			if err := c.node(ch, path+"."+field+"["+strconv.Itoa(i)+"]", depth+1); err != nil {
				return err
			}
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
		"tabs":   {"onChange"},
		"menu":   {"onChange"},
		"dialog": {"onClose"},
		"item":   {"onClick"},
		"swatch": {"onClick"},
		"native": Natives[n.Name].Fires,
	}[n.Type]
	for name, h := range map[string]*Handler{"onClick": n.OnClick, "onChange": n.OnChange, "onSubmit": n.OnSubmit, "onClose": n.OnClose} {
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
	choosing := n.Type == "select" || n.Type == "tabs" || n.Type == "menu"
	if !choosing && len(n.Options) > 0 {
		return fmt.Errorf("a %s has no options", n.Type)
	}
	if n.Multiline && n.Type != "input" {
		return fmt.Errorf("a %s is not multiline", n.Type)
	}
	if n.Controlled && n.Type != "input" && n.Type != "toggle" && n.Type != "select" && n.Type != "tabs" {
		return fmt.Errorf("a %s is not controlled", n.Type)
	}
	if choosing && (len(n.Options) == 0 || len(n.Options) > MaxOptions) {
		return fmt.Errorf("a %s has 1 to %d options", n.Type, MaxOptions)
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
	case "select", "tabs":
		var s string
		if json.Unmarshal(n.Value, &s) != nil {
			return fmt.Errorf("a %s's value is a string", n.Type)
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
	for _, ch := range slices.Concat(n.Children, n.Trailing, n.Actions) {
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

// extra checks the fields of the types that have their own: dialogs,
// tables, images, icons, and the colors any widget may take.
func (c *checker) extra(n *Node) error {
	if n.Type == "swatch" && (!hexColor.MatchString(n.Color) || n.Label == "") {
		return fmt.Errorf("a swatch has a color as #rrggbb, and a label")
	}
	for _, col := range []string{n.Color, n.Background} {
		if col != "" && !contains(Colors, col) && n.Type != "swatch" {
			return fmt.Errorf("color %q is none of %v", col, Colors)
		}
	}
	if n.Open && n.Type != "dialog" {
		return fmt.Errorf("a %s doesn't open", n.Type)
	}
	if (len(n.Columns) > 0 || len(n.Rows) > 0) && n.Type != "table" {
		return fmt.Errorf("a %s has no columns or rows", n.Type)
	}
	if n.Src != "" && n.Type != "image" || n.Name != "" && n.Type != "icon" && n.Type != "native" {
		return fmt.Errorf("a %s has no src or name", n.Type)
	}
	if (len(n.Trailing) > 0 || len(n.Actions) > 0) && n.Type != "item" {
		return fmt.Errorf("a %s has no trailing or actions", n.Type)
	}
	if len(n.Props) > 0 && n.Type != "native" {
		return fmt.Errorf("a %s has no props", n.Type)
	}
	if n.Size != "" && (n.Size != "small" || n.Type != "text" && n.Style != "ghost") {
		return fmt.Errorf("size is small, of a text or a ghost button, not %q of a %s", n.Size, n.Type)
	}
	for _, o := range n.Options {
		if (o.Disabled || o.Tooltip != "") && n.Style != "segmented" {
			return fmt.Errorf("only segmented tabs' options are disabled or have a tooltip")
		}
		if len(o.Tooltip) > MaxTooltip {
			return fmt.Errorf("a tooltip is up to %d bytes", MaxTooltip)
		}
	}
	if (n.Width != 0 || n.Height != 0 || n.Radius != 0) && n.Type != "image" && (n.Type != "select" || n.Height != 0 || n.Radius != 0) {
		return fmt.Errorf("a %s has no width, height or radius", n.Type)
	}
	if n.Type == "select" && n.Width != 0 && (n.Width < 40 || n.Width > 480) {
		return fmt.Errorf("a select's width is 40 to 480")
	}
	switch {
	case n.FontSize != 0 && n.Type != "text" && n.Type != "icon", n.Weight != 0 && n.Type != "text":
		return fmt.Errorf("a %s has no fontSize or weight", n.Type)
	case n.FontSize != 0 && (n.FontSize < 8 || n.FontSize > 48), n.Weight != 0 && (n.Weight < 100 || n.Weight > 900):
		return fmt.Errorf("fontSize is 8 to 48, and weight 100 to 900")
	case len(n.Keys) > 0 && n.Type != "keys", n.Type == "keys" && (len(n.Keys) == 0 || len(n.Keys) > 8):
		return fmt.Errorf("keys are a keys widget's, 1 to 8 of them")
	case slices.ContainsFunc(n.Keys, func(k string) bool { return k == "" || len(k) > 16 }):
		return fmt.Errorf("a key is 1 to 16 bytes")
	case n.Progress && (n.Type != "button" || n.Style == "ghost" || n.Style == "outline" || n.Style == "bordered"):
		return fmt.Errorf("progress is a plain button's")
	case n.Href != "" && n.Type != "link":
		return fmt.Errorf("a %s has no href", n.Type)
	case n.Type == "link" && (n.Text == "" || !strings.HasPrefix(n.Href, "https://") && !strings.HasPrefix(n.Href, "http://") || len(n.Href) > 2048):
		return fmt.Errorf("a link has a text and an http or https href")
	}
	for _, o := range n.Options {
		if o.Separator && (n.Type != "menu" || o.Value != "" || o.Label != "") {
			return fmt.Errorf("only a menu has separators, which have no value or label")
		}
	}
	drawn := n.Style == "ghost" || n.Style == "outline" || n.Style == "bordered" || n.Type == "item"
	switch {
	case n.Tooltip != "" && n.Type != "button" && n.Type != "menu":
		return fmt.Errorf("a %s has no tooltip", n.Type)
	case len(n.Tooltip) > MaxTooltip || len(n.Badge) > MaxBadge:
		return fmt.Errorf("a tooltip is up to %d bytes and a badge up to %d", MaxTooltip, MaxBadge)
	case (n.Icon != "" || n.Compact) && !drawn:
		return fmt.Errorf("only a ghost or outline %s has an icon, or a bordered one", n.Type)
	case n.Style == "ghost" && n.Icon == "":
		return fmt.Errorf("a ghost button shows an icon, which it doesn't name")
	case n.Type == "item" && n.Icon == "":
		return fmt.Errorf("an item shows an icon, which it doesn't name")
	case n.Compact && (n.Icon == "" || n.Type == "item"):
		return fmt.Errorf("a compact %s shows its icon alone, which it doesn't name", n.Type)
	case n.Badge != "" && n.Style != "ghost", n.Active && n.Style != "ghost" && n.Type != "item" && n.Type != "swatch":
		return fmt.Errorf("only a ghost button is active or has a badge, and an item is active")
	}
	switch n.Type {
	case "table":
		if len(n.Columns) == 0 || len(n.Columns) > MaxColumns || len(n.Rows) > MaxRows {
			return fmt.Errorf("a table has 1 to %d columns and up to %d rows", MaxColumns, MaxRows)
		}
		cols := map[string]bool{}
		for _, col := range n.Columns {
			if col.ID == "" || cols[col.ID] || len(col.Title) > MaxText || col.Width < 0 || col.Width > MaxSize {
				return fmt.Errorf("a table's columns each have an id of their own, a title and a width of 0 to %d", MaxSize)
			}
			cols[col.ID] = true
		}
		keys := map[string]bool{}
		for _, r := range n.Rows {
			if r.Key == "" || keys[r.Key] {
				return fmt.Errorf("a table's rows each have a key of their own")
			}
			keys[r.Key] = true
			for id, text := range r.Cells {
				if !cols[id] || len(text) > MaxText {
					return fmt.Errorf("row %q: cell %q is no column, or over %d bytes", r.Key, id, MaxText)
				}
			}
		}
	case "image":
		if !strings.HasPrefix(n.Src, "plugin:") && !strings.HasPrefix(n.Src, "https://") && n.Src != "app:icon" {
			return fmt.Errorf("an image's src is plugin:<path> or an https address, or app:icon")
		}
		if strings.Contains(n.Src, "..") || len(n.Src) > 2048 {
			return fmt.Errorf("an image's src may not leave the plugin's folder")
		}
		if n.Width < 0 || n.Height < 0 || n.Width > MaxSize || n.Height > MaxSize || n.Radius < 0 || n.Radius > MaxSize {
			return fmt.Errorf("an image's width, height and radius are 0 to %d", MaxSize)
		}
	case "icon":
		if n.Name == "" || len(n.Name) > 128 || strings.Contains(n.Name, "..") {
			return fmt.Errorf("an icon needs a name")
		}
	case "dialog":
		if n.Title == "" {
			return fmt.Errorf("a dialog needs a title")
		}
	}
	return nil
}

// checkNative checks that a native widget is one of Natives, and that it
// is passed only the props it takes.
func checkNative(n *Node) error {
	if why, ok := Locked[n.Name]; ok {
		return fmt.Errorf("%s is off limits to plugins: %s", n.Name, why)
	}
	nat, ok := Natives[n.Name]
	if !ok {
		return fmt.Errorf("no native widget %q", n.Name)
	}
	size := 0
	for name, raw := range n.Props {
		size += len(name) + len(raw)
		var v any
		if json.Unmarshal(raw, &v) != nil {
			return fmt.Errorf("prop %s is no JSON value", name)
		}
		want, ok := nat.Props[name]
		if !ok {
			return fmt.Errorf("%s takes no prop %q", n.Name, name)
		}
		got := ""
		switch v.(type) {
		case string:
			got = "string"
		case bool:
			got = "bool"
		case float64:
			got = "number"
		}
		if got != want {
			return fmt.Errorf("%s's prop %s is a %s", n.Name, name, want)
		}
	}
	if size > MaxPayload {
		return fmt.Errorf("props over %d bytes", MaxPayload)
	}
	return nil
}
