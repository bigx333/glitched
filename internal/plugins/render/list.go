package render

import (
	"github.com/egoist/mygo/ui"

	"glitched/internal/plugins"
	"glitched/internal/plugins/widget"
)

// listView is what a list keeps across frames: its place among its rows,
// and, for the view's items, where each key is and how tall they are.
type listView struct {
	state ui.ListState
	items []widget.ListItem
	index map[string]int
	tall  float32
	// drawn is the build it was last drawn in.
	drawn int
	// pointer is where the pointer was, in the window, as the last build
	// saw it, if one did; moved is whether this build sees it moved over
	// the list. A list that opens or moves under a pointer at rest sees
	// none move.
	pointerX, pointerY float32
	sampled, moved     bool
	// pressed are the keys pressed on the list the plugin hasn't caught
	// up with yet, in order.
	pressed []press
}

// press is a key pressed on a list: a move by d items, or a submit with
// the keys held.
type press struct {
	d      int
	submit bool
	mods   []string
}

// listOf is the list's view of n's items, built again for a new view's.
func (ctl *control) listOf(n *widget.Node) *listView {
	lv := ctl.list
	if lv == nil {
		lv = &listView{}
		ctl.list = lv
	}
	if len(lv.items) == len(n.Items) && (len(n.Items) == 0 || &lv.items[0] == &n.Items[0]) {
		return lv
	}
	lv.items = n.Items
	lv.index = make(map[string]int, len(n.Items))
	t, _, b, _ := n.Padding.Sides()
	lv.tall = float32(t + b + n.Gap*max(len(n.Items)-1, 0))
	for i, it := range n.Items {
		lv.index[it.Key] = i
		lv.tall += float32(n.ItemHeight(i))
	}
	// A choice whose item went is none.
	if i, ok := lv.index[ctl.text]; !ok || n.Items[i].Header {
		ctl.text = ""
	}
	return lv
}

// list draws a list: the rows in view, each as the plugin drew it in the
// app's row, and the choice, which Glitched keeps and the keys move.
func (r *renderer) list(kc *ui.Context, n *widget.Node) {
	ctl := r.cs.m[n.ID]
	if ctl == nil {
		return
	}
	lv := ctl.listOf(n)
	lv.drawn = r.cs.passes
	ls := &lv.state
	ls.Key = func(i int) any { return n.Items[i].Key }
	ls.Label = func(i int) string { return n.Items[i].Label }
	e := ui.List(kc, ls, len(n.Items)).Label(n.Label).Focusable().Gap(float32(n.Gap))
	if n.ChooseOnHover {
		x, y, in := e.PointerPosition()
		at := e.Bounds()
		x, y = x+at.X, y+at.Y
		lv.moved = in && lv.sampled && (x != lv.pointerX || y != lv.pointerY)
		lv.pointerX, lv.pointerY, lv.sampled = x, y, true
	}
	padding(e, n.Padding)
	if n.Grow {
		e.Grow(1).Shrink(1).Basis(0)
	} else {
		e.Height(min(lv.tall, float32(n.Height)))
	}
	r.pressed(n, ctl)
	e.OnShortcut(0, ui.KeyDown, func() { r.press(n, ctl, press{d: 1}) })
	e.OnShortcut(0, ui.KeyUp, func() { r.press(n, ctl, press{d: -1}) })
	e.OnShortcut(0, ui.KeyHome, func() { r.press(n, ctl, press{d: -len(n.Items)}) })
	e.OnShortcut(0, ui.KeyEnd, func() { r.press(n, ctl, press{d: len(n.Items)}) })
	e.OnShortcut(0, ui.KeyEnter, func() { r.press(n, ctl, press{submit: true, mods: heldKeys(kc)}) })
	e.OnShortcut(ui.Cmd, ui.KeyEnter, func() { r.press(n, ctl, press{submit: true, mods: heldKeys(kc)}) })
	if r.st.WaitRows != nil && len(n.Items) > 0 {
		first, _ := ls.Visible()
		first = min(max(first, 0), len(n.Items)-1)
		tall := max(e.Bounds().H, float32(n.Height))
		if tall == 0 {
			tall = maxListView
		}
		var in []widget.ListItem
		for i, y := first, float32(0); i < len(n.Items) && y < tall; i++ {
			in = append(in, n.Items[i])
			y += float32(n.ItemHeight(i) + n.Gap)
		}
		r.st.WaitRows(n.ID, in)
	}
	e.Rows(func(row ui.ListRow) { r.listRow(row.Context, n, ctl, row.Index) })
}

// maxListView is how tall a list that grows is taken to be before it was
// laid out: the rows it waits for as it opens are those that fit.
const maxListView = 2000

func (r *renderer) listRow(c *ui.Context, n *widget.Node, ctl *control, i int) {
	it := n.Items[i]
	var node *widget.Node
	if r.st.Rows != nil {
		node = r.st.Rows(n.ID, it)
	}
	in := *r
	in.row, in.thread = &plugins.RowRef{List: n.ID, Key: it.Key}, it.Thread
	h := float32(n.ItemHeight(i))
	held := func(e ui.Element) bool { return r.st.ListRow != nil && r.st.ListRow(c, n, i, e) }
	if it.Header {
		head := ui.Column(c.Key("header")).Height(h).Justify(ui.Center)
		in.hovered = held(head) || head.Hovered()
		head.Children(func() {
			if node != nil {
				in.node(c, node, 0)
			}
		})
		return
	}
	var b ui.Element
	if r.st.Row != nil {
		b = r.st.Row(c, "row", ctl.text == it.Key)
	} else {
		b = ui.ButtonBase(c.Key("row")).AlignItems(ui.Center).Radius(7).Cursor(ui.CursorPointer)
	}
	b.Height(h).Padding(0).Label(it.Label)
	in.hovered = held(b) || b.Hovered()
	// The pointer moving over a row chooses it; resting there doesn't,
	// though the rows scroll or move under it, so the keys and the
	// plugin's own value move the choice from it.
	if n.ChooseOnHover && ctl.list.moved && b.Hovered() {
		r.choose(n, ctl, it.Key)
	}
	if it.Thread != "" && r.st.ContextMenu != nil {
		r.st.ContextMenu(c, b, "thread:"+it.Thread)
	}
	b.Children(func() {
		if node != nil {
			in.node(c, node, 0)
		}
	}).OnClick(func() {
		r.choose(n, ctl, it.Key)
		if n.SubmitOnClick {
			r.submit(n, it.Key, heldKeys(c))
		}
	})
	if b.DoubleClicked() && !n.SubmitOnClick {
		r.choose(n, ctl, it.Key)
		r.submit(n, it.Key, heldKeys(c))
	}
}

// choose makes key the list's choice, and tells the plugin when it moved.
func (r *renderer) choose(n *widget.Node, ctl *control, key string) {
	if ctl.text == key {
		return
	}
	ctl.text = key
	r.fire(n, n.OnChange, "change", key)
}

// move moves the choice by d items, past headers, as far as the ends; a
// list that wraps around goes on from the other end by one item.
func (r *renderer) move(n *widget.Node, ctl *control, d int) {
	lv := ctl.listOf(n)
	at, chosen := lv.index[ctl.text]
	if !chosen {
		at = -1
		if d < 0 {
			at = len(n.Items)
		}
	}
	step := 1
	if d < 0 {
		step, d = -1, -d
	}
	to := -1
	for i := at + step; i >= 0 && i < len(n.Items) && d > 0; i += step {
		if !n.Items[i].Header {
			to, d = i, d-1
		}
	}
	if to < 0 && n.WrapAround && d == 1 {
		for i := range n.Items {
			j := i
			if step < 0 {
				j = len(n.Items) - 1 - i
			}
			if !n.Items[j].Header {
				to = j
				break
			}
		}
	}
	if to < 0 {
		return
	}
	r.choose(n, ctl, n.Items[to].Key)
	lv.state.ScrollIntoView(to)
}

// press does what a key asks of the list once the plugin took every event
// sent before it: a key pressed as the plugin draws the list a query
// just typed brings moves and submits that list, not the one on screen.
func (r *renderer) press(n *widget.Node, ctl *control, p press) {
	lv := ctl.listOf(n)
	lv.pressed = append(lv.pressed, p)
	r.pressed(n, ctl)
}

// pressed does the keys pressed on the list, in order, while the plugin
// has caught up with what they sent.
func (r *renderer) pressed(n *widget.Node, ctl *control) {
	lv := ctl.listOf(n)
	for len(lv.pressed) > 0 && r.cs.sent <= r.cs.processed {
		p := lv.pressed[0]
		lv.pressed = lv.pressed[1:]
		if p.submit {
			r.submit(n, ctl.text, p.mods)
		} else {
			r.move(n, ctl, p.d)
		}
	}
}

// heldKeys are the keys held, as a submit tells them: cmd is Command on macOS
// and Control elsewhere, where ctrl is never told.
func heldKeys(c *ui.Context) []string {
	var out []string
	mods := c.Modifiers()
	for _, m := range []struct {
		m    ui.Modifiers
		name string
	}{{ui.Cmd, "cmd"}, {ui.Shift, "shift"}, {ui.Alt, "alt"}, {ui.Ctrl &^ ui.Cmd, "ctrl"}} {
		if mods&m.m != 0 {
			out = append(out, m.name)
		}
	}
	return out
}

// submit tells the plugin the choice was submitted, with the keys held.
func (r *renderer) submit(n *widget.Node, key string, mods []string) {
	if i, ok := r.cs.m[n.ID].listOf(n).index[key]; !ok || n.Items[i].Header {
		return
	}
	s := *r
	s.mods = mods
	s.fire(n, n.OnSubmit, "submit", key)
}

// drive lets the input e move the list l with Up and Down, and submit it
// with Enter, as a palette's search field does.
func (r *renderer) drive(c *ui.Context, e ui.Element, l *widget.Node) {
	// Only a list drawn, in this build or the last, as one in a dialog
	// shut is not.
	ctl := r.cs.m[l.ID]
	if ctl == nil || ctl.list == nil || ctl.list.drawn < r.cs.passes-1 {
		return
	}
	e.OnShortcut(0, ui.KeyDown, func() { r.press(l, ctl, press{d: 1}) })
	e.OnShortcut(0, ui.KeyUp, func() { r.press(l, ctl, press{d: -1}) })
	e.OnShortcut(ui.Cmd, ui.KeyEnter, func() { r.press(l, ctl, press{submit: true, mods: heldKeys(c)}) })
	if e.Submitted() {
		r.press(l, ctl, press{submit: true, mods: heldKeys(c)})
	}
}
