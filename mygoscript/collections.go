package mygoscript

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"
)

type (
	rawJSON = json.RawMessage
	timeT   = time.Time
)

// selectedCodec is a list's or a grid's chosen row, -1 for none.
var selectedCodec = codec[int]{from: intCodec.from, to: intCodec.to, def: func() int { return -1 }}

func splitWidget(mk func(c *ui.Context, size *float32, first, second func()) ui.Element) widget {
	return widget{build: func(d *drawer, c *ui.Context, n node, pr *props, id string) (ui.Element, func()) {
		if len(n.Children) != 2 {
			pr.fail("children", fmt.Errorf("a split takes 2 children, not %d", len(n.Children)))
			return ui.Element{}, nil
		}
		b := bind(d, pr, id, "bind", "value", "onChange", f32Codec)
		first, second := n.Children[0], n.Children[1]
		e := mk(c, &b.V, func() { d.draw(c, first, id+"/first") }, func() { d.draw(c, second, id+"/second") })
		b.commit(d)
		return e, nil
	}}
}

func spans(pr *props) []ui.Span {
	list, _ := object[[]struct {
		Text, Font, Color, Background, DecorationColor  string
		Size, DecorationThickness, Radius               float32
		Weight                                          int
		Italic, Underline, WavyUnderline, Strikethrough bool
	}](pr, "spans")
	out := make([]ui.Span, 0, len(list))
	for _, s := range list {
		span := ui.Span{
			Text: s.Text, Font: s.Font, Size: s.Size, Weight: s.Weight, Italic: s.Italic,
			Underline: s.Underline, WavyUnderline: s.WavyUnderline, Strikethrough: s.Strikethrough,
			DecorationThickness: s.DecorationThickness, Radius: s.Radius,
		}
		for _, f := range []struct {
			hex string
			dst *ui.Color
		}{{s.Color, &span.Color}, {s.Background, &span.Background}, {s.DecorationColor, &span.DecorationColor}} {
			if f.hex == "" {
				continue
			}
			c, err := parseColor(f.hex)
			if err != nil {
				pr.fail("spans", err)
			}
			*f.dst = c
		}
		out = append(out, span)
	}
	return out
}

// svgWidget shows the SVG markup of its svg prop, parsed once per text.
func svgWidget(mk func(*ui.Context, *ui.SVG) ui.Element) widget {
	return widget{lead: "svg", build: func(d *drawer, c *ui.Context, n node, pr *props, id string) (ui.Element, func()) {
		s, err := d.p.svg(pr.str("svg", ""))
		if err != nil {
			pr.fail("svg", err)
			return ui.Element{}, nil
		}
		return mk(c, s), nil
	}}
}

// bufferWidget edits a TextBuffer the host keeps for the widget, bound to
// a string as the plain inputs are.
func bufferWidget(mk func(c *ui.Context, b *ui.TextBuffer) ui.Element) widget {
	return widget{build: func(d *drawer, c *ui.Context, n node, pr *props, id string) (ui.Element, func()) {
		b := bind(d, pr, id, "bind", "value", "onChange", strCodec)
		buf := handle(d.p, id, func() *ui.TextBuffer { return ui.NewTextBuffer(b.V) })
		if buf.String() != b.V {
			buf.Set(b.V)
		}
		e := mk(c, buf)
		b.V = buf.String()
		b.commit(d)
		return e, nil
	}}
}

func radioWidget(mk func(c *ui.Context, sel *string, value, label string) ui.Element) widget {
	return widget{lead: "label", build: func(d *drawer, c *ui.Context, n node, pr *props, id string) (ui.Element, func()) {
		label := pr.str("label", "")
		value := pr.str("value", label)
		if d.radio == nil {
			pr.fail("kind", fmt.Errorf("a radio goes only in a radioGroup"))
			return ui.Element{}, nil
		}
		e := mk(c, d.radio, value, label)
		if len(n.Children) == 0 {
			return e, nil
		}
		return e, d.kids(c, n, id)
	}}
}

// partsWidget is a base widget made of a container and one part per child,
// as TabsBase's list and tabs, choosing an index.
func partsWidget(mk func(c *ui.Context, sel *int, n int) (ui.Element, func(int) ui.Element)) widget {
	return widget{build: func(d *drawer, c *ui.Context, n node, pr *props, id string) (ui.Element, func()) {
		b := bind(d, pr, id, "bind", "value", "onChange", intCodec)
		box, part := mk(c, &b.V, len(n.Children))
		return box, func() {
			for i, child := range n.Children {
				part(i).Children(func() { d.draw(c, child, fmt.Sprintf("%s/%d", id, i)) })
			}
			b.commit(d)
		}
	}}
}

// style applies a table of element props to an element a widget hands
// out, as a popup's panel.
func (d *drawer) style(c *ui.Context, e ui.Element, kind string, m map[string]rawJSON, id string) {
	if len(m) == 0 {
		return
	}
	if _, err := applyProps(e, &props{kind: kind, m: m}); err != nil {
		d.fault(c, id+"!"+kind, err)
	}
}

// options builds n's children, which must be options, as item(value).
func (d *drawer) options(c *ui.Context, n node, id string, item func(string) ui.Element) {
	for i, child := range n.Children {
		childID := fmt.Sprintf("%s/option#%d", id, i)
		if child.Kind != "option" {
			d.fault(c, childID, fmt.Errorf("%s holds options, not a %s", n.Kind, child.Kind))
			continue
		}
		opr := newProps(child)
		e := item(opr.str("value", ""))
		e, err := applyProps(e, opr)
		e.Children(d.kids(c, child, childID))
		if err != nil {
			d.fault(c, childID+"!", err)
		}
	}
}

// anchor builds the widget a popover or a tip shows by.
func (d *drawer) anchor(c *ui.Context, pr *props, id string) ui.Element {
	n, ok := pr.node("anchor")
	if !ok {
		pr.fail("anchor", fmt.Errorf("needs a widget to show by"))
		return ui.Element{}
	}
	return d.draw(c, n, id+"/anchor")
}

// rowIDs are the key paths of ns, as children gives them.
func rowIDs(ns []node, id string) []string {
	ids := make([]string, len(ns))
	seen := map[string]bool{}
	for i, child := range ns {
		childID := child.Key
		for n := 0; childID == "" || seen[childID]; n++ {
			childID = fmt.Sprintf("%s#%d.%d", child.Kind, i, n)
		}
		seen[childID] = true
		ids[i] = id + "/" + childID
	}
	return ids
}

func listWidget(d *drawer, c *ui.Context, n node, pr *props, id string) (ui.Element, func()) {
	b := bind(d, pr, id, "bind", "value", "onChange", selectedCodec)
	s := handle(d.p, id, func() *ui.ListState { return &ui.ListState{} })
	ids := rowIDs(n.Children, id)
	s.Selected, s.Key = &b.V, func(i int) any { return ids[i] }
	e := ui.List(c, s, len(n.Children), func(i int) { d.draw(c, n.Children[i], ids[i]) })
	b.commit(d)
	return e, nil
}

type column struct {
	Title, ID                 string
	Width, MinWidth, MaxWidth float32
	Align                     string
}

func columns(pr *props) []ui.TableColumn {
	list, _ := object[[]column](pr, "columns")
	out := make([]ui.TableColumn, len(list))
	for i, col := range list {
		out[i] = ui.TableColumn{Title: col.Title, ID: col.ID, Width: col.Width, MinWidth: col.MinWidth, MaxWidth: col.MaxWidth}
		if col.Align != "" {
			a, ok := aligns[col.Align]
			if !ok {
				pr.fail("columns", fmt.Errorf("%q is not an alignment", col.Align))
			}
			out[i].Align = a
		}
	}
	return out
}

// cell draws the widget at row, col of rows, if there is one.
func (d *drawer) cell(c *ui.Context, rows [][]node, row, col int, id string) {
	if row < len(rows) && col < len(rows[row]) {
		d.draw(c, rows[row][col], fmt.Sprintf("%s/%d/%d", id, row, col))
	}
}

func tableWidget(d *drawer, c *ui.Context, n node, pr *props, id string) (ui.Element, func()) {
	b := bind(d, pr, id, "bind", "value", "onChange", selectedCodec)
	cols := columns(pr)
	rows, _ := object[[][]node](pr, "rows")
	s := handle(d.p, id, func() *ui.ListState { return &ui.ListState{} })
	s.Selected = &b.V
	e := ui.Table(c, s, cols, len(rows), func(row, col int) { d.cell(c, rows, row, col, id) })
	b.commit(d)
	return e, nil
}

func gridViewWidget(d *drawer, c *ui.Context, n node, pr *props, id string) (ui.Element, func()) {
	b := bind(d, pr, id, "bind", "value", "onChange", selectedCodec)
	s := handle(d.p, id, func() *ui.GridState { return &ui.GridState{} })
	ids := rowIDs(n.Children, id)
	s.Selected, s.Key = &b.V, func(i int) any { return ids[i] }
	e := ui.GridView(c, s, len(n.Children), pr.f32("minWidth", 120), pr.f32("itemHeight", 120), func(i int) {
		d.draw(c, n.Children[i], ids[i])
	})
	b.commit(d)
	return e, nil
}

// outlineItem is one item of an outline: its row, or the text of its
// label, or in an outline table its cells, and the items under it.
type outlineItem struct {
	ID       string
	Label    string
	Row      *node
	Cells    []node
	Children []outlineItem
}

// outlineItems indexes the items prop by id and returns the roots.
func outlineItems(pr *props) ([]string, map[string]*outlineItem) {
	list, _ := object[[]outlineItem](pr, "items")
	index := map[string]*outlineItem{}
	var walk func(items []outlineItem) []string
	walk = func(items []outlineItem) []string {
		ids := make([]string, 0, len(items))
		for i := range items {
			it := &items[i]
			if it.ID == "" || index[it.ID] != nil {
				pr.fail("items", fmt.Errorf("every item needs an id of its own; %q", it.ID))
				continue
			}
			index[it.ID] = it
			ids = append(ids, it.ID)
		}
		for _, it := range items {
			walk(it.Children)
		}
		return ids
	}
	return walk(list), index
}

func childIDs(index map[string]*outlineItem) func(string) []string {
	return func(k string) []string {
		it := index[k]
		if it == nil {
			return nil
		}
		ids := make([]string, len(it.Children))
		for i, ch := range it.Children {
			ids[i] = ch.ID
		}
		return ids
	}
}

func outlineWidget(d *drawer, c *ui.Context, n node, pr *props, id string) (ui.Element, func()) {
	roots, index := outlineItems(pr)
	s := handle(d.p, id, func() *ui.OutlineState[string] { return &ui.OutlineState[string]{} })
	e := ui.Outline(c, s, roots, childIDs(index), func(k string) {
		it := index[k]
		if it.Row != nil {
			d.draw(c, *it.Row, id+"/"+k)
			return
		}
		ui.Text(c, it.Label)
	})
	return e, nil
}

func outlineTableWidget(d *drawer, c *ui.Context, n node, pr *props, id string) (ui.Element, func()) {
	roots, index := outlineItems(pr)
	cols := columns(pr)
	s := handle(d.p, id, func() *ui.OutlineState[string] { return &ui.OutlineState[string]{} })
	e := ui.OutlineTable(c, s, cols, roots, childIDs(index), func(k string, col int) {
		it := index[k]
		switch {
		case col < len(it.Cells):
			d.draw(c, it.Cells[col], fmt.Sprintf("%s/%s/%d", id, k, col))
		case col == 0:
			ui.Text(c, it.Label)
		}
	})
	return e, nil
}
