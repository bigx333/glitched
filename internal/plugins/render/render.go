// Package render draws a plugin's widget tree with MyGo's components, and
// turns what the user does to it into events for the plugin's update.
package render

import (
	"cmp"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"

	"glitched/internal/plugins"
	"glitched/internal/plugins/widget"
)

// Style is the look the app gives the widgets.
type Style struct {
	Text, Muted, Faint, Accent, Red, Green, Amber, Border, Surface ui.Color
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
	cs.rev = rev
	live := map[string]bool{}
	v.Body.Walk(func(n *widget.Node) {
		if n.Type == "dialog" {
			delete(cs.dismissed, n.ID)
		}
		if n.Type != "input" && n.Type != "toggle" && n.Type != "select" && n.Type != "tabs" {
			return
		}
		live[n.ID] = true
		ctl := cs.m[n.ID]
		raw := canon(n.Value)
		if ctl == nil {
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
	r := renderer{cs: cs, st: st, md: md, dispatch: dispatch}
	r.node(c, v.Body, 0)
}

type renderer struct {
	cs       *Controls
	st       Style
	md       *markdown.Options
	dispatch func(plugins.UIEvent, map[string]any) int
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
	seq := r.dispatch(plugins.UIEvent{Type: typ, Handler: h.Name, Node: n.ID, Value: value, Payload: h.Payload}, r.cs.Values())
	if ctl := r.cs.m[n.ID]; ctl != nil && typ == "change" && seq > 0 {
		ctl.last = seq
	}
}

func (r *renderer) node(c *ui.Context, n *widget.Node, i int) {
	st := r.st
	// Stateful widgets take their key before they're built: a key set
	// after is a panic.
	kc := c.Key(key(n, i))
	switch n.Type {
	case "column", "row":
		newBox := ui.Column
		if n.Type == "row" {
			newBox = ui.Row
		}
		e := newBox(kc)
		if n.Type == "row" {
			e.AlignItems(ui.Center)
		}
		e.Gap(float32(n.Gap)).Padding(float32(n.Padding))
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
		e.Children(func() { r.children(c, n) })
	case "card":
		ui.Column(kc).Children(func() {
			st.Card(c, n.Title, func() {
				gap := float32(n.Gap)
				if gap == 0 {
					gap = 12
				}
				pad := float32(n.Padding)
				if pad == 0 {
					pad = 16
				}
				ui.Column(c).Padding(pad).Gap(gap).Children(func() { r.children(c, n) })
			})
		})
	case "text":
		t := ui.Text(kc, n.Text)
		switch n.Style {
		case "muted":
			t.TextColor(st.Muted)
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
		if n.Selectable {
			t.Selectable()
		}
		if col, ok := st.color(n.Color); ok {
			t.TextColor(col)
		}
	case "markdown":
		ui.Column(kc).LineHeight(1.55).Children(func() { markdown.View(c, n.Text, r.md) })
	case "badge":
		col := map[string]ui.Color{"accent": st.Accent, "success": st.Green, "warning": st.Amber, "danger": st.Red}[n.Style]
		if col == (ui.Color{}) {
			col = st.Muted
		}
		if c, ok := st.color(n.Color); ok {
			col = c
		}
		ui.Text(kc, n.Text).FontSize(11).FontWeight(600).TextColor(col).Padding(1, 6).Radius(5).Background(col.Alpha(0.14))
	case "divider":
		ui.Box(kc).Height(1).Background(st.Border)
	case "spacer":
		ui.Spacer(kc)
	case "spinner":
		ui.Spinner(kc)
	case "button", "menu":
		if n.Style == "ghost" || n.Style == "outline" {
			r.drawnButton(c, n, key(n, i))
			return
		}
	}
	switch n.Type {
	case "button":
		newButton := ui.Button
		if n.Style == "primary" {
			newButton = ui.PrimaryButton
		}
		b := newButton(kc, n.Label)
		if n.Style == "danger" {
			b.TextColor(st.Red)
		}
		b.Disabled(n.Disabled).OnClick(func() {
			if !n.Disabled {
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
			if n.Multiline {
				newInput = ui.TextArea
			}
			e := newInput(c.Key(key(n, i)+"#input"), &ctl.text)
			e.Placeholder(n.Placeholder).Disabled(n.Disabled).Label(n.Label)
			if e.Changed() {
				r.fire(n, n.OnChange, "change", ctl.text)
			}
			if !n.Multiline && e.Submitted() {
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
		if ui.Checkbox(kc, &ctl.on, n.Label).Disabled(n.Disabled).Changed() {
			r.fire(n, n.OnChange, "change", ctl.on)
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
		if ui.Tabs(kc, &at, labels...).Disabled(n.Disabled).Changed() {
			ctl.text = n.Options[at].Value
			r.fire(n, n.OnChange, "change", ctl.text)
		}
	case "menu":
		r.tip(c, ui.Button(kc, n.Label).Disabled(n.Disabled).Menu(r.menu(n)), n.Tooltip)
	case "dialog":
		open := n.Open && !r.cs.dismissed[n.ID]
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
					t.Width(float32(col.Width))
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
		w, h := float32(n.Width), float32(n.Height)
		if w == 0 {
			w = 64
		}
		if h == 0 {
			h = w
		}
		if src == nil {
			ui.Box(kc).Size(w, h).Radius(float32(n.Radius)).Background(st.Border.Alpha(0.4))
			return
		}
		ui.Image(kc, src).Size(w, h).Radius(float32(n.Radius))
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
		if col, ok := st.color(n.Color); ok {
			e.TextColor(col)
		}
	}
}

func (r *renderer) menu(n *widget.Node) func(*ui.Menu) {
	return func(m *ui.Menu) {
		for _, o := range n.Options {
			if m.Item(o.Shown()).Chosen() {
				r.fire(n, n.OnChange, "change", o.Value)
			}
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
	case n.Style == "outline" && st.Outline != nil:
		b = st.Outline(c, k).Shrink(0)
	default:
		b = ui.ButtonBase(c.Key(k)).Gap(6).AlignItems(ui.Center).Padding(5, 11).Radius(999).Cursor(ui.CursorPointer)
	}
	b.Label(cmp.Or(n.Tooltip, n.Label)).Disabled(n.Disabled)
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
		if ghost {
			icon(n.Icon, 15)
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
	if !ghost || n.Badge == "" {
		return
	}
	ui.Text(c, n.Badge).Absolute().Top(1).Right(1).FontSize(9).FontWeight(600).LineHeight(1.4).
		Padding(0, 4).Radius(999).Background(st.Accent).TextColor(ui.Hex("#ffffff")).PassThrough()
}
