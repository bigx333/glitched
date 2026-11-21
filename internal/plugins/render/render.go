// Package render draws a plugin's widget tree with MyGo's components, and
// turns what the user does to it into events for the plugin's update.
package render

import (
	"cmp"
	"encoding/json"
	"math"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"

	"glitched/internal/plugins"
	"glitched/internal/plugins/widget"
)

// Style is the look the app gives the widgets.
type Style struct {
	Text, Muted, Faint, Accent, Red, Green, Amber, Border, Surface, Hover ui.Color
	// Card draws a card: its title over a body.
	Card func(c *ui.Context, title string, body func())
	// Icon is the icon a name gives, nil for none.
	Icon func(name string) *ui.SVG
	// Image is the picture at src, nil while it loads or if it can't.
	Image func(src string) ui.ImageSource
	// Scope names the view among every plugin view the window draws: a
	// dialog's overlay is keyed by it, as overlays share one layer.
	Scope string
	// Ghost and Outline make the app's own ghost and outline buttons, as
	// its header draws them, keyed by key in c; Tip gives an element its
	// tooltip in the app's look. Nil draws MyGo's.
	Ghost   func(c *ui.Context, key any, on bool) ui.Element
	Outline func(c *ui.Context, key any) ui.Element
	Tip     func(c *ui.Context, e ui.Element, tip string) ui.Element
	// Row makes the app's list row, keyed by key in c: selected shows at
	// once, hover fades. Nil draws a ghost button.
	Row func(c *ui.Context, key any, selected bool) ui.Element
	// ListCard is the app's card of rows with lines between them, as
	// Settings draws them; Keys its keycaps of a shortcut.
	ListCard func(c *ui.Context, title string, body func())
	Keys     func(c *ui.Context, keys []string)
	// Swatch is the app's color swatch, keyed by key in c, ringed while
	// on; it says whether it was clicked. Nil draws a plain dot.
	Swatch func(c *ui.Context, key any, color ui.Color, on bool, label string) bool
	// Popover draws a ghost button's panel, as the app's popovers are,
	// attached to anchor while *open, width wide: body fills it. A
	// dismissal sets *open false. Nil draws MyGo's.
	Popover func(c *ui.Context, anchor ui.Element, open *bool, width float32, body func())
	// ContextMenu gives e the menu of Glitched's that name names, as a
	// widget's contextMenu does. Nil gives none.
	ContextMenu func(c *ui.Context, e ui.Element, name string)
	// MenuEnd ends every menu with Glitched's own items, as a draft's
	// composer.workspace ends with where it could start and adding and
	// removing projects. Nil ends none.
	MenuEnd func(m *ui.Menu)
	// Tile draws a project's mark: its icon, white on its color. Nil
	// draws the icon on the color.
	Tile func(c *ui.Context, icon *ui.SVG, color ui.Color, size float32)
	// Native draws a native widget, which the checks let only Natives be,
	// in a list's row about the thread its item names, else "": fire gives
	// the plugin a change. Nil, outside a component, says it draws only in
	// one.
	Native func(c *ui.Context, n *widget.Node, thread string, fire func(value any))
	// ListRow does to the row e of a list's item i what the app does to
	// its own rows, as their transitions and the jokes played on them; it
	// says whether the row stays as though pointed at, as while a popover
	// of it is open. Nil does nothing.
	ListRow func(c *ui.Context, list *widget.Node, i int, e ui.Element) (held bool)
	// Reveal says which thread's item a list scrolls into view, where it
	// has one; nil is none.
	Reveal func(thread string) bool
	// Modal is told of each dialog the view draws open, which holds the
	// window's keys. Nil is told nothing.
	Modal func()
	// Rows is the row of a list's item as the plugin drew it, nil before
	// it did: the list asks for the rows in view alone. Nil draws none.
	Rows func(list string, it widget.ListItem) *widget.Node
	// WaitRows draws the rows of a list's items in view before they show,
	// those not drawn yet.
	WaitRows func(list string, items []widget.ListItem)
}

// color is a theme token's color.
func (st Style) color(token string) (ui.Color, bool) {
	c, ok := map[string]ui.Color{"$text": st.Text, "$text.muted": st.Muted, "$text.faint": st.Faint, "$accent": st.Accent,
		"$success": st.Green, "$warning": st.Amber, "$danger": st.Red, "$border": st.Border, "$surface": st.Surface}[token]
	return c, ok
}

// Controls keep the values of a view's inputs, toggles and selects, by
// their ids, across the plugin's views: what the user typed stays while
// the plugin answers, and a view only overrides it with a value the
// plugin changed.
type Controls struct {
	m   map[string]*control
	rev int
	// dismissed are the dialogs the user closed, by id, till a view says
	// again whether they're open.
	dismissed map[string]bool
	// pending are the progress buttons clicked, by id, with the click's
	// number: each shows it runs till a view answers after it.
	pending map[string]int
	// passes counts the view's builds: a list remembers the one it was
	// last drawn in, and an input moves only a list drawn.
	passes int
	// sent is the number of the last event sent, and processed of the last
	// the plugin took before it drew the view shown.
	sent, processed int
}

type control struct {
	kind string
	text string // an input's text, a select's value
	on   bool   // a toggle's
	// seen is the value the plugin last gave, as JSON, so that only a
	// value it changes overrides the user's.
	seen string
	// last is the number of the last change sent.
	last int
	// list is a list's place among its rows.
	list *listView
}

// Restart readies the controls for the views of a session started again,
// which counts its views and the events it took from the start: the next
// view syncs them as any new view does, and no change sent to the last
// session waits on this one.
func (cs *Controls) Restart() {
	cs.rev, cs.sent, cs.processed, cs.pending = -1, 0, 0, nil
	for _, ctl := range cs.m {
		ctl.last = 0
		if ctl.list != nil {
			ctl.list.pressed = nil
		}
	}
}

// Sync takes the values a new view gives, once per view: rev is the
// view's number, and processed the last event the plugin took before it
// answered the view. It forgets the controls the view no longer has.
//
// While the user has changes the plugin hasn't taken, a view never changes
// the control: it answers an older value. Once the plugin took them, a
// controlled control takes the view's value whenever it differs from what
// the control holds; another takes it only when it differs from the last
// view's.
func (cs *Controls) Sync(v *widget.View, rev, processed int) {
	if cs.m == nil {
		cs.m = map[string]*control{}
	}
	if v == nil || rev == cs.rev {
		return
	}
	cs.rev, cs.processed = rev, processed
	for id, seq := range cs.pending {
		if seq <= processed {
			delete(cs.pending, id)
		}
	}
	live := map[string]bool{}
	v.Body.Walk(func(n *widget.Node) {
		if n.Type == "dialog" || n.Popover != nil {
			delete(cs.dismissed, n.ID)
		}
		if n.Type != "input" && n.Type != "toggle" && n.Type != "select" && n.Type != "tabs" && n.Type != "list" {
			return
		}
		live[n.ID] = true
		ctl := cs.m[n.ID]
		raw := canon(n.Value)
		// A widget of another kind under the same id is a new control.
		if ctl == nil || ctl.kind != n.Type {
			ctl = &control{}
			cs.m[n.ID] = ctl
			ctl.set(n)
			ctl.seen = raw
			return
		}
		prev := ctl.seen
		ctl.seen = raw
		switch {
		case ctl.last > processed:
		case n.Controlled:
			if raw != "null" && raw != ctl.current() {
				ctl.set(n)
			}
		case raw != prev:
			ctl.set(n)
		}
	})
	for id := range cs.m {
		if !live[id] {
			cs.m[id].release()
			delete(cs.m, id)
		}
	}
}

// canon is a JSON value in one spelling, whichever encoder wrote it:
// JavaScript leaves <, > and & as they are, Go escapes them.
func canon(raw json.RawMessage) string {
	var v any
	if len(raw) == 0 || json.Unmarshal(raw, &v) != nil {
		return "null"
	}
	return encode(v)
}

func encode(v any) string {
	var b strings.Builder
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	e.Encode(v)
	return strings.TrimSuffix(b.String(), "\n")
}

func (ctl *control) current() string {
	if ctl.kind == "toggle" {
		return encode(ctl.on)
	}
	return encode(ctl.text)
}

func (ctl *control) set(n *widget.Node) {
	ctl.kind = n.Type
	switch n.Type {
	case "toggle":
		ctl.on = false
		json.Unmarshal(n.Value, &ctl.on)
	case "select", "tabs":
		ctl.text = ""
		json.Unmarshal(n.Value, &ctl.text)
		if ctl.text == "" && len(n.Options) > 0 {
			ctl.text = n.Options[0].Value
		}
	default:
		ctl.text = ""
		json.Unmarshal(n.Value, &ctl.text)
	}
}

// Values are the controls' values, by id, as update gets them.
func (cs *Controls) Values() map[string]any {
	out := make(map[string]any, len(cs.m))
	for id, ctl := range cs.m {
		if ctl.kind == "toggle" {
			out[id] = ctl.on
		} else {
			out[id] = ctl.text
		}
	}
	return out
}

// View draws the view's body. dispatch gets what the user does, with the
// handler the widget names, during the build pass.
func View(c *ui.Context, v *widget.View, cs *Controls, st Style, md *markdown.Options, dispatch func(plugins.UIEvent, map[string]any) int) {
	if v == nil {
		return
	}
	cs.passes++
	r := renderer{cs: cs, st: st, md: md, dispatch: dispatch}
	r.inputs, r.fields = map[string]*widget.Node{}, map[string]*widget.Node{}
	v.Body.Walk(func(n *widget.Node) {
		switch {
		case n.Type == "list" && n.Input != "":
			r.inputs[n.Input] = n
		case n.Type == "input":
			r.fields[n.ID] = n
		}
	})
	r.node(c, v.Body, 0)
}

type renderer struct {
	cs       *Controls
	st       Style
	md       *markdown.Options
	dispatch func(plugins.UIEvent, map[string]any) int
	// inputs are the lists the inputs of the view move, by the input's id,
	// and fields the view's inputs, by id.
	inputs, fields map[string]*widget.Node
	// row is the list's row being drawn, thread the thread its item
	// names, and hovered whether the pointer is over it; mods are the keys
	// held as a list is submitted.
	row     *plugins.RowRef
	thread  string
	hovered bool
	mods    []string
}

// NativeBox is the box the native n, child i of its parent, draws in. It
// lets the pointer through to what is under it, but for the native in it,
// as a composer's frame does. Glitched draws a built-in's native in the
// same box when the built-in has no drawing yet, so that the native keeps
// its state, as a selection, when the drawing comes.
func NativeBox(c *ui.Context, n *widget.Node, i int) ui.Element {
	return ui.Column(c.Key(key(n, i))).PassThrough()
}

func key(n *widget.Node, i int) string {
	if n.ID != "" {
		return "id:" + n.ID // ids have no #, which the controls' own keys add
	}
	return "at:" + strconv.Itoa(i)
}

var aligns = map[string]ui.Align{"start": ui.Start, "center": ui.Center, "end": ui.End, "stretch": ui.Stretch, "between": ui.SpaceBetween}

func (r *renderer) children(c *ui.Context, n *widget.Node) {
	for i, ch := range n.Children {
		r.node(c, ch, i)
	}
}

func (r *renderer) fire(n *widget.Node, h *widget.Handler, typ string, value any) {
	if h == nil || r.dispatch == nil {
		return
	}
	seq := r.dispatch(plugins.UIEvent{Type: typ, Handler: h.Name, Node: n.ID, Value: value, Payload: h.Payload, Row: r.row, Modifiers: r.mods}, r.cs.Values())
	r.cs.sent = max(r.cs.sent, seq)
	if r.row != nil {
		return // a row's widgets keep no values: its ids are its own
	}
	if ctl := r.cs.m[n.ID]; ctl != nil && typ == "change" && seq > 0 {
		ctl.last = seq
	}
	if n.Progress && typ == "click" && seq > 0 {
		if r.cs.pending == nil {
			r.cs.pending = map[string]int{}
		}
		r.cs.pending[n.ID] = seq
	}
}

func (r *renderer) node(c *ui.Context, n *widget.Node, i int) {
	st := r.st
	// A node that builds nothing takes no key: the key would go on the
	// next element built, wherever, giving it and all inside it another
	// identity, as a dialog the user closed, which the plugin draws till
	// it hears of it, gave the app's composer, whose text area, new, took
	// the focus.
	if n.When == "hover" && !r.hovered || n.When == "idle" && r.hovered || n.Type == "dialog" && (!n.Open || r.cs.dismissed[n.ID]) {
		return
	}
	// Stateful widgets take their key before they're built: a key set
	// after is a panic.
	kc := c.Key(key(n, i))
	switch n.Type {
	case "list":
		r.list(kc, n)
		return
	case "column", "row":
		newBox := ui.Column
		if n.Type == "row" {
			newBox = ui.Row
		}
		var e ui.Element
		switch {
		case n.OnClick != nil && st.Row != nil:
			e = st.Row(c, key(n, i), false)
		case n.OnClick != nil:
			e = ui.ButtonBase(kc).Gap(8).AlignItems(ui.Center).Padding(6, 8).Radius(7).Cursor(ui.CursorPointer)
		default:
			e = newBox(kc)
		}
		if n.Wrap {
			e.Wrap()
		}
		if n.ContextMenu != "" && st.ContextMenu != nil {
			st.ContextMenu(c, e, n.ContextMenu)
		}
		if n.OnClick != nil {
			if n.Type == "column" {
				e.Column()
			}
			e.OnClick(func() { r.fire(n, n.OnClick, "click", nil) })
		}
		if n.Type == "row" {
			e.AlignItems(ui.Center)
		}
		e.Gap(n.Gap)
		padding(e, n.Padding)
		if a, ok := aligns[n.Align]; ok {
			e.AlignItems(a)
		}
		if a, ok := aligns[n.Justify]; ok {
			e.Justify(a)
		}
		if n.Grow {
			e.Grow(1).Shrink(1)
		}
		if col, ok := st.color(n.Background); ok {
			e.Background(col).Radius(8)
		}
		if col, ok := st.color(n.Border); ok {
			if n.BorderOpacity != nil {
				col = col.Alpha(float32(*n.BorderOpacity))
			}
			e.Border(1, col)
		}
		if n.Radius != 0 {
			e.Radius(n.Radius)
		}
		if n.Width != 0 {
			e.Width(n.Width)
		}
		if n.Height != 0 {
			e.Height(n.Height)
		}
		if len(n.Corners) == 4 {
			e.Radius(n.Corners[0], n.Corners[1], n.Corners[2], n.Corners[3])
		}
		if n.MaxWidthPercent != 0 {
			e.MaxWidthPercent(float32(n.MaxWidthPercent))
		}
		e.Children(func() { r.children(c, n) })
	case "card":
		if n.Style == "list" {
			ui.Column(kc).Children(func() {
				if st.ListCard == nil {
					ui.Column(c).Radius(12).Border(1, st.Border).Background(st.Surface).Clip().Dividers(1, st.Border).Children(func() { r.children(c, n) })
					return
				}
				st.ListCard(c, n.Title, func() { r.children(c, n) })
			})
			return
		}
		ui.Column(kc).Children(func() {
			st.Card(c, n.Title, func() {
				gap := n.Gap
				if gap == 0 {
					gap = 12
				}
				pad := n.Padding
				if len(pad) == 0 {
					pad = widget.Spacing{16}
				}
				box := ui.Column(c).Gap(gap)
				padding(box, pad)
				box.Children(func() { r.children(c, n) })
			})
		})
	case "text":
		t := ui.Text(kc, n.Text)
		switch n.Style {
		case "muted":
			t.TextColor(st.Muted)
		case "strong":
			t.FontWeight(600)
		case "heading":
			t.FontSize(16).FontWeight(650)
		case "title":
			t.FontSize(22).FontWeight(650)
		case "code":
			t.Font("monospace").FontSize(12)
		case "error":
			t.TextColor(st.Red)
		case "success":
			t.TextColor(st.Green)
		}
		if n.Size == "small" {
			t.FontSize(11.5)
		}
		if n.Italic {
			t.Italic()
		}
		if n.SingleLine {
			t.SingleLine().Shrink(1)
		}
		if n.FontSize != 0 {
			t.FontSize(float32(n.FontSize))
		}
		if n.Weight != 0 {
			t.FontWeight(n.Weight)
		}
		if n.Grow {
			t.Grow(1)
		}
		padding(t, n.Padding)
		if n.Selectable {
			t.Selectable()
		}
		if col, ok := st.color(n.Color); ok {
			t.TextColor(col)
		}
	case "markdown":
		ui.Column(kc).LineHeight(1.55).Children(func() { markdown.View(c, n.Text, r.md) })
	case "badge":
		if n.Style == "count" {
			ui.Text(kc, n.Text).FontSize(11).TextColor(st.Muted).Padding(0, 6).Radius(999).Background(st.Hover)
			return
		}
		col := map[string]ui.Color{"accent": st.Accent, "success": st.Green, "warning": st.Amber, "danger": st.Red}[n.Style]
		if col == (ui.Color{}) {
			col = st.Muted
		}
		if c, ok := st.color(n.Color); ok {
			col = c
		}
		ui.Text(kc, n.Text).FontSize(11).FontWeight(600).TextColor(col).Padding(1, 6).Radius(5).Background(col.Alpha(0.14))
	case "divider":
		if n.Style == "vertical" {
			ui.Box(kc).Width(1).AlignSelf(ui.Stretch).Background(st.Border)
			break
		}
		ui.Box(kc).Height(1).Background(st.Border)
	case "spacer":
		ui.Spacer(kc)
	case "spinner":
		ui.Spinner(kc)
	case "button", "menu":
		if n.Style == "ghost" || n.Style == "outline" || n.Style == "bordered" {
			r.drawnButton(c, n, key(n, i))
			return
		}
	case "tabs":
		if n.Style == "segmented" {
			r.segmented(c, kc, n)
			return
		}
	case "item":
		r.item(c, n, key(n, i))
		return
	case "native":
		e := NativeBox(c, n, i)
		if n.Grow {
			e.Grow(1).Shrink(1).Basis(0)
		}
		e.Children(func() {
			if st.Native == nil {
				ui.Text(c, n.Name+" draws only in a component of Glitched's.").TextColor(st.Muted).Margin(ui.Auto)
				return
			}
			st.Native(c, n, r.thread, func(value any) { r.fire(n, n.OnChange, "change", value) })
		})
		return
	}
	switch n.Type {
	case "button":
		newButton := ui.Button
		if n.Style == "primary" {
			newButton = ui.PrimaryButton
		}
		// A progress button runs till the plugin answers its click: a
		// spinner before it, and it can't be pressed again.
		busy := r.cs.pending[n.ID] > 0
		if busy {
			ui.Spinner(c.Key(key(n, i) + "#busy"))
			// The spinner took the button's key: built unkeyed, the
			// button would be another, and lose the focus.
			kc = c.Key(key(n, i))
		}
		b := newButton(kc, n.Label)
		if n.Style == "danger" {
			b.TextColor(st.Red)
		}
		b.Disabled(n.Disabled || busy).OnClick(func() {
			if !n.Disabled && !busy {
				r.fire(n, n.OnClick, "click", nil)
			}
		})
		r.tip(c, b, n.Tooltip)
	case "input":
		ctl := r.cs.m[n.ID]
		if ctl == nil {
			return
		}
		draw := func() {
			newInput := ui.TextInput
			switch {
			case n.Multiline:
				newInput = ui.TextArea
			case n.Style == "search":
				newInput = ui.SearchField
			}
			e := newInput(c.Key(key(n, i)+"#input"), &ctl.text)
			e.Placeholder(n.Placeholder).Disabled(n.Disabled).Label(n.Label)
			if n.Style == "bare" {
				e.AutoFocus().Grow(1).Background(ui.Transparent).Border(0, ui.Transparent).FocusRing(false).Padding(0).FontSize(15)
			}
			l := r.inputs[n.ID]
			switch {
			case !e.Changed():
			case l != nil && r.waits(l):
				// The plugin is to take the query it had as the keys
				// were pressed, then this; meanwhile the text is the
				// user's, whatever value a view gives it.
				ctl.last = math.MaxInt
				r.press(l, r.cs.m[l.ID], press{input: n.ID, text: ctl.text})
			default:
				r.fire(n, n.OnChange, "change", ctl.text)
			}
			if l != nil {
				r.drive(c, e, l)
			} else if !n.Multiline && e.Submitted() {
				r.fire(n, n.OnSubmit, "submit", ctl.text)
			}
		}
		if n.Label != "" {
			ui.Field(kc, n.Label, draw)
		} else {
			draw()
		}
	case "toggle":
		ctl := r.cs.m[n.ID]
		if ctl == nil {
			return
		}
		if n.Style == "switch" {
			if ui.Switch(kc, &ctl.on).Label(n.Label).Disabled(n.Disabled).Changed() {
				r.fire(n, n.OnChange, "change", ctl.on)
			}
			return
		}
		if ui.Checkbox(kc, &ctl.on, n.Label).Disabled(n.Disabled).Changed() {
			r.fire(n, n.OnChange, "change", ctl.on)
		}
	case "tile":
		var svg *ui.SVG
		if st.Icon != nil {
			svg = st.Icon(n.Icon)
		}
		ui.Box(kc).Children(func() {
			if st.Tile != nil {
				st.Tile(c, svg, ui.Hex(n.Color), n.Width)
				return
			}
			size := n.Width
			ui.Box(c).Size(size, size).Radius(size * 0.28).Center().Background(ui.Hex(n.Color)).Children(func() {
				if svg != nil {
					ui.Icon(c, svg).FontSize(size * 0.6).TextColor(ui.Hex("#ffffff"))
				}
			})
		})
	case "swatch":
		color := ui.Hex(n.Color)
		if st.Swatch == nil {
			if ui.ButtonBase(kc).Size(24, 24).Radius(12).Background(color).Label(n.Label).Clicked() {
				r.fire(n, n.OnClick, "click", nil)
			}
			return
		}
		if st.Swatch(kc, n.ID, color, n.Active, n.Label) {
			r.fire(n, n.OnClick, "click", nil)
		}
	case "select":
		ctl := r.cs.m[n.ID]
		if ctl == nil {
			return
		}
		shown, labels := "", make([]string, len(n.Options))
		for j, o := range n.Options {
			labels[j] = o.Shown()
			if o.Value == ctl.text {
				shown = labels[j]
			}
		}
		draw := func() {
			e := ui.Select(c.Key(key(n, i)+"#select"), &shown, labels).Disabled(n.Disabled).Label(n.Label)
			if n.Width > 0 {
				e.Width(n.Width)
			}
			if e.Changed() {
				for _, o := range n.Options {
					if o.Shown() == shown {
						ctl.text = o.Value
					}
				}
				r.fire(n, n.OnChange, "change", ctl.text)
			}
		}
		if n.Label != "" {
			ui.Field(kc, n.Label, draw)
		} else {
			draw()
		}
	case "tabs":
		ctl := r.cs.m[n.ID]
		if ctl == nil {
			return
		}
		labels := make([]string, len(n.Options))
		at := 0
		for j, o := range n.Options {
			labels[j] = o.Shown()
			if o.Value == ctl.text {
				at = j
			}
		}
		if n.Style == "control" {
			if ui.Segmented(kc, &at, labels...).Label(n.Label).Disabled(n.Disabled).Changed() {
				ctl.text = n.Options[at].Value
				r.fire(n, n.OnChange, "change", ctl.text)
			}
			return
		}
		if ui.Tabs(kc, &at, labels...).Disabled(n.Disabled).Changed() {
			ctl.text = n.Options[at].Value
			r.fire(n, n.OnChange, "change", ctl.text)
		}
	case "menu":
		r.tip(c, ui.Button(kc, n.Label).Disabled(n.Disabled).Menu(r.menu(n)), n.Tooltip)
	case "dialog":
		open := true
		if st.Modal != nil {
			st.Modal()
		}
		d := ui.Modal(c.Key(st.Scope+"#dialog:"+n.ID), &open, func() {
			ui.Column(c).Padding(20).Gap(12).MinWidth(320).MaxWidth(560).Children(func() {
				ui.Text(c, n.Title).FontSize(16).FontWeight(650)
				r.children(c, n)
			})
		})
		if d.Dismissed() {
			if r.cs.dismissed == nil {
				r.cs.dismissed = map[string]bool{}
			}
			r.cs.dismissed[n.ID] = true
			r.fire(n, n.OnClose, "close", nil)
		}
	case "table":
		ui.Column(kc).Gap(0).Children(func() {
			cell := func(col widget.Column, text string, head bool) {
				t := ui.Text(c, text).SingleLine().Shrink(1).Padding(6, 8)
				if col.Width > 0 {
					t.Width(col.Width)
				} else {
					t.Grow(1).Basis(0)
				}
				if head {
					t.FontWeight(600).TextColor(st.Muted).FontSize(11.5)
				}
			}
			ui.Row(c).Key("head").BorderWidth(0, 0, 1, 0).BorderColor(st.Border).Children(func() {
				for _, col := range n.Columns {
					cell(col, col.Title, true)
				}
			})
			for _, row := range n.Rows {
				ui.Row(c).Key("row:"+row.Key).BorderWidth(0, 0, 1, 0).BorderColor(st.Border.Alpha(0.5)).Children(func() {
					for _, col := range n.Columns {
						cell(col, row.Cells[col.ID], false)
					}
				})
			}
		})
	case "image":
		var src ui.ImageSource
		if st.Image != nil {
			src = st.Image(n.Src)
		}
		w, h := n.Width, n.Height
		if w == 0 {
			w = 64
		}
		if h == 0 {
			h = w
		}
		if src == nil {
			ui.Box(kc).Size(w, h).Radius(n.Radius).Background(st.Border.Alpha(0.4))
			return
		}
		ui.Image(kc, src).Size(w, h).Radius(n.Radius)
	case "icon":
		var svg *ui.SVG
		if st.Icon != nil {
			svg = st.Icon(n.Name)
		}
		if svg == nil {
			ui.Box(kc).Size(14, 14)
			return
		}
		e := ui.Icon(kc, svg)
		if n.FontSize != 0 {
			e.FontSize(float32(n.FontSize))
		}
		if col, ok := st.color(n.Color); ok {
			e.TextColor(col)
		}
	case "keys":
		if st.Keys != nil {
			st.Keys(kc, n.Keys)
			return
		}
		ui.Text(kc, strings.Join(n.Keys, " ")).TextColor(st.Muted)
	case "link":
		ui.Link(kc, n.Text, n.Href)
	}
}

// padding gives e the widget's padding, if it has one.
func padding(e ui.Element, s widget.Spacing) {
	if len(s) == 0 {
		return
	}
	t, r, b, l := s.Sides()
	e.Padding(t, r, b, l)
}

// segmented is tabs drawn as the app's segmented switch: an option it
// can't take is dimmed, and its box says why.
func (r *renderer) segmented(c *ui.Context, kc *ui.Context, n *widget.Node) {
	ctl := r.cs.m[n.ID]
	if ctl == nil {
		return
	}
	st := r.st
	ui.Row(kc).Radius(7).Border(1, st.Border).Padding(2).Gap(2).Children(func() {
		for _, o := range n.Options {
			box := ui.Box(c).Key(o.Value + "-box")
			if o.Disabled {
				r.tip(c, box, o.Tooltip)
			}
			box.Children(func() {
				var b ui.Element
				if st.Ghost != nil {
					b = st.Ghost(c, o.Value, ctl.text == o.Value)
				} else {
					b = ui.ButtonBase(c.Key(o.Value)).Radius(6).Cursor(ui.CursorPointer)
				}
				b.Padding(3, 9).Disabled(o.Disabled || n.Disabled).Children(func() { ui.Text(c, o.Shown()).FontSize(11.5) }).OnClick(func() {
					if o.Disabled || n.Disabled {
						return
					}
					ctl.text = o.Value
					r.fire(n, n.OnChange, "change", ctl.text)
				})
			})
		}
	})
}

// item is a list's row: its icon and text, then its trailing widgets, or,
// while it is pointed at, its actions.
func (r *renderer) item(c *ui.Context, n *widget.Node, k string) {
	st := r.st
	var b ui.Element
	if st.Row != nil {
		b = st.Row(c, k, n.Active)
	} else {
		b = ui.ButtonBase(c.Key(k)).Gap(8).AlignItems(ui.Center).Padding(6, 8).Radius(7).Cursor(ui.CursorPointer)
	}
	b.Padding(3, 4, 3, 8).Height(28).Label(cmp.Or(n.Label, n.Text))
	hovered := b.Hovered()
	b.Children(func() {
		if st.Icon != nil {
			if svg := st.Icon(n.Icon); svg != nil {
				ui.Icon(c, svg).FontSize(12)
			}
		}
		t := ui.Text(c, n.Text).FontSize(12)
		if n.Style == "code" {
			t.Font("monospace").TruncateStart()
		}
		t.SingleLine().Grow(1).Shrink(1)
		if !hovered {
			for i, ch := range n.Trailing {
				r.node(c, ch, i)
			}
			return
		}
		ui.Row(c).Gap(0).Children(func() {
			for i, ch := range n.Actions {
				r.node(c, ch, i)
			}
		})
	}).OnClick(func() { r.fire(n, n.OnClick, "click", nil) })
}

// popover draws n's popover under the button b while it's open, till the
// user dismisses it, which tells the plugin.
func (r *renderer) popover(c *ui.Context, b ui.Element, n *widget.Node) {
	open := n.Open && !r.cs.dismissed[n.ID]
	// The panel is as wide as the column's Width says, not the column in it.
	inner := *n.Popover
	inner.Width = 0
	body := func() { r.node(c, &inner, 0) }
	if r.st.Popover != nil {
		r.st.Popover(c, b, &open, float32(n.Popover.Width), body)
	} else {
		ui.PopoverBase(c, b, &open, func(panel ui.Element) {
			panel.AttachTo(b, ui.AnchorBottomStart, ui.AnchorTopStart).Width(float32(n.Popover.Width)).Radius(12).Background(r.st.Surface).Children(body)
		})
	}
	if n.Open && !open && !r.cs.dismissed[n.ID] {
		if r.cs.dismissed == nil {
			r.cs.dismissed = map[string]bool{}
		}
		r.cs.dismissed[n.ID] = true
		r.fire(n, n.OnClose, "close", nil)
	}
}

func (r *renderer) menu(n *widget.Node) func(*ui.Menu) {
	return func(m *ui.Menu) {
		for _, o := range n.Options {
			if o.Separator {
				m.Separator()
				continue
			}
			if m.Item(o.Shown()).Chosen() {
				r.fire(n, n.OnChange, "change", o.Value)
			}
		}
		if r.st.MenuEnd != nil {
			r.st.MenuEnd(m)
		}
	}
}

func (r *renderer) tip(c *ui.Context, e ui.Element, tip string) {
	switch {
	case tip == "":
	case r.st.Tip != nil:
		r.st.Tip(c, e, tip)
	default:
		e.Tooltip(tip)
	}
}

// drawnButton is a ghost or outline button or menu, drawn as the app's
// header draws its own: a ghost one shows only its icon, and is on while
// active; an outline one its icon and label, or its icon alone when
// compact. Its tooltip, when it has one, is also its name to assistive
// tools.
func (r *renderer) drawnButton(c *ui.Context, n *widget.Node, k string) {
	if n.Style == "ghost" {
		// In a box of its own, whether or not it has a badge now, so that
		// one coming keeps the button's state.
		ui.Box(c.Key(k)).Children(func() { r.button(c, n, k) })
		return
	}
	r.button(c, n, k)
}

func (r *renderer) button(c *ui.Context, n *widget.Node, k string) {
	st := r.st
	var b ui.Element
	switch {
	case n.Style == "ghost" && st.Ghost != nil:
		b = st.Ghost(c, k, n.Active).Padding(6)
		if n.Size == "small" {
			b.Padding(3)
		}
		padding(b, n.Padding)
		if col, ok := st.color(n.Color); ok {
			b.TextColor(col)
		} else if n.Color != "" {
			b.TextColor(ui.Hex(n.Color))
		}
	case n.Style == "bordered" && st.Ghost != nil:
		b = st.Ghost(c, k, false).Border(1, st.Border).Padding(6, 12)
	case n.Style == "outline" && st.Outline != nil:
		b = st.Outline(c, k).Shrink(0)
	default:
		b = ui.ButtonBase(c.Key(k)).Gap(6).AlignItems(ui.Center).Padding(5, 11).Radius(999).Cursor(ui.CursorPointer)
	}
	if len(n.Children) > 0 {
		// Its own content: its gap, and its label first.
		if n.Gap != 0 {
			b.Gap(n.Gap)
		}
		b.Label(cmp.Or(n.Label, n.Tooltip))
	} else {
		b.Label(cmp.Or(n.Tooltip, n.Label))
	}
	b.Disabled(n.Disabled)
	if n.Opacity != 0 {
		b.Opacity(float32(n.Opacity))
	}
	if n.ContextMenu != "" && st.ContextMenu != nil {
		st.ContextMenu(c, b, n.ContextMenu)
	}
	r.tip(c, b, n.Tooltip)
	icon := func(name string, size float32) {
		if st.Icon == nil {
			return
		}
		if svg := st.Icon(name); svg != nil {
			ui.Icon(c, svg).FontSize(size)
		}
	}
	ghost := n.Style == "ghost"
	b.Children(func() {
		if len(n.Children) > 0 {
			r.children(c, n)
			return
		}
		if ghost {
			icon(n.Icon, 15)
			return
		}
		if n.Style == "bordered" {
			icon(n.Icon, 13)
			ui.Text(c, n.Label)
			return
		}
		icon(n.Icon, 14)
		if !n.Compact {
			ui.Text(c, n.Label).SingleLine()
		}
		if n.Type == "menu" {
			icon("alt-arrow-down", 11)
		}
	})
	if n.Type == "menu" {
		b.Menu(r.menu(n))
		return
	}
	b.OnClick(func() {
		if !n.Disabled {
			r.fire(n, n.OnClick, "click", nil)
		}
	})
	if n.Popover != nil {
		r.popover(c, b, n)
	}
	if !ghost || n.Badge == "" {
		return
	}
	ui.Text(c, n.Badge).Absolute().Top(1).Right(1).FontSize(9).FontWeight(600).LineHeight(1.4).
		Padding(0, 4).Radius(999).Background(st.Accent).TextColor(ui.Hex("#ffffff")).PassThrough()
}
