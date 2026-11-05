package mygoscript

import (
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"github.com/egoist/mygo/ui"
)

// View draws the plugin's widget tree with native widgets. The first View
// of a plugin starts its VM. A script error shows in place, over the last
// tree the script rendered, with a button that restarts the VM; a widget
// the host cannot draw shows its error where it would be.
func View(c *ui.Context, p *Plugin) ui.Element {
	p.mu.Lock()
	p.services = c.Services()
	if !p.started {
		p.start()
	} else if p.dirty && p.vm != nil {
		p.fail(p.render())
	}
	tree, err := p.tree, p.err
	p.mu.Unlock()

	d := &drawer{p: p}
	e := ui.Column(c).Gap(12).Children(func() {
		if err != nil {
			errorView(c, p, err)
		}
		if tree != nil {
			d.draw(c, *tree, p.name)
		}
	})
	d.flush(c)
	return e
}

// drawer builds one pass of a tree.
type drawer struct {
	p *Plugin
	// events are the handlers to run once the pass is built, with their
	// values: a handler changes the state the rest of the pass reads.
	events []event
	// radio is the choice of the radio group being built.
	radio *string
	// faults are the widget errors the pass showed in place.
	faults []string
	// held, while a checkbox group is built, collects its checkboxes'
	// commits: the group edits their values after they are built.
	held *[]func()
}

type event struct {
	fn    *fnRef
	value any
}

func (d *drawer) queue(f *fnRef, v any) {
	if f != nil {
		d.events = append(d.events, event{f, v})
	}
}

// flush runs the queued handlers and asks for a pass that draws the
// state they and the bound widgets left.
func (d *drawer) flush(c *ui.Context) {
	for _, ev := range d.events {
		d.p.fire(ev.fn, ev.value)
	}
	d.p.mu.Lock()
	d.p.faults = d.faults
	changed := len(d.events) > 0 || d.p.dirty
	d.p.mu.Unlock()
	if changed {
		c.Invalidate()
	}
}

// draw builds n under the key path id, and returns its element. A widget's
// identity is its script key, or its kind and position among its
// siblings, so a rerun of the view keeps MyGo's per-widget state (focus,
// hover, press) and the host's values on the same widgets.
func (d *drawer) draw(c *ui.Context, n node, id string) ui.Element {
	w, ok := widgets[n.Kind]
	if !ok {
		return d.fault(c, id, fmt.Errorf("unknown widget %q", n.Kind))
	}
	pr := newProps(n)
	onClick, onSubmit := pr.fn("onClick"), pr.fn("onSubmit")
	menu, hasMenu := object[[]menuItem](pr, "menu")
	ctxMenu, hasCtxMenu := object[[]menuItem](pr, "contextMenu")
	if pr.err != nil {
		return d.fault(c, id, pr.err)
	}
	e, kids := w.build(d, c.Key(id), n, pr, id)
	if pr.err != nil {
		return d.fault(c, id+"!", pr.err)
	}
	e, err := applyProps(e, pr)
	if onClick != nil {
		e = e.OnClick(func() { d.p.fire(onClick, nil) })
	}
	if onSubmit != nil {
		e = e.OnSubmit(func() { d.p.fire(onSubmit, nil) })
	}
	if hasMenu {
		e = e.Menu(d.menu(menu))
	}
	if hasCtxMenu {
		e = e.ContextMenu(d.menu(ctxMenu))
	}
	if kids != nil {
		e = e.Children(kids)
	}
	if err != nil {
		d.fault(c, id+"!", err)
	}
	return e
}

// children builds ns, each under its own key in id.
func (d *drawer) children(c *ui.Context, ns []node, id string) {
	seen := map[string]bool{}
	for i, child := range ns {
		childID := child.Key
		for n := 0; childID == "" || seen[childID]; n++ {
			childID = fmt.Sprintf("%s#%d.%d", child.Kind, i, n)
		}
		seen[childID] = true
		d.draw(c, child, id+"/"+childID)
	}
}

// kids builds n's children, for a widget that takes them in a callback.
func (d *drawer) kids(c *ui.Context, n node, id string) func() {
	return func() { d.children(c, n.Children, id) }
}

// fault shows err where a widget would be.
func (d *drawer) fault(c *ui.Context, id string, err error) ui.Element {
	d.faults = append(d.faults, err.Error())
	return ui.Text(c.Key(id), err.Error()).TextColor(c.Theme().Danger).FontSize(12).Selectable()
}

type menuItem struct {
	Label     string
	Checked   bool
	Disabled  bool
	Separator bool
	EditItems bool
	OnChoose  *fnRef
	Items     []menuItem
}

func (d *drawer) menu(items []menuItem) func(m *ui.Menu) {
	return func(m *ui.Menu) {
		for _, it := range items {
			switch {
			case it.Separator:
				m.Separator()
			case it.EditItems:
				m.EditItems()
			case it.Items != nil:
				m.Submenu(it.Label, d.menu(it.Items)).Disabled(it.Disabled)
			default:
				if m.Item(it.Label).Checked(it.Checked).Disabled(it.Disabled).Chosen() && it.OnChoose != nil {
					d.p.fire(it.OnChoose, nil)
				}
			}
		}
	}
}

func errorView(c *ui.Context, p *Plugin, err error) {
	th := c.Theme()
	ui.Column(c.Key("mygoscript-error")).Gap(8).Padding(12).Radius(8).Border(1, th.Danger).Children(func() {
		ui.Text(c, "Script error in "+p.name).Bold().TextColor(th.Danger)
		ui.Text(c, err.Error()).Selectable()
		ui.Row(c).Children(func() {
			ui.Button(c, "Restart script").OnClick(p.reload)
		})
	})
}

// cell is a widget's value while its widget is built: V starts as the
// stored value, the widget edits it as its constructor returns, and
// commit stores the edit.
type cell[T any] struct {
	V        T
	start    any
	key, id  string
	onChange *fnRef
	to       func(T) any
}

// commit stores the widget's edit and queues its onChange handler.
func (b *cell[T]) commit(d *drawer) {
	if d.held != nil {
		*d.held = append(*d.held, func() { b.store(d) })
		return
	}
	b.store(d)
}

func (b *cell[T]) store(d *drawer) {
	v := b.to(b.V)
	if reflect.DeepEqual(v, b.start) {
		return
	}
	if d.p.set(b.key, b.id, v) {
		d.queue(b.onChange, v)
	}
}

// codec converts between a widget's Go value and its state value.
type codec[T any] struct {
	from func(any) (T, bool)
	to   func(T) any
	def  func() T
}

// bind reads the value of the widget at id: the state at the key its prop
// bindProp names, or else its own, which starts as its prop valueProp.
// onChange names the prop of the handler a change queues.
func bind[T any](d *drawer, pr *props, id, bindProp, valueProp, onChange string, cd codec[T]) *cell[T] {
	key := pr.str(bindProp, "")
	b := &cell[T]{key: key, id: id + "@" + bindProp, to: cd.to}
	if onChange != "" {
		b.onChange = pr.fn(onChange)
	}
	var initial any
	hasInitial := false
	if valueProp != "" {
		if raw, ok := pr.take(valueProp); ok {
			hasInitial = json.Unmarshal(raw, &initial) == nil
		}
	}
	v, ok := d.p.get(key, b.id)
	seeding := !ok && key == "" && hasInitial
	if seeding {
		v, ok = initial, true
	}
	b.V = cd.def()
	if ok {
		if t, good := cd.from(v); good {
			b.V = t
		} else if key != "" {
			pr.fail(bindProp, fmt.Errorf("state %q holds %v, which this widget cannot show", key, v))
		} else {
			pr.fail(valueProp, fmt.Errorf("%v does not suit this widget", v))
		}
	}
	b.start = cd.to(b.V)
	// An unbound widget's seed is its value from the first pass on, as an
	// edit would be, so a later seed does not reset it.
	if seeding {
		d.p.set("", b.id, b.start)
	}
	return b
}

var (
	strCodec = codec[string]{
		from: func(v any) (string, bool) { s, ok := v.(string); return s, ok },
		to:   func(s string) any { return s },
		def:  func() string { return "" },
	}
	boolCodec = codec[bool]{
		from: func(v any) (bool, bool) { b, ok := v.(bool); return b, ok },
		to:   func(b bool) any { return b },
		def:  func() bool { return false },
	}
	numCodec = codec[float64]{
		from: func(v any) (float64, bool) { f, ok := v.(float64); return f, ok },
		to:   func(f float64) any { return f },
		def:  func() float64 { return 0 },
	}
	intCodec = codec[int]{
		from: func(v any) (int, bool) { f, ok := v.(float64); return int(f), ok },
		to:   func(i int) any { return float64(i) },
		def:  func() int { return 0 },
	}
	f32Codec = codec[float32]{
		from: func(v any) (float32, bool) { f, ok := v.(float64); return float32(f), ok },
		to:   func(f float32) any { return float64(f) },
		def:  func() float32 { return 0 },
	}
	colorCodec = codec[ui.Color]{
		from: func(v any) (ui.Color, bool) {
			s, _ := v.(string)
			c, err := parseColor(s)
			return c, err == nil
		},
		to:  func(c ui.Color) any { return formatColor(c) },
		def: func() ui.Color { return ui.Color{R: 0x3b, G: 0x82, B: 0xf6, A: 0xff} },
	}
	timeCodec = codec[time.Time]{
		from: func(v any) (time.Time, bool) {
			s, _ := v.(string)
			t, err := parseTime(s)
			return t, err == nil
		},
		to:  func(t time.Time) any { return t.Format(time.RFC3339) },
		def: func() time.Time { return time.Now().Truncate(time.Minute) },
	}
	strsCodec = codec[[]string]{
		from: func(v any) ([]string, bool) {
			list, ok := v.([]any)
			if !ok {
				return nil, false
			}
			out := make([]string, 0, len(list))
			for _, s := range list {
				str, ok := s.(string)
				if !ok {
					return nil, false
				}
				out = append(out, str)
			}
			return out, true
		},
		to: func(s []string) any {
			out := make([]any, len(s))
			for i, v := range s {
				out[i] = v
			}
			return out
		},
		def: func() []string { return nil },
	}
)
