package main

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"

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
	a.cmdkOpen, a.cmdkQuery, a.cmdkIndex = true, "", 0
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
	for _, p := range a.projects {
		cmds = append(cmds, action("thread.new:"+p.key.id(), "New thread in "+a.projectLabel(p), "", "pen-new-square", func() { a.startThread(p) }))
	}
	if !a.picking {
		add := action("project.add", "Add project…", "", "folder-open", a.addProject)
		add.also = "workspace folder"
		cmds = append(cmds, add)
	}
	// Each is listed while an agent works in it too, lest the rows shift
	// under the one chosen as a run starts; its alert says to stop it.
	for _, p := range a.projects {
		rm := action("project.remove:"+p.key.id(), "Remove project "+a.projectLabel(p)+"…", "", "folder", func() { a.askRemove(p) })
		rm.also = "workspace delete"
		cmds = append(cmds, rm)
	}
	cmds = append(cmds,
		action("model.switch", "Switch model", "", "sparkles", a.openModelPicker),
		action("diff.toggle", "Toggle diff panel", "⌘D", "pull-request", func() { a.showDiff = !a.showDiff }),
		action("terminal.toggle", "Toggle terminal", "⌘J", "terminal", func() { a.showTerminal = !a.showTerminal }),
		action("preview.toggle", "Toggle preview", "⇧⌘P", "global", a.togglePreview),
		action("browser.newTab", "New browser tab", "⌘T", "plus", func() { a.newTab("") }),
		action("sidebar.toggle", "Toggle sidebar", "⌘B", "sidebar-minimalistic", func() { a.showSidebar = !a.showSidebar }),
		action("theme.toggle", "Toggle light / dark", "⇧⌘L", "pallete-2", a.toggleLightDark),
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
	return shown
}

// threadCommands are the palette's threads, the most recently active
// first.
func (a *app) threadCommands() []command {
	var threads []command
	for _, p := range a.projects {
		for _, t := range p.threads {
			threads = append(threads, command{section: "Threads", label: title(t), hint: p.name(), run: func() { a.select_(t) }, t: &t})
		}
	}
	slices.SortStableFunc(threads, func(x, y command) int { return newestFirst(*x.t, *y.t) })
	return threads
}

// paletteContext is the palette component's context: the query it opened
// with, and what it finds, which it filters as the query changes: the
// actions, then the threads, the most recently active first.
type paletteContext struct {
	Query   string          `json:"query"`
	Actions []commandRecord `json:"actions"`
	Threads []paletteThread `json:"threads"`
}

type paletteThread struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Project string `json:"project"`
	// Ago is how long ago it was last active, as its row says it.
	Ago string `json:"ago"`
}

func (a *app) paletteContext() paletteContext {
	ctx := paletteContext{Query: a.cmdkQuery, Actions: a.commandRecords(), Threads: []paletteThread{}}
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
	a.composerOpened, a.pickerOpen = true, true
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
		ctx := a.paletteContext()
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
			return t.id()
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
