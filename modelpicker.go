package main

import (
	"math"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/plugins"
	"glitched/internal/plugins/widget"
)

// modelInfo is a model as the picker shows it: key is what names it, and
// the rest only what the picker shows of it, efforts the levels it runs
// at where its engine says.
type modelInfo struct {
	key               modelKey
	name, short, desc string
	color             ui.Color
	icon              *ui.SVG
	efforts           []effortInfo
	// cliDefault is that its provider's program runs it where it is
	// given no model, and defaultEffort the effort it runs it at where
	// it is given none, as its engine reported (provider.Model).
	cliDefault    bool
	defaultEffort string
}

// family is how the picker shows the models of a provider, which names
// it as a thread's api.ModelRef does.
type family struct {
	provider   string
	name, desc string
	color      ui.Color
	icon       *ui.SVG
}

var families = []family{
	{"claude", "Claude", "Anthropic", ui.Hex("#d97757"), icClaude},
	{"codex", "Codex", "OpenAI", ui.Hex("#10a37f"), icOpenAI},
	{"gemini", "Gemini", "Google", ui.Hex("#4f7cf7"), icGemini},
	{"antigravity", "Antigravity", "Google", ui.Hex("#1a73e8"), icGemini},
	{"grok", "Grok", "xAI", ui.Hex("#2c2c31"), icGrok},
}

// familyOf is the family of provider, or one shown by its name alone for a
// provider Glitched has none for.
func familyOf(provider string) family {
	if i := slices.IndexFunc(families, func(f family) bool { return f.provider == provider }); i >= 0 {
		return families[i]
	}
	return family{provider: provider, name: provider, color: ui.Hex("#6b7280"), icon: icSparkles}
}

// knownModels are how the picker shows the models Glitched knows, by the
// id their engine reports. Which models it offers is the engines' to say
// (modelLists): these only until a family's engine first answers.
var knownModels = []modelInfo{
	{key: modelKey{"claude", "claude-opus-5-5"}, name: "Claude Opus 5.5", short: "Opus 5.5", desc: "Most capable, for complex work", color: ui.Hex("#d97757"), icon: icClaude},
	{key: modelKey{"claude", "claude-sonnet-5-5"}, name: "Claude Sonnet 5.5", short: "Sonnet 5.5", desc: "Smart and fast, for every day", color: ui.Hex("#d97757"), icon: icClaude},
	{key: modelKey{"claude", "claude-haiku-5-5"}, name: "Claude Haiku 5.5", short: "Haiku 5.5", desc: "Fastest, for quick edits", color: ui.Hex("#d97757"), icon: icClaude},
	{key: modelKey{"codex", "gpt-5.5-codex"}, name: "GPT-5.5 Codex", short: "GPT-5.5 Codex", desc: "Tuned for agentic coding", color: ui.Hex("#10a37f"), icon: icOpenAI},
	{key: modelKey{"codex", "gpt-5.5"}, name: "GPT-5.5", short: "GPT-5.5", desc: "General reasoning", color: ui.Hex("#10a37f"), icon: icOpenAI},
	{key: modelKey{"gemini", "gemini-3-pro"}, name: "Gemini 3 Pro", short: "Gemini 3 Pro", desc: "Long context, sees images", color: ui.Hex("#4f7cf7"), icon: icGemini},
	{key: modelKey{"gemini", "gemini-3-flash"}, name: "Gemini 3 Flash", short: "Gemini 3 Flash", desc: "Fast and light", color: ui.Hex("#4f7cf7"), icon: icGemini},
	{key: modelKey{"grok", "grok-4"}, name: "Grok 4", short: "Grok 4", desc: "Reasoning, with what's happening now", color: ui.Hex("#2c2c31"), icon: icGrok},
	{key: modelKey{"grok", "grok-code-fast"}, name: "Grok Code Fast", short: "Grok Code Fast", desc: "Quick agentic coding", color: ui.Hex("#2c2c31"), icon: icGrok},
}

// goneModel is what a model's row says of it while its engine no longer
// offers it: a thread, or the default, set to it keeps it till another is
// chosen, and its engine runs it or says why not.
const goneModel = "No longer offered"

// pickerRows are the rows of family f's models: what its engine offers,
// and the model chosen, of f, where the engine doesn't offer it: no
// longer, once it answered, or not yet, till it does; or the program's
// own default, which a workspace's settings name and it doesn't list.
func (a *app) pickerRows(f family) []modelInfo {
	rows := a.familyModels(f)
	m, offered := a.modelOf(a.model)
	if !offered && m.key.Provider == f.provider {
		if _, answered := a.models.reported[f.provider]; answered && !a.followsCLI {
			m.desc = goneModel
		}
		if slices.ContainsFunc(a.offeredModels(), func(o modelInfo) bool { return o.name == m.name }) {
			m.name = m.key.String() // a name of its own, as an offered model has
		}
		rows = append(rows, m)
	}
	return rows
}

// modelFamilies are the families with a model to show. A family whose
// engine runs nothing, a placeholder, isn't one.
func (a *app) modelFamilies() []family {
	out := []family{}
	for _, f := range a.providerFamilies() {
		if len(a.pickerRows(f)) > 0 {
			out = append(out, f)
		}
	}
	return out
}

// shownFamilies are the ones the picker offers: in a thread, only its own.
func (a *app) shownFamilies() []family {
	return slices.DeleteFunc(a.modelFamilies(), func(f family) bool { return a.pickerLock != "" && f.provider != a.pickerLock })
}

// modelRows is how many model rows the popover shows before its models
// scroll: five, and half of the sixth, so that the cut row shows there
// are more.
const modelRows = 5.5

// pickerReveal is the model row the popover scrolls into view as it opens,
// shows another family, or its family's models change around the row: the
// chosen model's, where it is the family's, else the first, at index at.
// A copy keyed by names (offeredModels) knows it by name. Glitched's own
// drawing of the models and a copy's each scroll to it once.
type pickerReveal struct {
	family, key, name string
	at                int
	native, copied    bool
}

// names is whether a copy's row key names the row: by its key, or by its
// name, as copies made before there were keys do.
func (r pickerReveal) names(key string) bool {
	return r.key != "" && (key == r.key || key == r.name)
}

// efforts are the levels the chosen model runs at, least first.
func (a *app) efforts() []effortInfo {
	m, _ := a.modelOf(a.model)
	return effortsOf(m)
}

// snap carries the effort's thumb to its level: a spring without bounce,
// which takes over a drag at the speed the pointer let it go.
var snap = ui.Animation{Spring: ui.SpringOptions{Duration: 300 * time.Millisecond}}

// snapZone is how near a level's point, in levels, the dragged thumb
// locks onto it.
const snapZone = 0.12

func abs32(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

// effortStops is the effort track's gradient, blue to red, laid across
// the whole track: the more effort, the more of it, and the hotter, the
// fill shows.
var effortStops = []ui.GradientStop{
	{Color: ui.Hex("#2563eb"), At: 0},
	{Color: ui.Hex("#7c3aed"), At: 0.4},
	{Color: ui.Hex("#db2777"), At: 0.72},
	{Color: ui.Hex("#ef4444"), At: 1},
}

var (
	maxPixels = []ui.Color{ui.Hex("#fecaca"), ui.Hex("#fda4af"), ui.Hex("#ffffff"), ui.Hex("#fca5a5"), ui.Hex("#f9a8d4")}
	// electric is fast mode's color: its lightning, its filled icon.
	electric = ui.Hex("#60c3ff")
)

// effortLevel is the chosen effort's place among the model's efforts:
// one it doesn't run at shows as effortFor has it.
func (a *app) effortLevel() int {
	m, _ := a.modelOf(a.model)
	e := effortFor(m, a.effort)
	return slices.IndexFunc(effortsOf(m), func(k effortInfo) bool { return k.id == e })
}

// modelPicker is the composer's model button, and the popover it opens:
// the families of models beside the models of the one pointed at, as a
// submenu, and the effort slider. High effort lights a glow around the
// button, and fast mode runs electricity around it in the glow's place.
// What the button shows and what the popover holds are composer.model's
// two copies, a plugin's or Glitched's own: Glitched keeps the button, its
// glow and electricity, the popover, and what a row does (modelListRow).
func (a *app) modelPicker(c *ui.Context, t thread) {
	a.askModels()
	m := a.shownModel(t)
	level := a.effortLevel()
	effort := a.efforts()[level]
	p := a.modelPending(t)
	own := "Model and effort"
	if !t.id().IsZero() && a.followsCLI {
		own += " · the CLI's default model"
	}
	b := a.tip(c, ghost(c, "model", a.pickerOpen).Padding(4, 8, 4, 4).Gap(6).Label("Model").Shrink(1).MinWidth(0), tipOf(p, own))
	if b.Children(func() {
		a.unconfirmedMark(c, p)
		a.pickerGlow(c, b, effort)
		if a.fast {
			a.electricBorder(c)
		}
		a.pluginComponent(c, "composer.model", &t, componentCopy{
			key: composerKey(t) + "/button",
			box: func() ui.Element {
				return ui.Row(c).Key("component:composer.model/button").Gap(6).AlignItems(ui.Center).Shrink(1).MinWidth(0).ClipX()
			},
			native: func(*widget.Node) {
				name, runs := a.runningModel(t, m.name)
				modelTile(c, m.icon, m.color, 18)
				ui.Text(c, name).FontSize(12).SingleLine().Shrink(1).MinWidth(0)
				if !a.cliEffort(t) && runs.label != "" {
					ui.Text(c, runs.label).FontSize(11).FontWeight(600).TextColor(runs.color).Shrink(0)
				}
				if a.fast {
					a.zapIcon(c, "trigger-zap", 12)
				}
				ui.Icon(c, icChevronD).FontSize(11)
			},
			current: true,
			context: func(r plugins.ThreadRecord) any {
				m := a.composerModelOf(c, t, r, "button")
				var runs effortInfo
				m.Model, runs = a.runningModel(t, m.Model)
				m.Effort = runs.label
				return m
			},
		})
	}).Clicked() {
		a.pickerOpen = !a.pickerOpen
		a.pickerFamily = m.key.Provider
	}
	if a.pickerFamily == "" {
		a.pickerFamily = m.key.Provider
	}
	if a.pickerLock != "" {
		a.pickerFamily = a.pickerLock
	}
	if shown := a.shownFamilies(); len(shown) > 0 && !slices.ContainsFunc(shown, func(f family) bool { return f.provider == a.pickerFamily }) {
		a.pickerFamily = shown[0].provider
	}
	if !a.pickerOpen {
		a.pickerReveal = pickerReveal{}
	} else {
		want := pickerReveal{family: a.pickerFamily}
		rows := a.pickerRows(familyOf(a.pickerFamily))
		if i := max(slices.IndexFunc(rows, func(m modelInfo) bool { return m.key == a.pickedIn(t) }), 0); i < len(rows) {
			want.key, want.name, want.at = rows[i].key.String(), rows[i].name, i
		}
		if r := a.pickerReveal; r.family != want.family || r.key != want.key || r.at != want.at {
			a.pickerReveal = want
		}
	}
	a.pickerAt = b.Bounds()
	a.popover(c, b, &a.pickerOpen, func(panel ui.Element) {
		panel.AttachTo(b, ui.AnchorTopStart, ui.AnchorBottomStart).Margin(0, 0, pickerMargin, -4).Width(420).Radius(16).Clip()
		a.frosted(c, panel).Shadow(0, 16, 40, 0, ui.RGBA(0, 0, 0, 0.35)).Children(func() {
			a.pluginComponent(c, "composer.model", &t, componentCopy{
				key: composerKey(t) + "/panel",
				box: func() ui.Element { return ui.Column(c).Key("component:composer.model/panel") },
				native: func(*widget.Node) {
					ui.Row(c).AlignItems(ui.Stretch).Children(func() {
						a.pickerFamilies(c)
						ui.Box(c).Width(1).Background(palette.border)
						a.pickerModels(c, t)
					})
					ui.Divider(c).Background(palette.border)
					a.effortSlider(c)
				},
				current: true,
				context: func(r plugins.ThreadRecord) any { return a.composerModelOf(c, t, r, "panel") },
				listRow: func(c *ui.Context, list *widget.Node, i int, e ui.Element, _ bool) bool {
					a.modelListRow(list.ID, list.Items[i].Key, e)
					return false
				},
				reveal: func(list *widget.Node, it widget.ListItem) bool {
					return list.ID == "models" && !a.pickerReveal.copied && a.pickerReveal.names(it.Key)
				},
			})
		})
	})
}

// composerModel is the context of composer.model: the button's copy, or
// the popover's, of the picker as it shows the thread's choice, or a new
// thread's default in the draft.
type composerModel struct {
	Thread plugins.ThreadRecord `json:"thread"`
	// Part is the copy's: "button" or "panel".
	Part string `json:"part"`
	// Model and Effort are what the button shows of the chosen model and
	// effort; Key is the row key of the model.
	Model  string `json:"model"`
	Key    string `json:"key"`
	Effort string `json:"effort"`
	Fast   bool   `json:"fast"`
	// FamilyKey is the provider whose models the popover shows, and
	// LockKey the only one it offers, a thread's provider, or "" for
	// every one; Family and Lock are their families' names, as a copy
	// made before there were keys knows them.
	FamilyKey string        `json:"familyKey"`
	LockKey   string        `json:"lockKey"`
	Family    string        `json:"family"`
	Lock      string        `json:"lock"`
	Families  []modelFamily `json:"families"`
	Models    []modelChoice `json:"models"`
	// FamilyRowHeight and ModelRowHeight are how tall Glitched's own
	// popover draws a family's row and a model's, from the interface
	// font's lines on this platform.
	FamilyRowHeight float32 `json:"familyRowHeight"`
	ModelRowHeight  float32 `json:"modelRowHeight"`
	// ModelsMaxHeight is how tall the models' column may be, padding and
	// all, past which its rows scroll: modelRows of them, less what the
	// window has no room for above the button.
	ModelsMaxHeight float32 `json:"modelsMaxHeight"`
}

// modelFamily is a family by its provider, Key, and what it shows.
type modelFamily struct {
	Key   string `json:"key"`
	Name  string `json:"name"`
	About string `json:"about"`
}

// modelChoice is a model by its row key, Key, its family's provider,
// FamilyKey, and what it shows, its family's name as Family.
type modelChoice struct {
	Key       string `json:"key"`
	Name      string `json:"name"`
	Short     string `json:"short"`
	About     string `json:"about"`
	FamilyKey string `json:"familyKey"`
	Family    string `json:"family"`
}

func (a *app) composerModelOf(c *ui.Context, t thread, r plugins.ThreadRecord, part string) composerModel {
	size := c.Theme().FontSize
	m := a.shownModel(t)
	out := composerModel{Thread: r, Part: part, Model: m.name, Key: a.pickedIn(t).String(), Effort: a.efforts()[a.effortLevel()].label, Fast: a.fast, FamilyKey: a.pickerFamily, LockKey: a.pickerLock,
		FamilyRowHeight: familyRowHeight(c),
		ModelRowHeight:  modelRowHeight(c, size),
		ModelsMaxHeight: a.modelsMaxHeight(c),
	}
	out.Family = familyOf(a.pickerFamily).name
	if a.pickerLock != "" {
		out.Lock = familyOf(a.pickerLock).name
	}
	out.Families, out.Models = []modelFamily{}, []modelChoice{}
	for _, f := range a.modelFamilies() {
		out.Families = append(out.Families, modelFamily{f.provider, f.name, f.desc})
		for _, m := range a.pickerRows(f) {
			out.Models = append(out.Models, modelChoice{m.key.String(), m.name, m.short, a.rowAbout(t, m), f.provider, f.name})
		}
	}
	return out
}

// shownModel is the model the picker shows in t's composer: the chosen
// one, or, where t's provider's program chooses it (cliDecides), or t
// follows a default no one here expected or reported, its default by
// that name alone.
func (a *app) shownModel(t thread) modelInfo {
	m, _ := a.modelOf(a.model)
	if a.cliDecides(t) || a.followsCLI && a.model.ID == "" {
		m.name = "CLI default"
	}
	return m
}

// pickedIn is the model the picker checks in t's composer: the chosen
// one, but none where t's provider's program chooses (cliDecides), as no
// row is the one it runs.
func (a *app) pickedIn(t thread) modelKey {
	if a.cliDecides(t) {
		return modelKey{}
	}
	return a.model
}

// rowAbout is what a model's row in t's composer says of it: that its
// provider's program runs it where given none, where this machine's
// engine knows that program (cliDecides), and what it's for.
func (a *app) rowAbout(t thread, m modelInfo) string {
	if def, ok := a.cliDefault(m.key.Provider); !ok || def.key != m.key || a.cliDecides(t) {
		return m.desc
	}
	return strings.Join(slices.DeleteFunc([]string{"CLI default", m.desc}, func(s string) bool { return s == "" }), " · ")
}

func familyRowHeight(c *ui.Context) float32 {
	return 12 + max(22, 1+lineHeight(c, c.Theme().FontSize, 500)+lineHeight(c, 11, 0))
}

func modelRowHeight(c *ui.Context, size float32) float32 {
	return 14 + max(14, 1+lineHeight(c, size, 500)+lineHeight(c, 11.5, 0))
}

// pickerPad and pickerGap are the padding of the popover's columns and the
// gap between their rows.
const (
	pickerPad = 6
	pickerGap = 1
)

// modelsMaxHeight is how tall the models' column may be: modelRows rows,
// but no taller than the window has room for above the button, with the
// effort under it, so the popover stays in the window. One row shows at
// the least.
func (a *app) modelsMaxHeight(c *ui.Context) float32 {
	rowH := modelRowHeight(c, c.Theme().FontSize)
	rows := func(n float32) float32 { return 2*pickerPad + n*rowH + (n-1)*pickerGap }
	most := rows(modelRows)
	if room := a.pickerAt.Y - pickerMargin - pickerEdge - 1 - a.effortBox.Bounds(c).H; a.pickerAt.H > 0 && room < most {
		most = room
	}
	return max(most, rows(1))
}

// pickerMargin is the popover's gap above the button; pickerEdge the
// least it keeps from the window's top.
const (
	pickerMargin = 10
	pickerEdge   = 8
)

// modelListRow does what the popover's own rows do to the row e of a
// copy's list: of "families", pointed at or clicked, it shows the
// family's models; of "models", clicked, it chooses the model. A row is
// keyed by the family's provider or the model's key, or, as copies did
// before there were keys, by its name, which is its own (offeredModels). Only what
// the picker offers: a family or a model past the thread's lock, or none
// of the picker's, does nothing.
func (a *app) modelListRow(list, key string, e ui.Element) {
	switch list {
	case "families":
		i := slices.IndexFunc(a.shownFamilies(), func(f family) bool { return f.provider == key || f.name == key })
		if i < 0 {
			return
		}
		if e.Hovered() || e.Clicked() {
			a.pickerFamily = a.shownFamilies()[i].provider
		}
	case "models":
		if a.pickerReveal.names(key) {
			a.pickerReveal.copied = true
		}
		i := slices.IndexFunc(a.offeredModels(), func(m modelInfo) bool { return m.key.String() == key || m.name == key })
		if i < 0 || a.pickerLock != "" && a.offeredModels()[i].key.Provider != a.pickerLock {
			return
		}
		if e.Clicked() {
			a.chooseModel(a.offeredModels()[i].key)
		}
	}
}

// fastButton turns fast mode on and off, beside the effort in the
// picker: just its lightning, which strikes as it comes on.
func (a *app) fastButton(c *ui.Context) {
	label := "Fast mode: off"
	if a.fast {
		label = "Fast mode: on"
	}
	b := a.tip(c, ghost(c, "fast", false).Padding(6).Role(ui.RoleSwitch).Checked(a.fast).Label("Fast mode"), label)
	if b.Children(func() { a.zapIcon(c, "fast-zap", 14) }).Clicked() {
		a.fast = !a.fast
		if a.fast {
			a.fastAt = c.Now()
		}
	}
}

// modelTile is a model's provider mark, white on the provider's color.
func modelTile(c *ui.Context, icon *ui.SVG, color ui.Color, size float32) {
	ui.Box(c).Size(size, size).Radius(size * 0.3).Center().
		LinearGradient(ui.LinearGradient{From: color.Mix(ui.Hex("#ffffff"), 0.12), To: color.Mix(ui.Hex("#000000"), 0.15), Angle: 135, Oklab: true}).
		Children(func() { ui.Icon(c, icon).FontSize(size * 0.68).TextColor(ui.Hex("#ffffff")) })
}

// pickerGlow is a soft glow around the model button, in the effort's
// colors from High up: borders of a gradient, a
// sharp one and blurred ones stacked. At Max it slowly turns. Fast mode's
// electricity takes its place. Without a GPU only the sharp one shows: a
// blurred layer is drawn anew every frame, and the glass composer under it
// with it.
func (a *app) pickerGlow(c *ui.Context, b ui.Element, effort effortInfo) {
	on := effort.glow
	if a.fast {
		on = 0
	}
	// The glow fades in and out as the settings change.
	k := b.AnimateTo("glow", on, ui.Animation{Duration: 250 * time.Millisecond, Ease: easeOut})
	if k < 0.01 {
		return
	}
	stops := []ui.GradientStop{
		{Color: effortStops[1].Color, At: 0},
		{Color: effortStops[2].Color, At: 0.35},
		{Color: effortStops[3].Color, At: 0.65},
		{Color: effortStops[1].Color, At: 1},
	}
	angle := float32(0)
	if effort.id == "max" {
		angle = b.Loop("turn", 4*time.Second, ui.Linear) * 360
	}
	g := ui.ConicGradient{Stops: stops, Angle: angle, Oklab: true}
	layers := []struct{ width, blur, alpha float32 }{{1, 0, 0.9}, {2.5, 3, 0.6}, {5, 9, 0.45}}
	if !gpu {
		layers = layers[:1]
	}
	for _, l := range layers {
		ui.Box(c).Absolute().Top(-1).Left(-1).Right(-1).Bottom(-1).PassThrough().Radius(7).
			Border(l.width, ui.Color{}).BorderConicGradient(g).Blur(l.blur).Opacity(k * l.alpha)
	}
}

func (a *app) pickerFamilies(c *ui.Context) {
	ui.Column(c).Width(150).Padding(pickerPad).Gap(pickerGap).Children(func() {
		for _, f := range a.shownFamilies() {
			open := f.provider == a.pickerFamily
			r := row(c, f.name, open).Padding(6, 8).Gap(8)
			// Pointing at a family opens its models, as a submenu does.
			if r.Hovered() {
				a.pickerFamily = f.provider
			}
			if r.Children(func() {
				modelTile(c, f.icon, f.color, 22)
				ui.Column(c).Grow(1).Gap(1).Children(func() {
					ui.Text(c, f.name).FontWeight(500).TextColor(palette.text)
					ui.Text(c, f.desc).FontSize(11).TextColor(palette.muted)
				})
				ui.Icon(c, icChevronR).FontSize(11)
			}).Clicked() {
				a.pickerFamily = f.provider
			}
		}
	})
}

// pickerModels shows the models of the family pointed at. Every family's
// list is built in the same cell, the others invisible, so the column is
// as tall as the longest list whichever shows: the popover keeps its size,
// and the families stay put under the pointer as it moves between them.
// Past modelsMaxHeight the lists scroll, to pickerReveal's row.
func (a *app) pickerModels(c *ui.Context, t thread) {
	picked := a.pickedIn(t)
	ui.Scroll(c).Key("models").Grow(1).MaxHeight(a.modelsMaxHeight(c)).Children(func() {
		ui.Grid(c).Columns(1).Children(func() {
			for _, f := range a.shownFamilies() {
				list := ui.Column(c).Key(f.name).RowStart(1).ColumnStart(1).Padding(pickerPad).Gap(pickerGap)
				if f.provider != a.pickerFamily {
					list.Invisible()
				}
				reveal := f.provider == a.pickerFamily && !a.pickerReveal.native
				list.Children(func() {
					for i, m := range a.pickerRows(f) {
						r := a.pickerModel(c, t, m, picked)
						if !reveal || m.key.String() != a.pickerReveal.key {
							continue
						}
						a.pickerReveal.native = true
						if i == 0 {
							list.ScrollIntoView(ui.ScrollIntoViewOptions{Block: ui.ScrollStart})
						} else {
							r.ScrollIntoView()
						}
					}
				})
			}
		})
	})
}

func (a *app) pickerModel(c *ui.Context, t thread, m modelInfo, picked modelKey) ui.Element {
	selected := m.key == picked
	r := row(c, m.key.String(), selected).Padding(7, 10).Gap(10).Children(func() {
		ui.Column(c).Grow(1).Shrink(1).Gap(1).Children(func() {
			ui.Text(c, m.short).FontWeight(500).TextColor(palette.text).SingleLine()
			ui.Text(c, a.rowAbout(t, m)).FontSize(11.5).TextColor(palette.muted).SingleLine()
		})
		if selected {
			ui.Icon(c, icCheck).FontSize(14).TextColor(palette.accent)
		}
	})
	if r.Clicked() {
		a.chooseModel(m.key)
	}
	return r
}

// effortSlider steps from Low to Max, as Codex's: a track whose gradient
// fill grows warmer as the effort goes up, twinkling at High and Max, a
// burst and a glow at the thumb at Max.
func (a *app) effortSlider(c *ui.Context) {
	level, es := a.effortLevel(), a.efforts()
	last := len(es) - 1
	var ids []string
	for _, e := range es {
		ids = append(ids, e.id)
	}
	if k := strings.Join(ids, " "); k != a.effortIDs {
		// A press of the old efforts chooses nothing of these, till it's
		// let go.
		a.effortIDs, a.effortHeld, a.effortLock, a.effortVoid = k, false, -1, true
	}
	ui.Column(c).Bind(&a.effortBox).Padding(12, 14, 14).Gap(10).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "Effort").FontWeight(500)
			// The new level rises in as it changes.
			ui.Text(c, es[level].label).Key(es[level].id).FontWeight(600).TextColor(es[level].color).Transition(riseIn).Margin(0, 0, 0, 8)
			ui.Spacer(c)
			a.fastButton(c)
		})
		v := float64(level)
		// One effort is a track with its one stop, at the start.
		steps := float32(max(last, 1))
		sb := ui.SliderBase(c.Key("effort"), &v, 0, float64(steps)).Step(1).Height(28).Label("Effort").Cursor(ui.CursorPointer)
		setLevel := func(l int) {
			l = min(max(l, 0), last)
			// A thread's effort the slider doesn't list shows as
			// effortFor has it, which it takes once chosen.
			if l == level && es[l].id == a.effort {
				return
			}
			haptic() // a tick on the trackpad at each step
			if es[l].id == "max" {
				a.maxAt = c.Now()
			}
			a.effort = es[l].id
			level = l
		}
		// While the pointer holds the thumb, it stays under the pointer,
		// free between the levels; only within a small zone around a
		// level's point does it lock onto the point, choosing that level.
		// Let go, a spring carries it to the level it last locked onto,
		// from where it was, at the speed it was going: half way to the
		// next level is not enough. Keys and clicks on the track move it
		// by the same spring.
		// Held is the whole press, not only the frames the pointer moves
		// in: a pause mid-drag keeps the thumb where it is.
		// The thumb chooses a level as it locks onto it, once: a thread
		// whose engine refuses the level shows its own again, and the
		// thumb still held there mustn't ask for it each frame.
		lock := func(l int) {
			if l != a.effortLock {
				a.effortLock = l
				setLevel(l)
			}
		}
		var pos float32
		held := sb.Pressed()
		void := a.effortVoid
		if !held {
			a.effortVoid = false
		}
		if w := sb.Bounds().W; held && !void && w > 2*stopInset {
			x, _, _ := sb.PointerPosition()
			at := min(max((x-stopInset)/(w-2*stopInset)*steps, 0), float32(last))
			if !a.effortHeld {
				a.effortHeld, a.effortPressAt, a.effortLock = true, at, -1
			}
			a.effortPressed, a.effortLastAt = true, at
			if nearest := float32(int(at + 0.5)); abs32(at-nearest) <= snapZone {
				lock(int(nearest))
				at = nearest
			}
			pos = sb.Jump("pos", at)
		} else {
			if a.effortHeld {
				// A click, pressed and let go in place, picks the level
				// nearest it, wherever on the track.
				a.effortHeld = false
				if abs32(a.effortLastAt-a.effortPressAt) < 0.05 {
					lock(int(a.effortLastAt + 0.5))
				}
			}
			if sb.Changed() && !void {
				setLevel(int(v + 0.5))
			}
			pos = sb.AnimateTo("pos", float32(level), snap)
		}
		maxAt := a.maxAt
		sb.Draw(func(p *ui.Painter, r ui.Rect) {
			if drawEffortTrack(p, r, pos, es, level, maxAt, &a.effortFlow) {
				p.AnimationFrame()
			}
		})
		ui.Row(c).Children(func() {
			ui.Text(c, "Faster").FontSize(11).TextColor(palette.faint).Grow(1)
			ui.Text(c, "Smarter").FontSize(11).TextColor(palette.faint)
		})
	})
}

const (
	trackH     = 20
	thumbR     = 12
	stopInset  = 10 // the thumb's center at the first and last stops
	burstTime  = 650 * time.Millisecond
	flashTime  = 550 * time.Millisecond
	strikeTime = 450 * time.Millisecond
	glowRadius = 26
)

// drawEffortTrack draws the track in r of the efforts es with the thumb pos
// levels along it, at level, and reports whether it's still moving.
func drawEffortTrack(p *ui.Painter, r ui.Rect, pos float32, es []effortInfo, level int, maxAt time.Time, flow *flowState) bool {
	t := p.Now()
	steps := float32(max(len(es)-1, 1))
	x0, x1 := r.X+stopInset, r.X+r.W-stopInset
	cy := r.Y + r.H/2
	tx := x0 + (x1-x0)*pos/steps
	track := ui.Rect{X: r.X, Y: cy - trackH/2, W: r.W, H: trackH}
	p.Fill(track, palette.text.Alpha(0.08), trackH/2)

	// The fill, up to the thumb, shows the gradient laid across the whole
	// track, brighter the more effort.
	fill := ui.Rect{X: track.X, Y: track.Y, W: tx - track.X, H: trackH}
	g := ui.LinearGradient{Stops: stopsUpTo(effortStops, fill.W/r.W), Angle: 90, Oklab: true}
	p.FillGradient(fill, g, trackH/2)
	// Light flows through the fill toward the thumb, as through a fibre:
	// more of it, and faster, the more effort.
	// The flow's levels are four's: more efforts share them.
	flowLevel := (level*3 + (len(es)-1)/2) / max(len(es)-1, 1)
	p.Clip(fill, trackH/2, func() { flow.draw(p, track, fill, flowLevel) })
	animating := fill.W > 0

	// The stops.
	for i := range len(es) {
		x := x0 + (x1-x0)*float32(i)/steps
		col := palette.text.Alpha(0.25)
		if float32(i) <= pos {
			col = ui.RGBA(255, 255, 255, 0.6)
		}
		p.Fill(ui.Rect{X: x - 1.5, Y: cy - 1.5, W: 3, H: 3}, col, 1.5)
	}

	// At Max, a glow behind the thumb, a flash and a burst as it arrives.
	if es[level].id == "max" {
		drawGlow(p, tx, cy, glowRadius, effortStops[3].Color, 0.55)
		since := t.Sub(maxAt)
		if !maxAt.IsZero() && since < burstTime {
			drawBurst(p, tx, cy, thumbR+1, thumbR+16, float32(since)/float32(burstTime), maxPixels)
			if k := float32(since) / float32(flashTime); k < 1 {
				drawGlow(p, tx, cy, glowRadius+8, ui.Hex("#fff7ed"), 0.7*(1-k)*(1-k))
			}
		}
	}

	// The thumb.
	thumb := ui.Rect{X: tx - thumbR, Y: cy - thumbR, W: 2 * thumbR, H: 2 * thumbR}
	p.Shadow(thumb, thumbR, 0, 1, 4, 0, ui.RGBA(0, 0, 0, 0.35))
	p.Fill(thumb, ui.Hex("#ffffff"), thumbR)
	p.Fill(ui.Rect{X: tx - 3, Y: cy - 3, W: 6, H: 6}, es[level].color, 3)
	return animating || float32(level) != pos
}

// zapIcon is the lightning of fast mode: an outline while it's off,
// filled electric blue while it's on. As it comes on, lightning strikes out of
// it: forking bolts that flicker a few times, new each flicker, the icon
// flashing white.
func (a *app) zapIcon(c *ui.Context, key string, size float32) {
	icon, col := icZap, palette.muted
	if a.fast {
		icon, col = icZapFilled, electric
	}
	at := a.fastAt
	striking := a.fast && !at.IsZero() && c.Now().Sub(at) < strikeTime
	box := ui.Box(c).Key(key).Size(size, size).Children(func() {
		z := ui.Icon(c, icon)
		flash := z.Keyframes("flash", []ui.Keyframe{
			{Value: 0},
			{At: 30 * time.Millisecond, Value: 1, Ease: easeOut},
			{At: strikeTime, Value: 0, Ease: easeOut},
		}, ui.KeyframeOptions{Run: uint64(at.UnixNano())})
		if !striking {
			flash = 0
		}
		z.FontSize(size).TextColor(col.Mix(boltCore, flash))
	})
	if !striking {
		return
	}
	box.DrawOver(func(p *ui.Painter, r ui.Rect) {
		since := p.Now().Sub(at)
		if since >= strikeTime {
			return
		}
		k := float32(since) / float32(strikeTime)
		lit := strobe(k)
		cx, cy := r.X+r.W/2, r.Y+r.H/2
		drawGlow(p, cx, cy, size*2.2, boltGlow, 0.7*lit)
		flicker := int(since / (45 * time.Millisecond))
		for i := range 4 {
			angle := (float32(i)+0.6*hash(i, flicker, 9))*math.Pi/2 + 0.4
			drawBolt(p, cx, cy, angle, size*(1.3+0.7*hash(i, flicker, 10)), 1.3, i*131+flicker*17, lit)
		}
		p.AnimationFrame()
	})
}

// electricBorder runs a charged line around the model button while fast
// mode is on, flashing brighter as the zap strikes it on. Where the
// desktop asks for less motion, or the window draws without a GPU, the
// line holds still.
func (a *app) electricBorder(c *ui.Context) {
	at := a.fastAt
	reduce := c.Preferences().ReduceMotion
	dark := a.theme.dark
	m := float32(elMargin - 1)
	ui.Box(c).Absolute().Top(-m).Left(-m).Right(-m).Bottom(-m).PassThrough().Draw(func(p *ui.Painter, r ui.Rect) {
		still := reduce || !p.Accelerated()
		since := p.Now().Sub(at)
		if at.IsZero() {
			since = p.Now().Sub(electricStart)
		}
		k := float32(1)
		if !at.IsZero() && since < strikeTime {
			k += 0.6 * strobe(float32(since)/float32(strikeTime))
		}
		drawElectricity(p, r, 7, since.Seconds(), still, k, dark)
		if !still {
			p.AnimationFrame()
		}
	})
}

// stopsUpTo returns the part of a gradient from 0 to f, laid out from 0 to
// 1: the fill shows the track's gradient only as far as the thumb, the
// same colors at every level, red only at Max.
func stopsUpTo(stops []ui.GradientStop, f float32) []ui.GradientStop {
	if f <= 0 {
		return stops[:1]
	}
	var out []ui.GradientStop
	for i, s := range stops {
		if s.At <= f {
			out = append(out, ui.GradientStop{Color: s.Color, At: s.At / f})
			continue
		}
		// The color at f, between the last stop and this one.
		prev := stops[i-1]
		k := (f - prev.At) / (s.At - prev.At)
		out = append(out, ui.GradientStop{Color: prev.Color.Mix(s.Color, k), At: 1})
		break
	}
	return out
}
