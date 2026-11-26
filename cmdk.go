package main

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/plugins"
	"glitched/internal/plugins/widget"
)

type command struct {
	// id names an action to glitched.commands.run; a thread has none.
	id      string
	section string
	label   string
	hint    string
	// icon is the name of one of the app's icons (appIconNamed).
	icon string
	run  func()
	// also are words the query finds the command by, not shown: each
	// word of the query is in the label, or is one of them whole.
	also string
	// t is the thread a Threads command opens, with its diff on ⌘↩.
	t *thread
}

func (a *app) openCommandPalette() {
	if a.wizard.open {
		return
	}
	a.cmdkOpen, a.cmdkQuery, a.cmdkIndex = true, "", 0
	a.cmdkHits, a.cmdkHitsQuery, a.cmdkSearched = nil, "", ""
	a.cmdkOpens++
}

// paletteKey is the key of the palette's opening now, its copy of the
// palette component.
func (a *app) paletteKey() string { return "palette:" + strconv.Itoa(a.cmdkOpens) }

// closePalette closes the palette's opening of that key, "" for any: what
// was chosen in one closed since leaves the next be.
func (a *app) closePalette(key string) {
	if key == "" || key == a.paletteKey() {
		a.cmdkOpen = false
	}
}

// id names the project in an action's id without its workspace's path,
// which only threads:read reads.
func (k projectKey) id() string {
	sum := sha256.Sum256([]byte(k.String()))
	return hex.EncodeToString(sum[:8])
}

// actions are the palette's actions, in its order, which plugins list and
// run as glitched.commands.list and run.
func (a *app) actions() []command {
	action := func(id, label, hint, icon string, run func()) command {
		return command{id: id, section: "Actions", label: label, hint: hint, icon: icon, run: run}
	}
	cmds := []command{action("thread.new", "New thread", "⌘N", "pen-new-square", a.newThread)}
	if e := a.localEnv(); e != nil {
		cmds = append(cmds, action("thread.new.scratch", "New thread without a project", "", "pen-new-square", func() { a.startIn(draft{e, a.scratchRoot()}) }))
	}
	for _, p := range a.projects {
		if !p.noProject {
			cmds = append(cmds, action("thread.new:"+p.key.id(), "New thread in "+a.projectLabel(p), "", "pen-new-square", func() { a.startThread(p) }))
		}
	}
	if len(a.backend.Envs()) > 0 && !a.wizard.open {
		add := action("project.add", "Add project…", "", "folder-open", func() { a.openWizard(sourceNone) })
		add.also = "workspace folder"
		cmds = append(cmds, add)
		clone := action("project.clone", "Clone repository…", "", "git-branch", func() { a.openWizard(sourceClone) })
		clone.also = "git github repo url"
		cmds = append(cmds, clone)
	}
	// Each is listed while an agent works in it too, lest the rows shift
	// under the one chosen as a run starts; its alert says to stop it.
	for _, p := range a.projects {
		label := "Remove project " + a.projectLabel(p) + "…"
		if p.noProject {
			label = "Remove threads without a project…"
		}
		rm := action("project.remove:"+p.key.id(), label, "", "folder", func() { a.askRemove(p) })
		rm.also = "workspace delete"
		cmds = append(cmds, rm)
	}
	cmds = append(cmds,
		action("model.switch", "Switch model", "", "sparkles", a.openModelPicker),
		action("thread.find", "Find in thread", "⌘F", "magnifer", func() { a.router.Push("/"); a.openFind() }),
		action("diff.toggle", "Toggle diff panel", "⌘D", "pull-request", func() { a.showDiff = !a.showDiff }),
		action("terminal.toggle", "Toggle terminal", "⌘J", "terminal", func() { a.showTerminal = !a.showTerminal }),
		action("preview.toggle", "Toggle preview", "⇧⌘P", "global", a.togglePreview),
		action("browser.newTab", "New browser tab", "⌘T", "plus", func() { a.newTab("") }),
		action("sidebar.toggle", "Toggle sidebar", "⌘B", "sidebar-minimalistic", func() { a.toggleSidebar(false) }),
		action("theme.toggle", "Toggle light / dark", "⇧⌘L", "pallete-2", a.toggleLightDark),
		action("zen.toggle", "Toggle Zen mode", "", "tuning", func() { a.settings.zen = !a.settings.zen }),
	)
	for i, s := range settingsSections {
		hint := ""
		if i == 0 {
			hint = "⌘,"
		}
		path := "/settings/" + s.path
		cmds = append(cmds, action("settings:"+s.path, "Settings › "+s.label, hint, s.icon, func() { a.router.Push(path) }))
	}
	return append(cmds, a.pluginCommands()...)
}

// commands are what the palette offers for the query: the actions, then
// the threads, the most recently active first. A query starting with >
// keeps to the actions.
func (a *app) commands() []command {
	cmds := a.actions()
	query, actionsOnly := strings.CutPrefix(a.cmdkQuery, ">")
	query = strings.TrimSpace(query)
	if !actionsOnly {
		cmds = append(cmds, a.threadCommands()...)
	}
	var shown []command
	for _, cmd := range cmds {
		if matches(cmd.label, query) || matches(cmd.hint, query) || alsoMatches(cmd, query) {
			shown = append(shown, cmd)
		}
	}
	if !actionsOnly && a.cmdkHitsQuery == query {
		shown = append(shown, a.messageCommands(query)...)
	}
	return shown
}

// triggerCmdkSearch searches the messages for query, unless it is what
// was searched last. The hits of the query before go at once.
func (a *app) triggerCmdkSearch(query string) {
	q := strings.TrimSpace(query)
	if q == a.cmdkSearched {
		return
	}
	a.cmdkHits, a.cmdkHitsQuery, a.cmdkSearched = nil, "", q
	a.cmdkSearchGen++
	if len(q) < 2 {
		return
	}
	gen := a.cmdkSearchGen
	a.backend.Search(q, 20, func(hits []client.SearchMatch) {
		if a.cmdkSearchGen == gen && a.cmdkOpen {
			a.cmdkHits, a.cmdkHitsQuery = hits, q
		}
	})
}

// messageCommands are the palette's messages found for query, each
// opening its thread with the find bar on it.
func (a *app) messageCommands(query string) []command {
	var out []command
	for _, m := range a.cmdkHits {
		t, _, ok := a.hitThread(m)
		if !ok {
			continue
		}
		seq := m.Hit.Seq
		out = append(out, command{section: "Messages", label: title(t), hint: hitSnippet(m.Hit), run: func() { a.openHit(t, query, seq) }, t: &t})
	}
	return out
}

// openHit shows t, with the find bar on the message search found at seq
// when find names the query.
func (a *app) openHit(t thread, find string, seq uint64) {
	a.select_(t)
	a.revealing = t.id()
	if find != "" {
		a.openFindAt(find, seq)
	}
}

// hitThread is the thread a hit is in, and its project's name.
func (a *app) hitThread(m client.SearchMatch) (thread, string, bool) {
	for _, p := range a.projects {
		for _, t := range p.threads {
			if t.id() == m.Hit.Thread && (m.Env == nil || t.env == m.Env) {
				return t, p.name(), true
			}
		}
	}
	return thread{}, "", false
}

// hitSnippet is a hit's words around the match on one line, at most 160
// letters.
func hitSnippet(h api.SearchHit) string {
	s := strings.Join(strings.Fields(h.Snippet), " ")
	if s == "" {
		return h.Role + " match"
	}
	if r := []rune(s); len(r) > 160 {
		s = string(r[:159]) + "…"
	}
	return s
}

// searchHit is a message threads.search found, as a plugin reads it.
type searchHit struct {
	Thread  string `json:"thread"`
	Title   string `json:"title"`
	Project string `json:"project"`
	Snippet string `json:"snippet"`
	Seq     uint64 `json:"seq"`
	Role    string `json:"role"`
	Ago     string `json:"ago"`
}

func (a *app) searchHits(ms []client.SearchMatch) []searchHit {
	out := []searchHit{}
	for _, m := range ms {
		if t, project, ok := a.hitThread(m); ok {
			out = append(out, searchHit{t.id().String(), title(t), project, hitSnippet(m.Hit), m.Hit.Seq, m.Hit.Role, ago(t.View.UpdatedAt, a.now)})
		}
	}
	return out
}

// threadCommands are the palette's threads, the most recently active
// first.
func (a *app) threadCommands() []command {
	var threads []command
	for _, p := range a.projects {
		for _, t := range p.threads {
			threads = append(threads, command{section: "Threads", label: title(t), hint: p.name(), run: func() { a.select_(t); a.revealing = t.id() }, t: &t})
		}
	}
	slices.SortStableFunc(threads, func(x, y command) int { return newestFirst(*x.t, *y.t) })
	return threads
}

// paletteContext is the palette component's context: the query it opened
// with, and what it finds, which it filters as the query changes: the
// actions, then the threads, the most recently active first. RowHeight,
// HeaderHeight and NoneHeight are how tall Glitched's own palette draws a
// row, a section's header and No results, in points: they follow the
// font's lines on each platform, a row's the tallest of its 14 point
// icon, its label's and its 11.5 point hint's.
type paletteContext struct {
	Query        string          `json:"query"`
	Actions      []commandRecord `json:"actions"`
	Threads      []paletteThread `json:"threads"`
	RowHeight    float32         `json:"rowHeight"`
	HeaderHeight float32         `json:"headerHeight"`
	NoneHeight   float32         `json:"noneHeight"`
}

type paletteThread struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Project string `json:"project"`
	// Ago is how long ago it was last active, as its row says it.
	Ago string `json:"ago"`
}

// lineHeight is how tall a line of text of the interface font draws, at
// size and weight, on this platform: a text element's height, padding
// aside.
func lineHeight(c *ui.Context, size float32, weight int) float32 {
	m := ui.Font{Family: c.Theme().Font, Size: size, Weight: weight}.Metrics()
	return m.Ascent + m.Descent + m.LineGap
}

func (a *app) paletteContext(c *ui.Context) paletteContext {
	size := c.Theme().FontSize
	ctx := paletteContext{Query: a.cmdkQuery, Actions: a.commandRecords(), Threads: []paletteThread{},
		RowHeight:    16 + max(14, lineHeight(c, size, 0), lineHeight(c, 11.5, 0)),
		HeaderHeight: 12 + lineHeight(c, 11, 600),
		NoneHeight:   48 + lineHeight(c, size, 0),
	}
	for _, cmd := range a.threadCommands() {
		ctx.Threads = append(ctx.Threads, paletteThread{cmd.t.id().String(), cmd.label, cmd.hint, ago(cmd.t.View.UpdatedAt, a.now)})
	}
	return ctx
}

// openModelPicker opens the composer's model picker, from wherever the
// palette was: the chat, with the composer at its full size, holds it.
// The picker is the shown thread's composer's from here, which may not
// have built its pickers since it came: compact or off screen, it builds
// none, and would close this one as another composer's.
func (a *app) openModelPicker() {
	if a.router.Path() != "/" {
		a.router.Push("/")
	}
	a.pickersIn(a.current().id())
	a.composerOpened, a.pickerOpen, a.pickerFamily = true, true, ""
}

// commandPalette is a sheet of glass over the blurred window. It's opened
// from the keyboard many times a day, so it shows and hides at once, and
// its selection moves at once with the arrows. What's on the glass is the
// palette component, each opening a copy of its own.
func (a *app) commandPalette(c *ui.Context) {
	a.dialog(c, &a.cmdkOpen, func(backdrop, panel ui.Element) {
		backdrop.Justify(ui.Start).Padding(96, 0, 0)
		if a.settings.glass {
			backdrop.Material(glass.Blur{Radius: 3})
		} else {
			backdrop.Background(ui.RGBA(0, 0, 0, 0.35))
		}
		panel.Width(600).Radius(18).Clip()
		a.frosted(c, panel).Shadow(0, 24, 64, 0, ui.RGBA(0, 0, 0, 0.4))
		ctx := a.paletteContext(c)
		a.pluginComponent(c, "palette", nil, componentCopy{
			key:     a.paletteKey(),
			box:     func() ui.Element { return ui.Column(c) },
			context: func(plugins.ThreadRecord) any { return ctx },
			native:  func(*widget.Node) { a.nativePalette(c) },
		})
	})
}

// nativePalette is Glitched's own drawing of the palette, which shows
// where no plugin draws it.
func (a *app) nativePalette(c *ui.Context) {
	if query, actionsOnly := strings.CutPrefix(a.cmdkQuery, ">"); !actionsOnly {
		a.triggerCmdkSearch(query)
	}
	cmds := a.commands()
	a.cmdkIndex = max(0, min(a.cmdkIndex, len(cmds)-1))
	// The arrows move the choice from the search field, which keeps
	// the keyboard, and the list scrolls it into view.
	moved := false
	if c.Shortcut(0, ui.KeyDown) && len(cmds) > 0 {
		a.cmdkIndex, moved = (a.cmdkIndex+1)%len(cmds), true
	}
	if c.Shortcut(0, ui.KeyUp) && len(cmds) > 0 {
		a.cmdkIndex, moved = (a.cmdkIndex-1+len(cmds))%len(cmds), true
	}
	if moved {
		a.cmdkList.ScrollIntoView(a.cmdkIndex)
	}

	ui.Row(c).Gap(10).Padding(14, 16).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(palette.border).Children(func() {
		ui.Icon(c, icSearch).FontSize(16).TextColor(palette.muted)
		in := ui.TextInput(c.Key("q"), &a.cmdkQuery).Placeholder("Search threads and commands…").AutoFocus().
			Grow(1).Background(ui.Transparent).Border(0, ui.Transparent).FocusRing(false).Padding(0).FontSize(15)
		if in.Changed() {
			a.cmdkIndex = 0
		}
		if len(cmds) > 0 {
			cmd := cmds[a.cmdkIndex]
			switch {
			case in.Submitted():
				a.cmdkOpen = false
				cmd.run()
			case cmd.t != nil && c.Shortcut(ui.Cmd, ui.KeyEnter):
				a.cmdkOpen = false
				cmd.run()
				a.showDiff, a.showPreview = true, false
			}
		}
		kbd(c, "esc")
	})
	// The results are a ui.List, as MyGo's launcher example has them,
	// which builds the rows in view and scrolls the choice into view.
	// The choice is the palette's own, which the search field's arrows,
	// Enter and clicks move: the list's selection would paint its
	// accent on the section headers too.
	l := &a.cmdkList
	l.Key = func(i int) any {
		if t := cmds[i].t; t != nil {
			return cmds[i].section + "/" + t.id().String()
		}
		return cmds[i].section + "/" + cmds[i].label
	}
	ui.List(c.Key("results"), l, len(cmds), func(i int) {
		cmd := cmds[i]
		ui.Column(c).Children(func() {
			if i == 0 || cmds[i-1].section != cmd.section {
				ui.Text(c, cmd.section).FontSize(11).FontWeight(600).TextColor(palette.faint).Padding(8, 10, 4)
			}
			r := row(c, "row", i == a.cmdkIndex).Padding(8, 10)
			if r.Hovered() {
				a.cmdkIndex = i
			}
			if r.Children(func() {
				if cmd.t != nil {
					ui.Box(c).Size(14, 14).Center().Shrink(0).Children(func() { a.statusMark(c, a.statusOf(*cmd.t)) })
				} else {
					ui.Icon(c, appIconNamed(cmd.icon)).FontSize(14)
				}
				ui.Text(c, cmd.label).SingleLine().Grow(1).Shrink(1)
				ui.Text(c, cmd.hint).FontSize(11.5).TextColor(palette.faint)
				if cmd.t != nil {
					ui.Text(c, ago(cmd.t.View.UpdatedAt, a.now)).FontSize(11.5).TextColor(palette.faint).MinWidth(28).TextAlign(ui.End)
				}
			}).Clicked() {
				a.cmdkOpen = false
				cmd.run()
			}
		})
	}).Height(paletteHeight(cmds)).Padding(6).Children(func() {
		if len(cmds) == 0 {
			ui.Text(c, "No results").TextColor(palette.muted).Padding(24).TextAlign(ui.Center)
		}
	})
	ui.Row(c).Gap(14).Padding(7, 16).AlignItems(ui.Center).BorderWidth(1, 0, 0, 0).BorderColor(palette.border).Children(func() {
		hint := func(key, what string) {
			ui.Row(c).Gap(5).AlignItems(ui.Center).Children(func() {
				kbd(c, key)
				ui.Text(c, what).FontSize(11).TextColor(palette.muted)
			})
		}
		hint("↑↓", "navigate")
		hint("↩", "open")
		hint("⌘↩", "open with diff")
		ui.Spacer(c)
		ui.Row(c).Gap(5).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "Type").FontSize(11).TextColor(palette.muted)
			kbd(c, ">")
			ui.Text(c, "for actions").FontSize(11).TextColor(palette.muted)
		})
	})
}

// paletteHeight is the results' height: as tall as they are, up to 380
// DIPs, past which they scroll.
func paletteHeight(cmds []command) float32 {
	if len(cmds) == 0 {
		return 72
	}
	h := float32(12)
	for i, cmd := range cmds {
		if i == 0 || cmds[i-1].section != cmd.section {
			h += 26
		}
		h += 34
	}
	return min(h, 380)
}

// alsoMatches is whether each word of the query is in the command's label
// or is one of its unshown words, whole, so that those don't put actions
// ahead of the threads a part of a word finds.
func alsoMatches(cmd command, query string) bool {
	words := strings.Fields(strings.ToLower(query))
	if cmd.also == "" || len(words) == 0 {
		return false
	}
	also := strings.Fields(cmd.also)
	for _, w := range words {
		if !slices.Contains(also, w) && !matches(cmd.label, w) {
			return false
		}
	}
	return true
}
