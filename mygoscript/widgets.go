package mygoscript

import (
	"fmt"

	"github.com/egoist/mygo/ui"
)

// widget builds one kind of node with MyGo's constructor for it. build
// takes the props it uses from pr and returns the element, for the
// element props, and the children to build in it last, or nil when the
// constructor built them. lead is the prop a leading string argument of
// the kind's Luau constructor sets.
type widget struct {
	lead  string
	build func(d *drawer, c *ui.Context, n node, pr *props, id string) (ui.Element, func())
}

// container is a widget whose children Element.Children builds.
func container(mk func(c *ui.Context) ui.Element) widget {
	return widget{build: func(d *drawer, c *ui.Context, n node, pr *props, id string) (ui.Element, func()) {
		return mk(c), d.kids(c, n, id)
	}}
}

// leaf is a widget made from its props alone; its children, if any, go in
// it as in a container.
func leaf(lead string, mk func(c *ui.Context, pr *props) ui.Element) widget {
	return widget{lead: lead, build: func(d *drawer, c *ui.Context, n node, pr *props, id string) (ui.Element, func()) {
		e := mk(c, pr)
		if len(n.Children) == 0 {
			return e, nil
		}
		return e, d.kids(c, n, id)
	}}
}

// bound is a widget editing one value, bound to state by its bind prop or
// kept by the host under its path, which a change reports to onChange.
func bound[T any](lead string, cd codec[T], mk func(d *drawer, c *ui.Context, pr *props, v *T) ui.Element) widget {
	return widget{lead: lead, build: func(d *drawer, c *ui.Context, n node, pr *props, id string) (ui.Element, func()) {
		b := bind(d, pr, id, "bind", "value", "onChange", cd)
		e := mk(d, c, pr, &b.V)
		b.commit(d)
		if len(n.Children) == 0 {
			return e, nil
		}
		return e, d.kids(c, n, id)
	}}
}

// holder is a widget around children that a callback builds, with an
// open/closed value, as an accordion item's.
func holder(lead string, mk func(d *drawer, c *ui.Context, pr *props, open *bool, kids func()) ui.Element) widget {
	return widget{lead: lead, build: func(d *drawer, c *ui.Context, n node, pr *props, id string) (ui.Element, func()) {
		b := bind(d, pr, id, "bind", "value", "onChange", boolCodec)
		e := mk(d, c, pr, &b.V, d.kids(c, n, id))
		b.commit(d)
		return e, nil
	}}
}

// group is a widget whose constructor builds its children in a callback.
func group(lead string, mk func(c *ui.Context, pr *props, kids func()) ui.Element) widget {
	return widget{lead: lead, build: func(d *drawer, c *ui.Context, n node, pr *props, id string) (ui.Element, func()) {
		return mk(c, pr, d.kids(c, n, id)), nil
	}}
}

func rangeOf(pr *props) (lo, hi float64) {
	return pr.num("min", 0), pr.num("max", 1)
}

// widgets maps every kind a script can build to the MyGo widget that
// draws it. DESIGN.md lists the ui constructors left out and why.
var widgets map[string]widget

func init() {
	widgets = map[string]widget{
		// Layout.
		"box":              container(ui.Box),
		"column":           container(ui.Column),
		"row":              container(ui.Row),
		"grid":             container(ui.Grid),
		"scroll":           container(ui.Scroll),
		"scrollBoth":       container(ui.ScrollBoth),
		"scrollHorizontal": container(ui.ScrollHorizontal),
		"spacer":           leaf("", func(c *ui.Context, pr *props) ui.Element { return ui.Spacer(c) }),
		"divider":          leaf("", func(c *ui.Context, pr *props) ui.Element { return ui.Divider(c) }),
		"overlay": {build: func(d *drawer, c *ui.Context, n node, pr *props, id string) (ui.Element, func()) {
			ui.Overlay(c, d.kids(c, n, id))
			return ui.Element{}, nil
		}},
		"split":                splitWidget(ui.Split),
		"splitPercent":         splitWidget(ui.SplitPercent),
		"splitVertical":        splitWidget(ui.SplitVertical),
		"splitVerticalPercent": splitWidget(ui.SplitVerticalPercent),

		// Text.
		"text": leaf("text", func(c *ui.Context, pr *props) ui.Element { return ui.Text(c, pr.str("text", "")) }),
		"richText": leaf("", func(c *ui.Context, pr *props) ui.Element {
			return ui.RichText(c, spans(pr)...)
		}),
		"link": leaf("label", func(c *ui.Context, pr *props) ui.Element {
			if pr.has("url") {
				pr.fail("url", fmt.Errorf("opening URLs needs a grant scripts do not have; handle onClick"))
			}
			return ui.Link(c, pr.str("label", ""), "")
		}),
		"badge":  leaf("text", func(c *ui.Context, pr *props) ui.Element { return ui.Badge(c, pr.str("text", "")) }),
		"icon":   svgWidget(ui.Icon),
		"image":  svgWidget(func(c *ui.Context, s *ui.SVG) ui.Element { return ui.Image(c, s) }),
		"avatar": leaf("name", func(c *ui.Context, pr *props) ui.Element { return ui.Avatar(c, pr.str("name", ""), nil) }),

		// Indicators.
		"spinner":  leaf("", func(c *ui.Context, pr *props) ui.Element { return ui.Spinner(c) }),
		"progress": leaf("value", func(c *ui.Context, pr *props) ui.Element { return ui.Progress(c, pr.num("value", 0)) }),
		"meter": leaf("value", func(c *ui.Context, pr *props) ui.Element {
			lo, hi := rangeOf(pr)
			v := pr.num("value", 0)
			var levels *ui.MeterLevels
			if pr.has("warning") || pr.has("critical") {
				levels = &ui.MeterLevels{Warning: pr.num("warning", hi), Critical: pr.num("critical", hi)}
			}
			return ui.Meter(c, v, lo, hi, levels)
		}),

		// Buttons and menus.
		"button":        leaf("label", func(c *ui.Context, pr *props) ui.Element { return ui.Button(c, pr.str("label", "")) }),
		"primaryButton": leaf("label", func(c *ui.Context, pr *props) ui.Element { return ui.PrimaryButton(c, pr.str("label", "")) }),
		"buttonBase":    container(ui.ButtonBase),
		"menuButton": {lead: "label", build: func(d *drawer, c *ui.Context, n node, pr *props, id string) (ui.Element, func()) {
			items, _ := object[[]menuItem](pr, "items")
			return ui.MenuButton(c, pr.str("label", ""), d.menu(items)), nil
		}},

		// Text inputs.
		"textInput":           bound("", strCodec, func(d *drawer, c *ui.Context, pr *props, v *string) ui.Element { return ui.TextInput(c, v) }),
		"textInputBase":       bound("", strCodec, func(d *drawer, c *ui.Context, pr *props, v *string) ui.Element { return ui.TextInputBase(c, v) }),
		"textArea":            bound("", strCodec, func(d *drawer, c *ui.Context, pr *props, v *string) ui.Element { return ui.TextArea(c, v) }),
		"textAreaBase":        bound("", strCodec, func(d *drawer, c *ui.Context, pr *props, v *string) ui.Element { return ui.TextAreaBase(c, v) }),
		"textInputBuffer":     bufferWidget(ui.TextInputBuffer),
		"textInputBufferBase": bufferWidget(ui.TextInputBufferBase),
		"textAreaBuffer":      bufferWidget(ui.TextAreaBuffer),
		"textAreaBufferBase":  bufferWidget(ui.TextAreaBufferBase),
		"searchField":         bound("", strCodec, func(d *drawer, c *ui.Context, pr *props, v *string) ui.Element { return ui.SearchField(c, v) }),
		"editableText":        bound("", strCodec, func(d *drawer, c *ui.Context, pr *props, v *string) ui.Element { return ui.EditableText(c, v) }),
		"autocomplete": bound("", strCodec, func(d *drawer, c *ui.Context, pr *props, v *string) ui.Element {
			return ui.Autocomplete(c, v, pr.strs("suggestions"))
		}),
		"tokenField": bound("", strsCodec, func(d *drawer, c *ui.Context, pr *props, v *[]string) ui.Element {
			return ui.TokenField(c, v, pr.strs("suggestions"))
		}),

		// Numbers.
		"slider": bound("", numCodec, func(d *drawer, c *ui.Context, pr *props, v *float64) ui.Element {
			lo, hi := rangeOf(pr)
			return ui.Slider(c, v, lo, hi)
		}),
		"sliderBase": bound("", numCodec, func(d *drawer, c *ui.Context, pr *props, v *float64) ui.Element {
			lo, hi := rangeOf(pr)
			return ui.SliderBase(c, v, lo, hi)
		}),
		"stepSlider": bound("", numCodec, func(d *drawer, c *ui.Context, pr *props, v *float64) ui.Element {
			lo, hi := rangeOf(pr)
			return ui.StepSlider(c, v, lo, hi, pr.num("step", 1))
		}),
		"stepper": bound("", numCodec, func(d *drawer, c *ui.Context, pr *props, v *float64) ui.Element {
			return ui.Stepper(c, v, pr.num("min", 0), pr.num("max", 100), pr.num("step", 1))
		}),
		"numberInput": bound("", numCodec, func(d *drawer, c *ui.Context, pr *props, v *float64) ui.Element {
			return ui.NumberInput(c, v, pr.num("min", 0), pr.num("max", 100), pr.num("step", 1))
		}),
		"rangeSlider": {build: func(d *drawer, c *ui.Context, n node, pr *props, id string) (ui.Element, func()) {
			low := bind(d, pr, id, "bind", "value", "onChange", numCodec)
			high := bind(d, pr, id, "bindHigh", "valueHigh", "onChangeHigh", numCodec)
			lo, hi := rangeOf(pr)
			e := ui.RangeSlider(c, &low.V, &high.V, lo, hi, pr.num("step", 0))
			low.commit(d)
			high.commit(d)
			return e, nil
		}},
		"rating": bound("", intCodec, func(d *drawer, c *ui.Context, pr *props, v *int) ui.Element {
			return ui.Rating(c, v, pr.int("max", 5))
		}),

		// Choices.
		"checkbox": bound("label", boolCodec, func(d *drawer, c *ui.Context, pr *props, v *bool) ui.Element {
			return ui.Checkbox(c, v, pr.str("label", ""))
		}),
		"checkboxBase": bound("", boolCodec, func(d *drawer, c *ui.Context, pr *props, v *bool) ui.Element { return ui.CheckboxBase(c, v) }),
		"checkboxGroup": {lead: "label", build: func(d *drawer, c *ui.Context, n node, pr *props, id string) (ui.Element, func()) {
			outer, held := d.held, []func(){}
			d.held = &held
			e := ui.CheckboxGroup(c, pr.str("label", ""), d.kids(c, n, id))
			d.held = outer
			for _, commit := range held {
				commit()
			}
			return e, nil
		}},
		"switch":     bound("", boolCodec, func(d *drawer, c *ui.Context, pr *props, v *bool) ui.Element { return ui.Switch(c, v) }),
		"switchBase": bound("", boolCodec, func(d *drawer, c *ui.Context, pr *props, v *bool) ui.Element { return ui.SwitchBase(c, v) }),
		"toggle": bound("label", boolCodec, func(d *drawer, c *ui.Context, pr *props, v *bool) ui.Element {
			return ui.Toggle(c, v, pr.str("label", ""))
		}),
		"toggleBase":  bound("", boolCodec, func(d *drawer, c *ui.Context, pr *props, v *bool) ui.Element { return ui.ToggleBase(c, v) }),
		"toggleGroup": group("", func(c *ui.Context, pr *props, kids func()) ui.Element { return ui.ToggleGroup(c, kids) }),
		"radioGroup": {build: func(d *drawer, c *ui.Context, n node, pr *props, id string) (ui.Element, func()) {
			b := bind(d, pr, id, "bind", "value", "onChange", strCodec)
			outer := d.radio
			d.radio = &b.V
			e := ui.RadioGroup(c, d.kids(c, n, id))
			d.radio = outer
			b.commit(d)
			return e, nil
		}},
		"radio": radioWidget(func(c *ui.Context, sel *string, value, label string) ui.Element {
			return ui.Radio(c, sel, value, label)
		}),
		"radioBase": radioWidget(func(c *ui.Context, sel *string, value, label string) ui.Element { return ui.RadioBase(c, sel, value) }),
		"segmented": bound("", intCodec, func(d *drawer, c *ui.Context, pr *props, v *int) ui.Element {
			return ui.Segmented(c, v, pr.strs("labels")...)
		}),
		"segmentedBase": partsWidget(func(c *ui.Context, sel *int, n int) (ui.Element, func(int) ui.Element) {
			p := ui.SegmentedBase(c, sel, n)
			return p.Track, p.Segment
		}),
		"tabs": bound("", intCodec, func(d *drawer, c *ui.Context, pr *props, v *int) ui.Element {
			return ui.Tabs(c, v, pr.strs("labels")...)
		}),
		"tabsBase": partsWidget(func(c *ui.Context, sel *int, n int) (ui.Element, func(int) ui.Element) {
			p := ui.TabsBase(c, sel, n)
			return p.List, p.Tab
		}),
		"select": bound("", strCodec, func(d *drawer, c *ui.Context, pr *props, v *string) ui.Element {
			return ui.Select(c, v, pr.strs("options"))
		}),
		"selectBase": {build: func(d *drawer, c *ui.Context, n node, pr *props, id string) (ui.Element, func()) {
			b := bind(d, pr, id, "bind", "value", "onChange", strCodec)
			trigger, hasTrigger := pr.node("trigger")
			panel, _ := object[map[string]rawJSON](pr, "panel")
			sel := ui.SelectBase(c, &b.V)
			if hasTrigger {
				sel.Trigger.Children(func() { d.draw(c, trigger, id+"/trigger") })
			}
			sel.Popup(func(e ui.Element) {
				d.style(c, e, "selectBase.panel", panel, id)
				d.options(c, n, id, sel.Item)
			})
			b.commit(d)
			return sel.Trigger, nil
		}},
		"combobox": bound("", strCodec, func(d *drawer, c *ui.Context, pr *props, v *string) ui.Element {
			return ui.Combobox(c, v, pr.strs("options"))
		}),
		"comboboxBase": {build: func(d *drawer, c *ui.Context, n node, pr *props, id string) (ui.Element, func()) {
			b := bind(d, pr, id, "bind", "value", "onChange", strCodec)
			onChoose := pr.fn("onChoose")
			panel, _ := object[map[string]rawJSON](pr, "panel")
			cb := ui.ComboboxBase(c, &b.V)
			cb.Popup(func(e ui.Element) {
				d.style(c, e, "comboboxBase.panel", panel, id)
				d.options(c, n, id, cb.Item)
			})
			if v, ok := cb.Chosen(); ok {
				d.queue(onChoose, v)
			}
			b.commit(d)
			return cb.Input, nil
		}},
		"option": {lead: "value", build: func(d *drawer, c *ui.Context, n node, pr *props, id string) (ui.Element, func()) {
			pr.fail("kind", fmt.Errorf("an option goes only in a selectBase or a comboboxBase"))
			return ui.Element{}, nil
		}},
		"breadcrumbs": bound("", intCodec, func(d *drawer, c *ui.Context, pr *props, v *int) ui.Element {
			return ui.Breadcrumbs(c, pr.strs("items"), v)
		}),

		// Colors and dates.
		"colorPicker": bound("", colorCodec, func(d *drawer, c *ui.Context, pr *props, v *ui.Color) ui.Element { return ui.ColorPicker(c, v) }),
		"colorWell":   bound("", colorCodec, func(d *drawer, c *ui.Context, pr *props, v *ui.Color) ui.Element { return ui.ColorWell(c, v) }),
		"calendar":    bound("", timeCodec, func(d *drawer, c *ui.Context, pr *props, v *timeT) ui.Element { return ui.Calendar(c, v) }),
		"dateInput":   bound("", timeCodec, func(d *drawer, c *ui.Context, pr *props, v *timeT) ui.Element { return ui.DateInput(c, v) }),
		"timeInput":   bound("", timeCodec, func(d *drawer, c *ui.Context, pr *props, v *timeT) ui.Element { return ui.TimeInput(c, v) }),

		// Forms.
		"form":  group("", func(c *ui.Context, pr *props, kids func()) ui.Element { return ui.Form(c, kids) }),
		"field": group("label", func(c *ui.Context, pr *props, kids func()) ui.Element { return ui.Field(c, pr.str("label", ""), kids) }),
		"fieldset": group("legend", func(c *ui.Context, pr *props, kids func()) ui.Element {
			return ui.Fieldset(c, pr.str("legend", ""), kids)
		}),

		// Disclosure.
		"accordion": group("", func(c *ui.Context, pr *props, kids func()) ui.Element { return ui.Accordion(c, kids) }),
		"accordionItem": holder("title", func(d *drawer, c *ui.Context, pr *props, open *bool, kids func()) ui.Element {
			return ui.AccordionItem(c, pr.str("title", ""), open, kids)
		}),
		"collapsible": holder("label", func(d *drawer, c *ui.Context, pr *props, open *bool, kids func()) ui.Element {
			return ui.Collapsible(c, pr.str("label", ""), open, kids)
		}),
		"collapsibleBase": {build: func(d *drawer, c *ui.Context, n node, pr *props, id string) (ui.Element, func()) {
			b := bind(d, pr, id, "bind", "value", "onChange", boolCodec)
			trigger, hasTrigger := pr.node("trigger")
			parts := ui.CollapsibleBase(c, &b.V)
			if hasTrigger {
				parts.Trigger.Children(func() { d.draw(c, trigger, id+"/trigger") })
			}
			parts.Panel(d.kids(c, n, id))
			b.commit(d)
			return parts.Trigger, nil
		}},
		"tree": group("", func(c *ui.Context, pr *props, kids func()) ui.Element { return ui.Tree(c, kids) }),
		"treeItem": {lead: "label", build: func(d *drawer, c *ui.Context, n node, pr *props, id string) (ui.Element, func()) {
			b := bind(d, pr, id, "bind", "value", "onChange", boolCodec)
			var e ui.Element
			if len(n.Children) == 0 {
				e = ui.TreeItem(c, pr.str("label", ""), nil, nil)
			} else {
				e = ui.TreeItem(c, pr.str("label", ""), &b.V, d.kids(c, n, id))
			}
			b.commit(d)
			return e, nil
		}},

		// Overlays.
		"modal": holder("", func(d *drawer, c *ui.Context, pr *props, open *bool, kids func()) ui.Element {
			return ui.Modal(c, open, kids)
		}),
		"dialogBase": holder("", func(d *drawer, c *ui.Context, pr *props, open *bool, kids func()) ui.Element {
			backdrop, _ := object[map[string]rawJSON](pr, "backdrop")
			return ui.DialogBase(c, open, func(bd, panel ui.Element) {
				d.style(c, bd, "dialogBase.backdrop", backdrop, "")
				panel.Children(kids)
			})
		}),
		"popover": {build: func(d *drawer, c *ui.Context, n node, pr *props, id string) (ui.Element, func()) {
			b := bind(d, pr, id, "bind", "value", "onChange", boolCodec)
			anchor := d.anchor(c, pr, id)
			e := ui.Popover(c.Key(id), anchor, &b.V, d.kids(c, n, id))
			b.commit(d)
			return e, nil
		}},
		"popoverBase": {build: func(d *drawer, c *ui.Context, n node, pr *props, id string) (ui.Element, func()) {
			b := bind(d, pr, id, "bind", "value", "onChange", boolCodec)
			anchor := d.anchor(c, pr, id)
			e := ui.PopoverBase(c.Key(id), anchor, &b.V, func(panel ui.Element) { panel.Children(d.kids(c, n, id)) })
			b.commit(d)
			return e, nil
		}},
		"tooltipBase": {build: func(d *drawer, c *ui.Context, n node, pr *props, id string) (ui.Element, func()) {
			anchor := d.anchor(c, pr, id)
			return ui.TooltipBase(c.Key(id), anchor, func(tip ui.Element) { tip.Children(d.kids(c, n, id)) }), nil
		}},
		"alertDialog": {lead: "title", build: func(d *drawer, c *ui.Context, n node, pr *props, id string) (ui.Element, func()) {
			b := bind(d, pr, id, "bind", "value", "onChange", boolCodec)
			onChoose := pr.fn("onChoose")
			if i := ui.AlertDialog(c, &b.V, pr.str("title", ""), pr.str("message", ""), pr.strs("buttons")...); i >= 0 {
				d.queue(onChoose, float64(i))
			}
			b.commit(d)
			return ui.Element{}, nil
		}},
		"findBar": {build: func(d *drawer, c *ui.Context, n node, pr *props, id string) (ui.Element, func()) {
			open := bind(d, pr, id, "bindOpen", "open", "onOpenChange", boolCodec)
			query := bind(d, pr, id, "bind", "value", "onChange", strCodec)
			current := bind(d, pr, id, "bindCurrent", "current", "onCurrentChange", intCodec)
			e := ui.FindBar(c, &open.V, &query.V, pr.int("matches", 0), &current.V)
			open.commit(d)
			query.commit(d)
			current.commit(d)
			return e, nil
		}},

		// Navigation.
		"toolbar": group("", func(c *ui.Context, pr *props, kids func()) ui.Element { return ui.Toolbar(c, kids) }),
		"sidebar": {build: func(d *drawer, c *ui.Context, n node, pr *props, id string) (ui.Element, func()) {
			b := bind(d, pr, id, "bind", "value", "onChange", strCodec)
			e := ui.Sidebar(c, &b.V, d.kids(c, n, id))
			b.commit(d)
			return e, nil
		}},
		"sidebarSection": holder("title", func(d *drawer, c *ui.Context, pr *props, open *bool, kids func()) ui.Element {
			return ui.SidebarSection(c, pr.str("title", ""), open, kids)
		}),
		"sidebarItem": {lead: "label", build: func(d *drawer, c *ui.Context, n node, pr *props, id string) (ui.Element, func()) {
			var icon *ui.SVG
			if src := pr.str("icon", ""); src != "" {
				s, err := d.p.svg(src)
				if err != nil {
					pr.fail("icon", err)
				}
				icon = s
			}
			itemID := pr.str("id", n.Key)
			if itemID == "" {
				pr.fail("id", fmt.Errorf("a sidebar item needs an id or a key"))
			}
			e := ui.SidebarItem(c, itemID, icon, pr.str("label", ""))
			if len(n.Children) == 0 {
				return e, nil
			}
			return e, d.kids(c, n, id)
		}},

		// Collections.
		"list":         {build: listWidget},
		"table":        {build: tableWidget},
		"gridView":     {build: gridViewWidget},
		"outline":      {build: outlineWidget},
		"outlineTable": {build: outlineTableWidget},
	}
}
