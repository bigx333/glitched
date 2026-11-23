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
	"errors"
	"fmt"
	"math"
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

	// A list's bounds: its items are data, not widgets, so they count
	// toward neither MaxNodes nor MaxDepth, and a view with a list may be
	// MaxListBytes. A row the plugin draws is MaxRowNodes at most.
	MaxListItems = 20000
	MaxListBytes = 4 << 20
	MaxItemKey   = 256
	MaxItemLabel = 1024
	MaxRowNodes  = 200
	MaxRowBytes  = 16 << 10
	MinRowHeight = 8
	MaxRowHeight = 400
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
	"tile":     leaf,
	"list":     control,
}

// Native is a widget Glitched draws itself, which a plugin only places:
// a surface too heavy or too live for widgets, as the diff of a file.
type Native struct {
	// Props are what a plugin may pass it, by name, each "string", "bool"
	// or "number".
	Props map[string]string
	// In are the components it draws in; anywhere else it says so in its
	// place. Thread says it draws about the copy's own thread; Row that it
	// sits only in a list's row, and draws about the thread the row's item
	// names.
	In     []string
	Thread bool
	// Inline draws it straight into its parent, with no box of its own,
	// so that it takes and gives up room in a row as the view it draws
	// does where Glitched draws it itself.
	Inline bool
	Row    bool
	// InRow says it may sit in a list's row too, though it draws about
	// no thread, as a section's header among the sidebar's rows.
	InRow bool
	// Fires are the handlers it fires.
	Fires []string
}

// Natives are the native widgets a plugin may place, by name. Only these
// draw: a name that isn't one, a locked one included, refuses the view.
var Natives = map[string]Native{
	"diff.body":       {Props: map[string]string{"path": "string"}, In: []string{"thread.panel.diff"}, Thread: true, Fires: []string{"onChange"}},
	"theme.card":      {Props: map[string]string{"theme": "string"}, In: []string{"settings.page.appearance"}, Fires: []string{"onChange"}},
	"composer.input":  {In: []string{"composer"}, Thread: true},
	"transcript.body": {Props: map[string]string{"bare": "bool"}, In: []string{"timeline.message"}, Thread: true},
	// transcript.retry is a failed command's Retry and Dismiss, by its
	// row's error or under its message's note.
	"transcript.retry": {In: []string{"timeline.message"}, Thread: true},
	// A message's actions, as Glitched draws them under it: Copy, Edit &
	// resend (a message of yours), Retry (a turn that failed or was
	// stopped), and the command a plugin put in message.actions at index.
	// One with nothing to do under a message takes no room.
	"message.copy":  {In: []string{"message.toolbar"}, Thread: true},
	"message.edit":  {In: []string{"message.toolbar"}, Thread: true},
	"message.retry": {In: []string{"message.toolbar"}, Thread: true},
	"message.item":  {Props: map[string]string{"index": "number"}, In: []string{"message.toolbar"}, Thread: true},
	// thread.crumb is the header's button back to a thread above the one
	// shown, by its id.
	"thread.crumb": {Props: map[string]string{"thread": "string"}, In: []string{"thread.header.breadcrumb"}, Thread: true, Inline: true},
	// thread.heading is the title of the thread shown, in the header.
	"thread.heading": {In: []string{"thread.header.breadcrumb"}, Thread: true, Inline: true},
	// The sidebar's own parts of a thread's row, which its plugin's rows
	// draw: the row's actions while it is pointed at, the field that
	// renames it, its project's tile with its state and the jokes snooze
	// and settle play on it, its state, and its children's toggle; and how
	// long ago it was last active, as the palette shows it.
	"thread.row.actions": {In: []string{"sidebar.body"}, Row: true},
	"thread.rename":      {In: []string{"sidebar.body"}, Row: true},
	"thread.tile":        {In: []string{"sidebar.body"}, Row: true},
	// The model picker's parts: a model's or, with family, a family's
	// tile; the effort's name in its color; fast mode's lightning while
	// it's on; and the effort slider with fast mode's switch.
	"model.tile":    {Props: map[string]string{"family": "string", "size": "number"}, In: []string{"composer.model"}, InRow: true},
	"model.level":   {In: []string{"composer.model"}},
	"model.zap":     {In: []string{"composer.model"}},
	"model.effort":  {In: []string{"composer.model"}},
	"thread.status": {Props: map[string]string{"dot": "bool", "size": "number"}, In: []string{"sidebar.body", "palette", "agents.body"}, Row: true},
	"thread.age":    {In: []string{"palette", "agents.body"}, Row: true},
	// thread.actions is the … that opens the thread's menu, as the agents
	// panel's rows show it while pointed at.
	"thread.actions":  {In: []string{"agents.body"}, Row: true},
	"thread.children": {In: []string{"sidebar.body"}, Row: true},
	// The rest of a sidebar row's parts: its title, bold while unread, a
	// child's smaller; what it shows at its end while it isn't pointed at;
	// what plugins place in each row; and its machine, where there are
	// several. And a section's header, which opens and closes it.
	"thread.title":    {Props: map[string]string{"child": "bool"}, In: []string{"sidebar.body", "agents.body"}, Row: true},
	"thread.mark":     {Props: map[string]string{"child": "bool"}, In: []string{"sidebar.body"}, Row: true},
	"thread.slot":     {In: []string{"sidebar.body"}, Row: true},
	"thread.machine":  {In: []string{"sidebar.body"}, Row: true},
	"sidebar.section": {Props: map[string]string{"section": "string"}, In: []string{"sidebar.body"}, InRow: true},
}

// Locked are the parts of Glitched's UI no plugin draws, replaces or
// places, and why. No manifest or grant changes them.
var Locked = map[string]string{
	"approval.card":       "an approval answers for you, so only Glitched draws it",
	"question.card":       "an answer to an agent's question is yours, so only Glitched draws it",
	"settings.plugins":    "it is where you turn plugins off, so no plugin draws it",
	"settings.servers":    "pairing an engine trusts a machine, so only Glitched draws it",
	"settings.providers":  "it chooses the program Glitched runs as an agent, so only Glitched draws it",
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
	// default or switch; an item's text plain or code; an input's default,
	// search, or bare: no frame, filling its row and taking the keyboard
	// as it shows, as the palette's search field.
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

	Gap     float32 `json:"gap,omitempty"`
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
	Src    string  `json:"src,omitempty"`
	Width  float32 `json:"width,omitempty"`
	Height float32 `json:"height,omitempty"`
	Radius float32 `json:"radius,omitempty"`
	// Name is an icon's: one of the app's, or "plugin:<path>.svg".
	Name string `json:"name,omitempty"`
	// Color and Background are theme tokens (Colors); a swatch's or a
	// tile's color is the color it shows, as #rrggbb, and a ghost
	// button's either. A select's Width is in points, as is a tile's
	// size.
	Color      string `json:"color,omitempty"`
	Background string `json:"background,omitempty"`
	// Border is a row's or column's border one point wide, a theme token
	// drawn at BorderOpacity, 0 to 1, where that is set. A row's or
	// column's Radius rounds its corners, 8 where it has a background and
	// no radius, and Corners each of them: top-left, top-right,
	// bottom-right and bottom-left. MaxWidthPercent caps a row's or
	// column's width at that share of its parent's, 1 to 100.
	Border          string    `json:"border,omitempty"`
	BorderOpacity   *float64  `json:"borderOpacity,omitempty"`
	Corners         []float32 `json:"corners,omitempty"`
	MaxWidthPercent int       `json:"maxWidthPercent,omitempty"`

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

	// Italic slants a text.
	Italic bool `json:"italic,omitempty"`
	// SingleLine keeps a text on one line, cut short where it doesn't fit.
	SingleLine bool `json:"singleLine,omitempty"`
	// Popover is a ghost button's panel, a column anchored under it,
	// shown while Open; OnClose fires as the user dismisses it. The
	// column's Width is the panel's, 120 to 480.
	Popover *Node `json:"popover,omitempty"`
	popover bool  // is one
	// Wrap lets a row's children run onto more lines. Opacity dims a
	// button, 0 to 1. ContextMenu names the menu of Glitched's a ghost
	// button or a clicked row opens on a right click: "thread:<id>" or
	// "project:<key>", with what plugins add to it.
	Wrap        bool    `json:"wrap,omitempty"`
	Opacity     float64 `json:"opacity,omitempty"`
	ContextMenu string  `json:"contextMenu,omitempty"`

	// Name is also a native widget's (Natives), and Props what it is
	// passed.
	Props map[string]json.RawMessage `json:"props,omitempty"`

	// Items are a list's, every one of them, in order: Glitched draws the
	// rows in view and asks the plugin's rows export for those alone.
	// RowHeight is an item's height when it names none; Height caps the
	// list, which is as tall as its rows up to it. Input names an input of
	// the view whose Up, Down and Enter move and submit the list.
	Items     []ListItem `json:"items,omitempty"`
	RowHeight float32    `json:"rowHeight,omitempty"`
	Input     string     `json:"input,omitempty"`
	// SubmitOnClick makes a click on a list's row submit it as well as
	// choose it, as a palette's does. ChooseOnHover makes the row pointed
	// at the choice; WrapAround moves the choice from one end to the
	// other, as a palette's Up and Down do.
	SubmitOnClick bool `json:"submitOnClick,omitempty"`
	ChooseOnHover bool `json:"chooseOnHover,omitempty"`
	WrapAround    bool `json:"wrapAround,omitempty"`
	// NoKeys keeps the keys out of a list: it takes no focus, and only
	// the pointer chooses, as the sidebar's threads.
	NoKeys bool `json:"noKeys,omitempty"`
	// When shows a widget of a list's row only while the row is pointed
	// at ("hover") or only while it isn't ("idle").
	When string `json:"when,omitempty"`
}

// ListItem is one row of a list, as its view names it: Key is its
// identity, which the choice and the rows export use; Label its name to
// assistive tools; Version changes when its row must be drawn again;
// Height is its own; a Header heads a section and is never chosen; Thread
// gives the row Glitched's menu of that thread.
type ListItem struct {
	Key     string  `json:"key"`
	Label   string  `json:"label"`
	Version string  `json:"version,omitempty"`
	Height  float32 `json:"height,omitempty"`
	Header  bool    `json:"header,omitempty"`
	Thread  string  `json:"thread,omitempty"`
}

// ItemHeight is the height of the list's item i: its own, else the list's
// RowHeight, else 32.
func (n *Node) ItemHeight(i int) float32 {
	if h := n.Items[i].Height; h > 0 {
		return h
	}
	if n.RowHeight > 0 {
		return n.RowHeight
	}
	return 32
}

// Column is a table's column.
type Column struct {
	ID    string  `json:"id"`
	Title string  `json:"title"`
	Width float32 `json:"width,omitempty"`
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
type Spacing []float32

// UnmarshalJSON takes a number or a list of numbers.
func (s *Spacing) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] != '[' {
		var n float32
		if err := json.Unmarshal(b, &n); err != nil {
			return err
		}
		*s = Spacing{n}
		return nil
	}
	return json.Unmarshal(b, (*[]float32)(s))
}

// Sides are the padding's top, right, bottom and left.
func (s Spacing) Sides() (t, r, b, l float32) {
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
		"text":    {"", "body", "muted", "strong", "heading", "title", "code", "error", "success"},
		"button":  {"", "default", "primary", "danger", "ghost", "outline", "bordered"},
		"menu":    {"", "default", "outline", "ghost"},
		"badge":   {"", "default", "accent", "success", "warning", "danger", "count", "pill"},
		"tabs":    {"", "default", "segmented", "control"},
		"toggle":  {"", "default", "switch"},
		"item":    {"", "code"},
		"card":    {"", "default", "list"},
		"input":   {"", "default", "search", "bare"},
		"divider": {"", "vertical"},
	}
	aligns = []string{"", "start", "center", "end", "stretch"}
	// ghostContent is what a ghost button may show in place of its icon.
	ghostContent = []string{"text", "tile", "icon", "badge", "spacer"}
	justifys     = []string{"", "start", "center", "end", "between"}
)

// Decode reads a view from a view export's JSON and checks it.
func Decode(b []byte) (*View, error) { return DecodeWithin(b, MaxNodes) }

// DecodeWithin is Decode with a bound of its own on the widgets: a place's
// copy is a badge, not a page.
func DecodeWithin(b []byte, maxNodes int) (*View, error) {
	// Only a view with a list may pass MaxBytes, which its JSON says.
	limit := MaxBytes
	if bytes.Contains(b, []byte(`"list"`)) {
		limit = MaxListBytes
	}
	if len(b) > limit {
		return nil, fmt.Errorf("view is %d bytes, over the %d allowed", len(b), limit)
	}
	var v View
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(&v); err != nil {
		return nil, fmt.Errorf("view: %w", whole(err))
	}
	if err := v.check(maxNodes); err != nil {
		return nil, err
	}
	// The bytes past MaxBytes are the lists' items', never other widgets':
	// the view without its items is MaxBytes at most.
	if len(b) > MaxBytes {
		var lists []*Node
		v.Body.Walk(func(n *Node) {
			if len(n.Items) > 0 {
				lists = append(lists, n)
			}
		})
		kept := make([][]ListItem, len(lists))
		for i, n := range lists {
			kept[i], n.Items = n.Items, nil
		}
		var rest bytes.Buffer
		e := json.NewEncoder(&rest)
		e.SetEscapeHTML(false)
		err := e.Encode(&v)
		for i, n := range lists {
			n.Items = kept[i]
		}
		if err != nil || rest.Len() > MaxBytes {
			return nil, fmt.Errorf("view is %d bytes besides its lists' items, over the %d allowed", rest.Len(), MaxBytes)
		}
	}
	return &v, nil
}

// DecodeRow reads one row of a list, as the rows export draws it, and
// checks it. A row holds no control that keeps a value, no dialog, popover,
// list or native widget: it is drawn and dropped as it scrolls.
func DecodeRow(b []byte) (*Node, error) {
	if len(b) > MaxRowBytes {
		return nil, fmt.Errorf("row is %d bytes, over the %d allowed", len(b), MaxRowBytes)
	}
	var v View
	d := json.NewDecoder(bytes.NewReader(append(append([]byte(`{"body":`), b...), '}')))
	d.DisallowUnknownFields()
	if err := d.Decode(&v); err != nil {
		return nil, fmt.Errorf("row: %w", whole(err))
	}
	if v.Body == nil {
		return nil, fmt.Errorf("row: no widget")
	}
	c := checker{ids: map[string]bool{}, max: MaxRowNodes, row: true}
	if err := c.node(v.Body, "row", 1); err != nil {
		return nil, err
	}
	return v.Body, nil
}

func (n *Node) find(ok func(*Node) bool) *Node {
	var found *Node
	n.Walk(func(n *Node) {
		if found == nil && ok(n) {
			found = n
		}
	})
	return found
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
	if err := c.node(v.Body, "body", 1); err != nil {
		return err
	}
	inputs := map[string]bool{}
	for _, l := range c.lists {
		if l.Input == "" {
			continue
		}
		if inputs[l.Input] {
			return fmt.Errorf("list %q: the input %q moves another list", l.ID, l.Input)
		}
		inputs[l.Input] = true
		in := v.Body.find(func(n *Node) bool { return n.ID == l.Input })
		if in == nil || in.Type != "input" || in.Multiline || in.OnSubmit != nil {
			return fmt.Errorf("list %q: its input %q is no single-line input of the view without an onSubmit: Enter in it submits the list", l.ID, l.Input)
		}
	}
	return nil
}

type checker struct {
	max   int
	nodes int
	ids   map[string]bool
	// row is set while checking a list's row; lists are the lists seen.
	row   bool
	lists []*Node
}

// whole says which field takes a whole number, where json says a number
// with a fraction doesn't fit an int. Points take fractions; a count, a
// weight or a percent doesn't.
func whole(err error) error {
	var t *json.UnmarshalTypeError
	if !errors.As(err, &t) || !strings.HasPrefix(t.Type.Kind().String(), "int") {
		return err
	}
	n, ok := strings.CutPrefix(t.Value, "number ")
	if f, perr := strconv.ParseFloat(n, 64); !ok || perr != nil || f == math.Trunc(f) {
		return err
	}
	return fmt.Errorf("%s is a whole number, not %s", t.Field, n)
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
	if k != container && k != dialog && len(n.Children) > 0 && !((n.Type == "button" || n.Type == "menu") && n.Style == "ghost") {
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
	if n.Gap < 0 || n.Gap > MaxSpacing || len(n.Padding) == 3 || len(n.Padding) > 4 || slices.ContainsFunc(n.Padding, func(p float32) bool { return p < 0 || p > MaxSpacing }) {
		return fail("gap and padding are 0 to %d, and padding is 1, 2 or 4 of them", MaxSpacing)
	}
	if n.Type == "native" {
		// What it is comes first: a locked or unknown name says so.
		if err := checkNative(n); err != nil {
			return fail("%v", err)
		}
	}
	if err := c.list(n); err != nil {
		return fail("%v", err)
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
	if n.Popover != nil {
		if n.Popover.Type != "column" {
			return fail("a popover is a column")
		}
		n.Popover.popover = true
		if err := c.node(n.Popover, path+".popover", depth+1); err != nil {
			return err
		}
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
		"button": {"onClick", "onClose"},
		"input":  {"onChange", "onSubmit"},
		"toggle": {"onChange"},
		"select": {"onChange"},
		"tabs":   {"onChange"},
		"menu":   {"onChange"},
		"dialog": {"onClose"},
		"item":   {"onClick"},
		"swatch": {"onClick"},
		"row":    {"onClick"},
		"column": {"onClick"},
		"list":   {"onChange", "onSubmit"},
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
	if n.Controlled && n.Type != "input" && n.Type != "toggle" && n.Type != "select" && n.Type != "tabs" && n.Type != "list" {
		return fmt.Errorf("a %s is not controlled", n.Type)
	}
	// A ghost menu may have none: Glitched's own items may end it.
	if choosing && (len(n.Options) == 0 && !(n.Type == "menu" && n.Style == "ghost") || len(n.Options) > MaxOptions) {
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
	case "list":
		var s string
		if json.Unmarshal(n.Value, &s) != nil {
			return fmt.Errorf("a list's value is the key of the item chosen")
		}
		if s != "" && !slices.ContainsFunc(n.Items, func(it ListItem) bool { return it.Key == s && !it.Header }) {
			return fmt.Errorf("value %q is no item of the list, or a header", s)
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
	n.Popover.Walk(fn)
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
	if n.Type == "tile" && (!hexColor.MatchString(n.Color) || n.Icon == "" || n.Width < 8 || n.Width > 64) {
		return fmt.Errorf("a tile has a color as #rrggbb, an icon, and a width of 8 to 64")
	}
	ghost := n.Type == "button" && n.Style == "ghost"
	rawColor := n.Type == "swatch" || n.Type == "tile" || ghost && hexColor.MatchString(n.Color)
	for i, col := range []string{n.Color, n.Background, n.Border} {
		if col != "" && !contains(Colors, col) && !(rawColor && i == 0) {
			return fmt.Errorf("color %q is none of %v", col, Colors)
		}
	}
	switch {
	case n.Open && n.Type != "dialog" && n.Popover == nil:
		return fmt.Errorf("a %s doesn't open", n.Type)
	case n.Popover != nil && !ghost:
		return fmt.Errorf("only a ghost button has a popover")
	case n.Popover != nil && (n.Popover.Width < 120 || n.Popover.Width > 480):
		return fmt.Errorf("a popover's width is 120 to 480")
	case n.OnClose != nil && n.Type == "button" && n.Popover == nil:
		return fmt.Errorf("only a button with a popover has onClose")
	case n.Italic && n.Type != "text":
		return fmt.Errorf("a %s isn't italic", n.Type)
	case n.SingleLine && n.Type != "text":
		return fmt.Errorf("a %s has no singleLine", n.Type)
	case n.Wrap && n.Type != "row":
		return fmt.Errorf("only a row wraps")
	case n.Opacity != 0 && (n.Type != "button" || n.Opacity < 0.2 || n.Opacity > 1):
		return fmt.Errorf("opacity is a button's, 0.2 to 1")
	case n.Style == "ghost" && slices.ContainsFunc(n.Children, func(ch *Node) bool { return ch == nil || !contains(ghostContent, ch.Type) }):
		return fmt.Errorf("a ghost button's children are %v", ghostContent)
	case n.ContextMenu != "" && !ghost && !(n.Type == "row" && n.OnClick != nil):
		return fmt.Errorf("only a ghost button or a clicked row has a contextMenu")
	case n.ContextMenu != "" && (!strings.HasPrefix(n.ContextMenu, "thread:") && !strings.HasPrefix(n.ContextMenu, "project:") || len(n.ContextMenu) > 1024):
		return fmt.Errorf("a contextMenu is thread:<id> or project:<key>")
	case n.OnClick != nil && (n.Type == "row" || n.Type == "column") && n.ID == "":
		return fmt.Errorf("a %s that is clicked needs an id", n.Type)
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
	box := n.Type == "row" || n.Type == "column"
	switch {
	case (n.Border != "" || n.BorderOpacity != nil || n.Corners != nil || n.MaxWidthPercent != 0) && !box:
		return fmt.Errorf("a %s has no border, borderOpacity, corners or maxWidthPercent", n.Type)
	case n.BorderOpacity != nil && (n.Border == "" || *n.BorderOpacity < 0 || *n.BorderOpacity > 1):
		return fmt.Errorf("a borderOpacity is 0 to 1, of a border")
	case n.Corners != nil && (len(n.Corners) != 4 || slices.ContainsFunc(n.Corners, func(r float32) bool { return r < 0 || r > MaxSize })):
		return fmt.Errorf("corners are 4 radii, each 0 to %d", MaxSize)
	case n.MaxWidthPercent < 0 || n.MaxWidthPercent > 100:
		return fmt.Errorf("a maxWidthPercent is 1 to 100")
	case box && (n.Radius < 0 || n.Radius > MaxSize):
		return fmt.Errorf("a radius is 0 to %d", MaxSize)
	case box && (n.Width < 0 || n.Height < 0 || n.Width > MaxSize || n.Height > MaxSize):
		return fmt.Errorf("a width or a height is 0 to %d", MaxSize)
	}
	if !box && (n.Width != 0 || n.Height != 0 || n.Radius != 0) && n.Type != "image" && !(n.Type == "list" && n.Width == 0 && n.Radius == 0) && (n.Type != "select" && n.Type != "tile" && !n.popover || n.Height != 0 || n.Radius != 0) {
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
	drawn := n.Style == "ghost" || n.Style == "outline" || n.Style == "bordered" || n.Type == "item" || n.Type == "tile"
	switch {
	case n.Tooltip != "" && n.Type != "button" && n.Type != "menu" && !(n.Type == "badge" && n.Style == "pill"):
		return fmt.Errorf("a %s has no tooltip", n.Type)
	case len(n.Tooltip) > MaxTooltip || len(n.Badge) > MaxBadge:
		return fmt.Errorf("a tooltip is up to %d bytes and a badge up to %d", MaxTooltip, MaxBadge)
	case (n.Icon != "" || n.Compact) && !drawn:
		return fmt.Errorf("only a ghost or outline %s has an icon, or a bordered one", n.Type)
	case n.Style == "ghost" && n.Icon == "" && len(n.Children) == 0:
		return fmt.Errorf("a ghost button shows an icon or its children, which it hasn't")
	case n.Style == "ghost" && n.Icon != "" && len(n.Children) > 0:
		return fmt.Errorf("a ghost button shows an icon or its children, not both")
	case n.Type == "item" && n.Icon == "":
		return fmt.Errorf("an item shows an icon, which it doesn't name")
	case n.Compact && (n.Icon == "" || n.Type == "item"):
		return fmt.Errorf("a compact %s shows its icon alone, which it doesn't name", n.Type)
	case n.Badge != "" && (n.Style != "ghost" || n.Type == "menu"), n.Active && n.Style != "ghost" && n.Type != "item" && n.Type != "swatch":
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

// list checks a list's own fields, and what a list's row holds.
func (c *checker) list(n *Node) error {
	if c.row {
		switch {
		case n.Type == "native" && !Natives[n.Name].Row && !Natives[n.Name].InRow:
			return fmt.Errorf("a list's row holds no native %s", n.Name)
		case n.Type == "input" || n.Type == "toggle" || n.Type == "select" || n.Type == "tabs" || n.Type == "dialog" || n.Type == "list":
			return fmt.Errorf("a list's row holds no %s", n.Type)
		case n.Popover != nil || n.Progress:
			return fmt.Errorf("a list's row holds no popover or progress button")
		}
	}
	switch {
	case n.When != "" && !c.row:
		return fmt.Errorf("when is for a widget in a list's row")
	case n.When != "" && n.When != "hover" && n.When != "idle":
		return fmt.Errorf("when is hover or idle, not %q", n.When)
	case n.Type == "native" && Natives[n.Name].Row && !c.row:
		return fmt.Errorf("%s draws only in a list's row", n.Name)
	case n.Type != "list" && (len(n.Items) > 0 || n.RowHeight != 0 || n.Input != "" || n.SubmitOnClick || n.ChooseOnHover || n.WrapAround || n.NoKeys):
		return fmt.Errorf("a %s has no items, rowHeight, input, submitOnClick, chooseOnHover, wrapAround or noKeys", n.Type)
	case n.Type != "list":
		return nil
	case n.NoKeys && n.Input != "":
		return fmt.Errorf("a list with noKeys has no input: its keys would move it")
	case n.Label == "":
		return fmt.Errorf("a list needs a label, its name to assistive tools")
	case !n.Grow && n.Height == 0:
		return fmt.Errorf("a list grows or has a height: it draws only the rows in it")
	case len(n.Items) > MaxListItems:
		return fmt.Errorf("a list has up to %d items", MaxListItems)
	case n.RowHeight != 0 && (n.RowHeight < MinRowHeight || n.RowHeight > MaxRowHeight):
		return fmt.Errorf("a rowHeight is %d to %d", MinRowHeight, MaxRowHeight)
	case n.Height != 0 && (n.Height < MinRowHeight || n.Height > MaxSize):
		return fmt.Errorf("a list's height is %d to %d", MinRowHeight, MaxSize)
	}
	keys := make(map[string]bool, len(n.Items))
	for i, it := range n.Items {
		switch {
		case it.Key == "" || len(it.Key) > MaxItemKey:
			return fmt.Errorf("items[%d]: a key is 1 to %d bytes", i, MaxItemKey)
		case keys[it.Key]:
			return fmt.Errorf("items[%d]: the key %q is used twice", i, it.Key)
		case it.Label == "" && it.Thread == "" || len(it.Label) > MaxItemLabel:
			return fmt.Errorf("items[%d]: a label is 1 to %d bytes, or none for a thread's, whose row names it", i, MaxItemLabel)
		case len(it.Version) > MaxItemKey || len(it.Thread) > MaxItemKey:
			return fmt.Errorf("items[%d]: a version or a thread is up to %d bytes", i, MaxItemKey)
		case it.Height != 0 && (it.Height < MinRowHeight || it.Height > MaxRowHeight):
			return fmt.Errorf("items[%d]: a height is %d to %d", i, MinRowHeight, MaxRowHeight)
		case it.Header && it.Thread != "":
			return fmt.Errorf("items[%d]: a header has no thread", i)
		}
		keys[it.Key] = true
	}
	c.lists = append(c.lists, n)
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
