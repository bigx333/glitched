package mygoscript

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

const svg = `'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><circle cx="12" cy="12" r="10" fill="currentColor"/></svg>'`

// widgetCases build every kind a script can use, inside a column, with the
// props that kind takes.
var widgetCases = map[string]string{
	"box":                  `ui.box({ padding = 8, background = "#ff000080" }, { ui.text("in a box") })`,
	"column":               `ui.column({ gap = 4, alignItems = "center" }, { ui.text("a"), ui.text("b") })`,
	"row":                  `ui.row({ gap = 4, justify = "spaceBetween" }, { ui.text("a"), ui.text("b") })`,
	"grid":                 `ui.grid({ columns = 2, gap = 4, columnTracks = { "1fr", "120" } }, { ui.text("a"), ui.text("b") })`,
	"scroll":               `ui.scroll({ height = 60 }, { ui.text("scrolled") })`,
	"scrollBoth":           `ui.scrollBoth({ height = 60 }, { ui.text("both") })`,
	"scrollHorizontal":     `ui.scrollHorizontal({ ui.text("sideways") })`,
	"spacer":               `ui.row({ ui.text("l"), ui.spacer(), ui.text("r") })`,
	"divider":              `ui.divider()`,
	"overlay":              `ui.overlay({ ui.text("floating", { absolute = true, left = 10, top = 10 }) })`,
	"split":                `ui.split({ value = 120, height = 80 }, { ui.text("left"), ui.text("right") })`,
	"splitPercent":         `ui.splitPercent({ value = 50, height = 80 }, { ui.text("left"), ui.text("right") })`,
	"splitVertical":        `ui.splitVertical({ value = 40, height = 120 }, { ui.text("top"), ui.text("bottom") })`,
	"splitVerticalPercent": `ui.splitVerticalPercent({ value = 50, height = 120 }, { ui.text("top"), ui.text("bottom") })`,
	"text":                 `ui.text("hello", { fontSize = 18, bold = true, textColor = "#336699" })`,
	"richText":             `ui.richText({ spans = { { text = "rich ", weight = 700 }, { text = "text", italic = true, color = "#ff0000" } } })`,
	"link":                 `ui.link("a link", { onClick = function(s) return { clicked = true } end })`,
	"badge":                `ui.badge("12")`,
	"icon":                 `ui.icon(` + svg + `, { fontSize = 20 })`,
	"image":                `ui.image(` + svg + `, { size = { 24, 24 } })`,
	"avatar":               `ui.avatar("Ada Lovelace")`,
	"spinner":              `ui.spinner()`,
	"progress":             `ui.progress(0.4)`,
	"meter":                `ui.meter(0.9, { min = 0, max = 1, warning = 0.8, critical = 0.95, label = "Disk" })`,
	"button":               `ui.button("Press", { tooltip = "a button" })`,
	"primaryButton":        `ui.primaryButton("Go")`,
	"buttonBase":           `ui.buttonBase({ padding = 6, hovered = { background = "#00000010" } }, { ui.text("base button") })`,
	"menuButton":           `ui.menuButton("Sort", { items = { { label = "Name", checked = true, onChoose = function() end }, { separator = true }, { label = "More", items = { { label = "Size" } } } } })`,
	"textInput":            `ui.textInput({ bind = "name", placeholder = "Name" })`,
	"textInputBase":        `ui.textInputBase({ value = "base" })`,
	"textArea":             `ui.textArea({ value = "lines", lines = { 2, 4 } })`,
	"textAreaBase":         `ui.textAreaBase({ value = "lines" })`,
	"textInputBuffer":      `ui.textInputBuffer({ value = "buffer" })`,
	"textInputBufferBase":  `ui.textInputBufferBase({ value = "buffer" })`,
	"textAreaBuffer":       `ui.textAreaBuffer({ value = "buffer" })`,
	"textAreaBufferBase":   `ui.textAreaBufferBase({ value = "buffer" })`,
	"searchField":          `ui.searchField({ value = "query" })`,
	"editableText":         `ui.editableText({ value = "rename me" })`,
	"autocomplete":         `ui.autocomplete({ suggestions = { "apple", "apricot" } })`,
	"tokenField":           `ui.tokenField({ value = { "go", "luau" }, suggestions = { "rust" }, label = "Tags" })`,
	"slider":               `ui.slider({ value = 0.5, min = 0, max = 1, label = "Volume" })`,
	"sliderBase":           `ui.sliderBase({ value = 2, min = 0, max = 10, height = 20 })`,
	"stepSlider":           `ui.stepSlider({ value = 3, min = 0, max = 10, step = 1 })`,
	"stepper":              `ui.stepper({ value = 3, min = 0, max = 10, step = 1 })`,
	"numberInput":          `ui.numberInput({ value = 3, min = 0, max = 10, step = 0.5 })`,
	"rangeSlider":          `ui.rangeSlider({ value = 2, valueHigh = 8, min = 0, max = 10 })`,
	"rating":               `ui.rating({ value = 3, max = 5 })`,
	"checkbox":             `ui.checkbox("Agree", { bind = "agree" })`,
	"checkboxBase":         `ui.checkboxBase({ value = true })`,
	"checkboxGroup":        `ui.checkboxGroup("All", { ui.checkbox("One"), ui.checkbox("Two") })`,
	"switch":               `ui.switch({ value = true, label = "Wi-Fi" })`,
	"switchBase":           `ui.switchBase({ value = false })`,
	"toggle":               `ui.toggle("Bold")`,
	"toggleBase":           `ui.toggleBase({ ui.text("B") })`,
	"toggleGroup":          `ui.toggleGroup({ ui.toggle("B"), ui.toggle("I") })`,
	"radioGroup":           `ui.radioGroup({ value = "m", label = "Size" }, { ui.radio("Small", { value = "s" }), ui.radio("Medium", { value = "m" }) })`,
	"radio":                `ui.radioGroup({ ui.radio("Only") })`,
	"radioBase":            `ui.radioGroup({ ui.radioBase({ value = "x" }, { ui.text("custom radio") }) })`,
	"segmented":            `ui.segmented({ labels = { "Day", "Week" }, value = 1 })`,
	"segmentedBase":        `ui.segmentedBase({ ui.text("One"), ui.text("Two") })`,
	"tabs":                 `ui.tabs({ labels = { "General", "Advanced" } })`,
	"tabsBase":             `ui.tabsBase({ gap = 4 }, { ui.text("Tab A"), ui.text("Tab B") })`,
	"select":               `ui.select({ options = { "S", "M", "L" }, value = "M" })`,
	"selectBase":           `ui.selectBase({ value = "a", trigger = ui.text("choose"), panel = { padding = 4 } }, { ui.option("a", { ui.text("A") }), ui.option("b", { ui.text("B") }) })`,
	"combobox":             `ui.combobox({ options = { "Arial", "Helvetica" }, value = "Arial" })`,
	"comboboxBase":         `ui.comboboxBase({ value = "Ar", onChoose = function() end }, { ui.option("Arial", { ui.text("Arial") }) })`,
	"option":               `ui.selectBase({ trigger = ui.text("pick") }, { ui.option("x", { ui.text("X") }) })`,
	"breadcrumbs":          `ui.breadcrumbs({ items = { "Home", "Docs", "Page" }, value = 2 })`,
	"colorPicker":          `ui.colorPicker({ value = "#ff8800ff" })`,
	"colorWell":            `ui.colorWell({ value = "#00ff00" })`,
	"calendar":             `ui.calendar({ value = "2026-11-05" })`,
	"dateInput":            `ui.dateInput({ value = "2026-11-05" })`,
	"timeInput":            `ui.timeInput({ value = "2026-11-05T09:30:00Z" })`,
	"form":                 `ui.form({ ui.field("Name", { ui.textInput() }) })`,
	"field":                `ui.field("Email", { description = "For receipts.", ui.textInput() })`,
	"fieldset":             `ui.fieldset("Shipping", { ui.textInput() })`,
	"accordion":            `ui.accordion({ ui.accordionItem("First", { value = true }, { ui.text("first body") }) })`,
	"accordionItem":        `ui.accordion({ ui.accordionItem("Second", { ui.text("second body") }) })`,
	"collapsible":          `ui.collapsible("More", { value = true }, { ui.text("more body") })`,
	"collapsibleBase":      `ui.collapsibleBase({ value = true, trigger = ui.text("toggle") }, { ui.text("panel") })`,
	"tree":                 `ui.tree({ ui.treeItem("src", { value = true }, { ui.treeItem("main.go") }) })`,
	"treeItem":             `ui.tree({ ui.treeItem("go.mod") })`,
	"modal":                `ui.modal({ value = true }, { ui.text("in a modal") })`,
	"dialogBase":           `ui.dialogBase({ value = true, backdrop = { background = "#00000040" }, padding = 12 }, { ui.text("in a dialog") })`,
	"popover":              `ui.popover({ value = true, anchor = ui.button("anchor") }, { ui.text("in a popover") })`,
	"popoverBase":          `ui.popoverBase({ value = true, anchor = ui.button("anchor") }, { ui.text("in a base popover") })`,
	"tooltipBase":          `ui.tooltipBase({ anchor = ui.button("hover me") }, { ui.text("tip") })`,
	"alertDialog":          `ui.alertDialog("Delete?", { message = "No undo.", buttons = { "Cancel", "Delete" }, value = true, onChoose = function(s, i) return { chose = i } end })`,
	"findBar":              `ui.findBar({ open = true, value = "needle", matches = 3, current = 1 })`,
	"toolbar":              `ui.toolbar({ label = "Format" }, { ui.button("Share"), ui.spacer(), ui.button("More") })`,
	"sidebar":              `ui.sidebar({ value = "inbox", width = 200 }, { ui.sidebarSection("Mail", { value = true }, { ui.sidebarItem("Inbox", { id = "inbox" }, { ui.badge("3") }), ui.sidebarItem("Sent", { id = "sent", icon = ` + svg + ` }) }) })`,
	"sidebarSection":       `ui.sidebar({ ui.sidebarSection("Tags", { ui.sidebarItem("Red", { key = "red" }) }) })`,
	"sidebarItem":          `ui.sidebar({ ui.sidebarItem("Only", { id = "only" }) })`,
	"list":                 `ui.list({ height = 80, value = 0 }, { ui.text("row 1"), ui.text("row 2") })`,
	"table":                `ui.table({ height = 100, columns = { { title = "Name" }, { title = "Size", width = 60, align = "end" } }, rows = { { ui.text("a.go"), ui.text("1 KB") } } })`,
	"gridView":             `ui.gridView({ height = 100, minWidth = 60, itemHeight = 40 }, { ui.text("cell 1"), ui.text("cell 2") })`,
	"outline":              `ui.outline({ height = 100, items = { { id = "src", label = "src", children = { { id = "main", row = ui.text("main.go") } } } } })`,
	"outlineTable":         `ui.outlineTable({ height = 100, columns = { { title = "Name" }, { title = "Kind" } }, items = { { id = "a", cells = { ui.text("a"), ui.text("file") } } } })`,
}

func script(view string) string {
	return `return { state = { name = "", agree = false }, view = function(state) return ui.column({ ` + view + ` }) end }`
}

// drawn shows view in a tester and returns the plugin, failing on any
// script error or widget fault.
func drawn(t *testing.T, name, view string) (*Plugin, *ui.Tester) {
	t.Helper()
	p := New(name, script(view))
	t.Cleanup(p.Close)
	tt := ui.NewTester(func(c *ui.Context) { View(c, p) }, 900, 700)
	tt.Frame()
	p.mu.Lock()
	err, faults := p.err, p.faults
	p.mu.Unlock()
	if err != nil {
		t.Fatalf("script error: %v", err)
	}
	if len(faults) > 0 {
		t.Fatalf("widget faults: %q", faults)
	}
	return p, tt
}

func TestEveryWidgetBuilds(t *testing.T) {
	for kind := range widgets {
		if _, ok := widgetCases[kind]; !ok {
			t.Errorf("no test builds ui.%s", kind)
		}
	}
	for kind, view := range widgetCases {
		t.Run(kind, func(t *testing.T) {
			if _, ok := widgets[kind]; !ok {
				t.Fatalf("ui.%s is not a widget", kind)
			}
			drawn(t, kind, view)
		})
	}
}

func TestWidgetTextsShow(t *testing.T) {
	for kind, want := range map[string]string{
		"text": "hello", "button": "Press", "badge": "12", "checkbox": "Agree",
		"collapsible": "more body", "modal": "in a modal", "list": "row 2",
		"table": "a.go", "split": "right", "selectBase": "choose", "tree": "main.go",
	} {
		_, tt := drawn(t, kind, widgetCases[kind])
		if !tt.HasText(want) {
			t.Errorf("%s: %q not shown; texts %q", kind, want, tt.Texts())
		}
	}
}

func TestBadWidgetsFaultInPlace(t *testing.T) {
	for name, c := range map[string]struct{ view, want string }{
		"unknown prop":  {`ui.text("x", { colour = "#fff" })`, "unknown prop colour"},
		"bad color":     {`ui.text("x", { background = "red" })`, "not a #rrggbb"},
		"bad align":     {`ui.row({ alignItems = "middle" })`, "not one of start"},
		"url":           {`ui.link("x", { url = "https://example.com" })`, "needs a grant"},
		"stray radio":   {`ui.radio("x")`, "only in a radioGroup"},
		"split of one":  {`ui.split({ ui.text("x") })`, "takes 2 children"},
		"bad svg":       {`ui.icon("<nope")`, "svg"},
		"one size":      {`ui.text("x", { size = 28 })`, "want { a, b }"},
		"bound to type": {`ui.checkbox("x", { bind = "name" })`, "cannot show"},
	} {
		t.Run(name, func(t *testing.T) {
			p := New(name, script(c.view))
			defer p.Close()
			tt := ui.NewTester(func(c *ui.Context) { View(c, p) }, 600, 400)
			tt.Frame()
			p.mu.Lock()
			faults, err := strings.Join(p.faults, "\n"), p.err
			p.mu.Unlock()
			if err != nil || !strings.Contains(faults, c.want) {
				t.Fatalf("faults %q, error %v; want %q", faults, err, c.want)
			}
			if !tt.HasText("x") && name == "unknown prop" {
				t.Errorf("the widget beside its fault is gone: %q", tt.Texts())
			}
		})
	}
	for name, view := range map[string]string{
		"unknown widget": `ui.iframe({})`,
		"stray child":    `ui.column({ 42 })`,
	} {
		t.Run(name, func(t *testing.T) {
			p := New(name, script(view))
			defer p.Close()
			tt := ui.NewTester(func(c *ui.Context) { View(c, p) }, 600, 400)
			tt.Frame()
			shows(t, tt, "Script error in "+name)
		})
	}
}

func TestBoundWidgetsEditHostState(t *testing.T) {
	src := `return {
		state = { name = "", agree = false, volume = 0.5, typed = 0 },
		view = function(state)
			return ui.column({
				ui.textInput({ key = "name", bind = "name", label = "Name", onChange = function(s, v) return { typed = s.typed + 1 } end }),
				ui.checkbox("Agree", { bind = "agree" }),
				ui.slider({ key = "volume", bind = "volume", min = 0, max = 1, step = 0.1, label = "Volume" }),
				ui.text("name=" .. state.name .. " agree=" .. tostring(state.agree) .. " volume=" .. state.volume),
			})
		end,
	}`
	p := New("bound", src)
	defer p.Close()
	tt := ui.NewTester(func(c *ui.Context) { View(c, p) }, 600, 400)
	tt.Frame()

	if err := tt.Click("Name"); err != nil {
		t.Fatal(err)
	}
	tt.Type("Ada")
	tt.Frame()
	if p.Value("name") != "Ada" {
		t.Fatalf("name %v", p.Value("name"))
	}
	if typed, _ := p.Value("typed").(float64); typed < 1 {
		t.Fatalf("onChange ran %v times", p.Value("typed"))
	}

	if err := tt.Click("Agree"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if p.Value("agree") != true {
		t.Fatalf("agree %v", p.Value("agree"))
	}

	if err := tt.Click("Volume"); err != nil {
		t.Fatal(err)
	}
	tt.Key(0, ui.KeyEnd)
	tt.Frame()
	if p.Value("volume") != 1.0 {
		t.Fatalf("volume %v", p.Value("volume"))
	}
	tt.Frame()
	shows(t, tt, "name=Ada agree=true volume=1")

	// The values are the host's: a restarted VM draws the same ones.
	p.reload()
	tt.Frame()
	shows(t, tt, "name=Ada agree=true volume=1")
}

func TestUnboundWidgetsKeepTheirValue(t *testing.T) {
	p, tt := drawn(t, "local", `ui.checkbox("Remember", { key = "remember" })`)
	if err := tt.Click("Remember"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	p.reload()
	tt.Frame()
	p.mu.Lock()
	v := p.local["local/remember@bind"]
	p.mu.Unlock()
	if v != true {
		t.Fatalf("unbound checkbox lost its value: %v (%v)", v, p.local)
	}
}

func TestHandlersAreFunctions(t *testing.T) {
	src := `return {
		state = { picked = "" },
		view = function(state)
			return ui.column({
				ui.radioGroup({ bind = "size", label = "Size", onChange = function(s, v) return { picked = "size " .. v } end }, {
					ui.radio("Small", { value = "s" }), ui.radio("Large", { value = "l" }),
				}),
				ui.text("picked: " .. state.picked),
			})
		end,
	}`
	p := New("handlers", src)
	defer p.Close()
	tt := ui.NewTester(func(c *ui.Context) { View(c, p) }, 600, 400)
	tt.Frame()
	if err := tt.Click("Large"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	if p.Value("size") != "l" {
		t.Fatalf("size %v", p.Value("size"))
	}
	shows(t, tt, "picked: size l")
}
