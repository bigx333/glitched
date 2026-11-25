package render

import (
	"math"
	"slices"
	"sync"

	"github.com/egoist/mygo/ui"

	"glitched/internal/plugins"
	"glitched/internal/plugins/widget"
)

// listView is what a list keeps across frames: its place among its rows,
// and, for the view's items, where each key is and how tall they are.
type listView struct {
	state *ui.ListState
	items []widget.ListItem
	index map[string]int
	tall  float32
	// sections are the sections the items name, each once, and sec each
	// item's among them, -1 for none, nil while none names one; open is
	// how far open each is as the build folds it, -1 for one not folding.
	sections []string
	sec      []int
	open     []float32
	// While a section folds, vis are the items the list's rows show, in
	// order, and vh how tall each row shows; row is each item's row, -1
	// for one covered, and shown the list's height as its rows show.
	// With nothing folding, vis is nil, each item is its row, and shown
	// is tall.
	vis   []int
	vh    []float32
	row   []int
	shown float32
	// under is the room under each row the curtain uncovered of the
	// row after it, no more than its gap: the row after joins the list,
	// gap and all, once the curtain passes its gap.
	under []float32
	// caps are how much of each section folding, by name, its curtain
	// uncovers open: the most the box showed of it under its top in the
	// builds since it began to fold. ends are where the rows built in the
	// last build ended, by key, in the window, as laid out before it, and
	// top and box where the list began and how tall it was; built are
	// this build's.
	caps        map[string]float32
	ends, built map[string]float32
	top, box    float32
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
// the keys held; or a change of the input that drives it, typed behind
// such a key, or a row the pointer moved over then, which wait behind it
// too.
type press struct {
	d      int
	submit bool
	mods   []string
	input  string
	text   string
	choose string
}

// spare are the states of lists gone, to give lists to come. A list's
// state binds a handle in the window, which MyGo keeps till the window
// closes: a list that comes and goes, as a palette's or the sidebar's,
// takes one gone rather than binding another each time.
var spare struct {
	sync.Mutex
	lists []*ui.ListState
}

func spareList() *ui.ListState {
	spare.Lock()
	defer spare.Unlock()
	n := len(spare.lists)
	if n == 0 {
		return &ui.ListState{}
	}
	s := spare.lists[n-1]
	spare.lists = spare.lists[:n-1]
	*s = ui.ListState{Handle: s.Handle}
	return s
}

// release gives the control's list state, if it has one, to a list to
// come.
func (ctl *control) release() {
	if ctl.list == nil {
		return
	}
	spare.Lock()
	spare.lists = append(spare.lists, ctl.list.state)
	spare.Unlock()
	ctl.list = nil
}

// Release gives the lists of a view no longer drawn to lists to come.
func (cs *Controls) Release() {
	for _, ctl := range cs.m {
		ctl.release()
	}
}

// toEnd is a move as far as the list goes, however long it is by then.
const toEnd = 1 << 30

// listOf is the list's view of n's items, built again for a new view's.
func (ctl *control) listOf(n *widget.Node) *listView {
	lv := ctl.list
	if lv == nil {
		lv = &listView{state: spareList()}
		ctl.list = lv
	}
	if len(lv.items) == len(n.Items) && (len(n.Items) == 0 || &lv.items[0] == &n.Items[0]) {
		return lv
	}
	lv.items = n.Items
	lv.index = make(map[string]int, len(n.Items))
	t, _, b, _ := n.Padding.Sides()
	lv.tall = t + b + n.Gap*float32(max(len(n.Items)-1, 0))
	lv.sections, lv.sec = nil, nil
	for i, it := range n.Items {
		lv.index[it.Key] = i
		lv.tall += n.ItemHeight(i)
		if it.Section == "" {
			continue
		}
		if lv.sec == nil {
			lv.sec = slices.Repeat([]int{-1}, len(n.Items))
		}
		k := slices.Index(lv.sections, it.Section)
		if k < 0 {
			k = len(lv.sections)
			lv.sections = append(lv.sections, it.Section)
		}
		lv.sec[i] = k
	}
	lv.vis, lv.vh, lv.row, lv.shown = nil, nil, nil, lv.tall
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
	// Keys pressed on a list that went, as one in a dialog shut, went
	// with it: they don't act on what it shows when it comes back.
	if lv.drawn < r.cs.passes-1 {
		lv.pressed = nil
	}
	lv.drawn = r.cs.passes
	if r.st.Fold != nil {
		if lv.built == nil {
			lv.ends, lv.built = map[string]float32{}, map[string]float32{}
		}
		lv.ends, lv.built = lv.built, lv.ends
		clear(lv.built)
	}
	lv.fold(n, r.st.Fold)
	ls := lv.state
	vis, row := lv.vis, lv.row
	ls.Key = func(j int) any { return n.Items[item(vis, j)].Key }
	ls.Label = func(j int) string {
		// A section's first row, covered, is nothing to assistive tools.
		if i := item(vis, j); vis == nil || row[i] >= 0 {
			return n.Items[i].Label
		}
		return ""
	}
	if r.st.Reveal != nil {
		if i := slices.IndexFunc(n.Items, func(it widget.ListItem) bool { return r.st.Reveal(n, it) }); i >= 0 {
			if j := lv.rowOf(i); j >= 0 {
				ls.ScrollIntoView(j)
			}
		}
	}
	e := ui.List(kc, ls, lv.count(n)).Label(n.Label).Gap(n.Gap)
	if n.ChooseOnHover {
		x, y, in := e.PointerPosition()
		at := e.Bounds()
		x, y = x+at.X, y+at.Y
		lv.moved = in && lv.sampled && (x != lv.pointerX || y != lv.pointerY)
		lv.pointerX, lv.pointerY, lv.sampled = x, y, true
	}
	at := e.Bounds()
	lv.top, lv.box = at.Y, at.H
	padding(e, n.Padding)
	if n.Grow {
		e.Grow(1).Shrink(1).Basis(0)
	} else {
		e.Height(min(lv.shown, n.Height))
	}
	r.pressed(n, ctl)
	if !n.NoKeys {
		e.Focusable()
		e.OnShortcut(0, ui.KeyDown, func() { r.press(n, ctl, press{d: 1}) })
		e.OnShortcut(0, ui.KeyUp, func() { r.press(n, ctl, press{d: -1}) })
		e.OnShortcut(0, ui.KeyHome, func() { r.press(n, ctl, press{d: -toEnd}) })
		e.OnShortcut(0, ui.KeyEnd, func() { r.press(n, ctl, press{d: toEnd}) })
		e.OnShortcut(0, ui.KeyEnter, func() { r.press(n, ctl, press{submit: true, mods: heldKeys(kc)}) })
		e.OnShortcut(ui.Cmd, ui.KeyEnter, func() { r.press(n, ctl, press{submit: true, mods: heldKeys(kc)}) })
		// The row chosen, which had the keys, goes as a curtain covers
		// it: the list keeps them.
		if i, ok := lv.index[ctl.text]; ok && lv.rowOf(i) < 0 && e.FocusWithin() {
			e.Focus(ui.FocusOptions{PreventScroll: true})
		}
	}
	if r.st.WaitRows != nil && len(n.Items) > 0 {
		// From the item the first row in view shows, at their full
		// heights: the rows a section is about to uncover draw before
		// they show.
		first, _ := ls.Visible()
		first = item(vis, min(max(first, 0), max(lv.count(n)-1, 0)))
		tall := max(e.Bounds().H, n.Height)
		if tall == 0 {
			tall = maxListView
		}
		var in []widget.ListItem
		for i, y := first, float32(0); i < len(n.Items) && y < tall; i++ {
			in = append(in, n.Items[i])
			y += n.ItemHeight(i) + n.Gap
		}
		r.st.WaitRows(n.ID, in)
	}
	e.Rows(func(row ui.ListRow) { r.listRow(row.Context, n, ctl, row.Index) })
}

// item is the item the list's row j shows, as vis maps them.
func item(vis []int, j int) int {
	if vis == nil {
		return j
	}
	return vis[j]
}

// rowOf is the list's row of item i, -1 while a section covers it.
func (lv *listView) rowOf(i int) int {
	if lv.vis == nil {
		return i
	}
	return lv.row[i]
}

// count is how many rows the list has.
func (lv *listView) count(n *widget.Node) int {
	if lv.vis == nil {
		return len(n.Items)
	}
	return len(lv.vis)
}

// fold maps the list's rows to its items as f says how far open each
// section they name is. A folding section's curtain uncovers, from its
// top, p of its rows' height, gaps and all, or of what the box showed of
// them under its top as it began to fold, if less: a row shows what of
// its item it uncovered, less its gap, and one it covers has no row, but
// the section's first, which shows at no height so that its gap keeps
// the header above where it is with the section shut. Nothing folding,
// each item is its row.
func (lv *listView) fold(n *widget.Node, f func(*widget.Node, string) (float32, bool)) {
	lv.vis, lv.vh, lv.row, lv.under, lv.shown = nil, nil, nil, nil, lv.tall
	if f == nil || len(lv.sections) == 0 {
		clear(lv.caps)
		return
	}
	lv.open = lv.open[:0]
	folding := false
	for _, s := range lv.sections {
		p, ok := f(n, s)
		if ok {
			p, folding = min(max(p, 0), 1), true
		} else {
			p = -1
		}
		lv.open = append(lv.open, p)
	}
	for s := range lv.caps {
		if k := slices.Index(lv.sections, s); k < 0 || lv.open[k] < 0 {
			delete(lv.caps, s)
		}
	}
	if !folding {
		return
	}
	gap := n.Gap
	// The folding sections' rows, gaps and all.
	full := make([]float32, len(lv.sections))
	for i, k := range lv.sec {
		if k >= 0 && lv.open[k] >= 0 {
			full[k] += n.ItemHeight(i) + gap
		}
	}
	if lv.caps == nil {
		lv.caps = map[string]float32{}
	}
	reach := make([]float32, len(lv.sections))
	for k, t := range full {
		if lv.open[k] < 0 {
			continue
		}
		// The most the box showed of it as it folded: the curtain never
		// ends short of where the section rests, and only goes on.
		c, had := lv.caps[lv.sections[k]]
		if m, ok := lv.capOf(n, k); ok || !had {
			c = max(c, m)
		}
		lv.caps[lv.sections[k]] = c
		reach[k] = lv.open[k] * min(c, t)
	}
	at := make([]float32, len(lv.sections))
	seen := make([]bool, len(lv.sections))
	lv.vis = make([]int, 0, len(n.Items))
	lv.vh = make([]float32, 0, len(n.Items))
	lv.under = make([]float32, 0, len(n.Items))
	lv.row = make([]int, len(n.Items))
	t, _, b, _ := n.Padding.Sides()
	lv.shown = t + b - gap
	for i, k := range lv.sec {
		h := n.ItemHeight(i)
		if k >= 0 && lv.open[k] >= 0 {
			v := min(max(reach[k]-at[k], 0), h+gap)
			at[k] += h + gap
			first := !seen[k]
			seen[k] = true
			switch {
			case v <= 0 && first:
				lv.row[i] = -1
				lv.vis = append(lv.vis, i)
				lv.vh = append(lv.vh, 0)
				lv.under = append(lv.under, 0)
				lv.shown += gap
				continue
			case v <= 0:
				lv.row[i] = -1
				continue
			case v <= gap && !first && len(lv.vis) > 0:
				// Uncovered no further than its gap, the row shows as room
				// under the row before, as fast as the curtain goes.
				lv.row[i] = -1
				lv.under[len(lv.under)-1] += v
				lv.shown += v
				continue
			}
			h = max(v-gap, 0)
		}
		lv.row[i] = len(lv.vis)
		lv.vis = append(lv.vis, i)
		lv.vh = append(lv.vh, h)
		lv.under = append(lv.under, 0)
		lv.shown += h + gap
	}
	if len(lv.vis) == 0 {
		lv.shown = t + b
	}
}

// capOf is how much of section k the box shows under the section's top,
// gaps and all, as the last build laid the list out, its padding too, as
// rows show through it: no less than the section's first row. The box is
// the list's height, or, for one that grows, how tall it was laid out.
// Where the section begins isn't known when the item before it wasn't
// drawn, nor the box of a list that grows before it was laid out: ok is
// false, and the cap is the box, or all of it.
func (lv *listView) capOf(n *widget.Node, k int) (c float32, ok bool) {
	box := n.Height
	if n.Grow || box <= 0 {
		if box = lv.box; box <= 0 {
			return math.MaxFloat32, false
		}
	}
	first := slices.Index(lv.sec, k)
	pt, _, _, _ := n.Padding.Sides()
	// Where the curtain starts, under the list's top: as a row's gap
	// comes with it, the end of the row before, or a gap above the
	// first.
	start := pt - n.Gap
	if first > 0 {
		end, ok := lv.ends[n.Items[first-1].Key]
		if !ok {
			return box, false
		}
		// The row before scrolled above the box, the box shows the
		// section from its top: a fold starts at its header, which the
		// click on it keeps in view.
		start = max(end-lv.top, -n.Gap)
	}
	return max(box-start, n.ItemHeight(first)+n.Gap), true
}

// maxListView is how tall a list that grows is taken to be before it was
// laid out: the rows it waits for as it opens are those that fit.
const maxListView = 2000

// listRow draws the list's row j. The row of an item in a section is as
// tall as it shows, clipped while that is less than the item, whose
// widgets draw in a box of its full height within: the row shows what
// the curtain uncovered, from the top, rather than all of it squashed.
func (r *renderer) listRow(c *ui.Context, n *widget.Node, ctl *control, j int) {
	lv := ctl.list
	i := item(lv.vis, j)
	it := n.Items[i]
	h := n.ItemHeight(i)
	show := h
	if lv.vis != nil {
		show = lv.vh[j]
	}
	var node *widget.Node
	if r.st.Rows != nil && show > 0 {
		node = r.st.Rows(n.ID, it)
	}
	in := *r
	in.row, in.thread = &plugins.RowRef{List: n.ID, Key: it.Key}, it.Thread
	held := func(e ui.Element) bool { return r.st.ListRow != nil && r.st.ListRow(c, n, i, e) }
	// fold makes e as tall as the item shows, clipped while that's less
	// than its height.
	fold := func(e ui.Element) {
		if lv.built != nil {
			if at := e.Bounds(); at.W > 0 {
				lv.built[it.Key] = at.Y + at.H
			}
		}
		if lv.under != nil && lv.under[j] > 0 {
			e.Margin(0, 0, lv.under[j], 0)
		}
		e.Height(show)
		if show < h {
			e.ClipY()
		}
		if show <= 0 {
			// Nothing of it shows: nothing to the pointer, the keys or
			// assistive tools.
			e.Invisible()
		}
	}
	// widgets draws the row's widgets in e: for an item in a section, in
	// the box of the item's height, which lay lays out as e does.
	widgets := func(e ui.Element, lay func(ui.Element) ui.Element) ui.Element {
		return e.Children(func() {
			if it.Section == "" {
				if node != nil {
					in.node(c, node, 0)
				}
				return
			}
			lay(ui.Box(c.Key("fold"))).Height(h).Shrink(0).Children(func() {
				if node != nil {
					in.node(c, node, 0)
				}
			})
		})
	}
	if it.Header {
		head := ui.Column(c.Key("header"))
		if it.Section == "" {
			head.Justify(ui.Center)
		}
		fold(head)
		in.hovered = held(head) || head.Hovered()
		widgets(head, func(e ui.Element) ui.Element { return e.Justify(ui.Center) })
		return
	}
	var b ui.Element
	if r.st.Row != nil {
		b = r.st.Row(c, "row", ctl.text == it.Key)
	} else {
		b = ui.ButtonBase(c.Key("row")).AlignItems(ui.Center).Radius(7).Cursor(ui.CursorPointer)
	}
	fold(b)
	b.Padding(0)
	if it.Label != "" {
		b.Label(it.Label)
	}
	in.hovered = held(b) || b.Hovered()
	// The pointer moving over a row chooses it; resting there doesn't,
	// though the rows scroll or move under it, so the keys and the
	// plugin's own value move the choice from it.
	if n.ChooseOnHover && ctl.list.moved && b.Hovered() {
		if lv := ctl.list; len(lv.pressed) > 0 {
			if last := lv.pressed[len(lv.pressed)-1]; last.choose != it.Key {
				lv.pressed = append(lv.pressed, press{choose: it.Key})
			}
		} else {
			r.choose(n, ctl, it.Key)
		}
	}
	if it.Thread != "" && r.st.ContextMenu != nil {
		r.st.ContextMenu(c, b, "thread:"+it.Thread)
	}
	widgets(b, func(e ui.Element) ui.Element {
		// As the row lays out its widgets: the app's from the start,
		// MyGo's button centered.
		justify := ui.Center
		if r.st.Row != nil {
			justify = ui.Start
		}
		return e.Row().AlignItems(ui.Center).Justify(justify).Grow(1).AlignSelf(ui.Start)
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
	// A header is never chosen, nor an item a section covers.
	takes := func(i int) bool { return !n.Items[i].Header && lv.rowOf(i) >= 0 }
	for i := at + step; i >= 0 && i < len(n.Items) && d > 0; i += step {
		if takes(i) {
			to, d = i, d-1
		}
	}
	if to < 0 && n.WrapAround && d == 1 {
		for i := range n.Items {
			j := i
			if step < 0 {
				j = len(n.Items) - 1 - i
			}
			if takes(j) {
				to = j
				break
			}
		}
	}
	if to < 0 {
		return
	}
	r.choose(n, ctl, n.Items[to].Key)
	lv.state.ScrollIntoView(lv.rowOf(to))
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
		switch {
		case p.input != "":
			// An input the view no longer has took nothing.
			if in := r.fields[p.input]; in != nil {
				r.fire(in, in.OnChange, "change", p.text)
			}
			// What no handler took, the input no longer holds against
			// the plugin's value, once nothing else typed in it waits.
			if c := r.cs.m[p.input]; c != nil && c.last == math.MaxInt && !slices.ContainsFunc(lv.pressed, func(q press) bool { return q.input == p.input }) {
				c.last = 0
			}
		case p.submit:
			r.submit(n, ctl.text, p.mods)
		case p.choose != "":
			r.choose(n, ctl, p.choose)
		default:
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

// waits says keys pressed on the list l wait for the plugin, so that what
// its input types now waits behind them.
func (r *renderer) waits(l *widget.Node) bool {
	ctl := r.cs.m[l.ID]
	return ctl != nil && ctl.list != nil && len(ctl.list.pressed) > 0
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
