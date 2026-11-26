package main

import (
	"cmp"
	"fmt"

	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/plugins"
)

func (a *app) header(c *ui.Context, t thread, files []fileChange) {
	bar := c.TitleBar()
	// Clear of the traffic lights as the sidebar slides away from them.
	left := 16 + bar.Left*(1-a.sidebarOpen)
	// Narrow, beside a panel, the header keeps to icons, and never draws
	// past the chat's edge.
	chat := a.chatRoom()
	narrow := chat < 700
	// Clear of the window buttons on the right (Windows, Linux) unless a
	// panel takes that edge.
	right := float32(12)
	if _, _, sub := a.subagentShown(t); !a.showDiff && !a.showPreview && !sub && !a.managerPanelShown(t) {
		right += bar.Right
	}
	head := ui.Row(c).Height(max(bar.Height, headerHeight)).Shrink(0).
		Padding(0, right, 0, left).Gap(headerGap).AlignItems(ui.Center).ClipX()
	head.DragWindow().Children(func() {
		if !a.sidebarVisible() {
			if a.iconButton(c, "show-sidebar", icPanelLeft, "Show sidebar  ⌘B", false).Clicked() {
				a.revealSidebar(false)
			}
		}
		project, branch := a.projectOf(t).name(), t.View.Branch
		machine := len(a.backend.Envs()) > 1 && t.env != nil
		text := func(s string, size float32) float32 {
			w, _ := c.MeasureText(0, ui.Span{Text: s, Size: size})
			return w
		}
		pills := headerPills{project: text(project, 11) + 2*pillPadX + 2 + headerGap}
		if machine {
			pills.project += text(t.env.Name, 10.5) + machinePillChrome + headerGap
		}
		if branch != "" {
			pills.branch = text(branch, 11) + 2*pillPadX + 2 + headerGap
		}
		if !a.sidebarVisible() {
			pills.sidebar = headerIconW + headerGap
		}
		manager := isManager(t)
		needs, active := 0, 0
		if manager {
			needs, active = a.managerAttention(t)
			pills.manager = text("Manager", 11) + headerPillIcon + 2*pillPadX + 2 + headerGap
			pills.sweep = text(sweepLabel(effectiveSweep(t)), 11) + 2*pillPadX + 2 + headerGap
			pills.overview = text("Overview", 11) + headerPillIcon + 2*pillPadX + 2 + headerGap
			if n := cmp.Or(needs, active); n > 0 {
				pills.overview += text(fmt.Sprint(n), 10.5) + 4
			}
		}
		natural, _ := c.MeasureText(0, ui.Span{Text: title(t), Weight: 600})
		g := &a.headerGive
		give := g.choose(t.id(), narrow, chat-left-right, min(natural, headerTitleRoom), pills)
		w := g.widths()
		// The title, and a child's way back up before it.
		heading := a.heading(c, t, narrow)
		if give < giveProject {
			pill(c, project).Key("header-project").Shrink(0)
		}
		if give < giveBranch && branch != "" {
			pill(c, branch).Key("header-branch").Shrink(0)
		}
		if give < giveProject && machine {
			a.machinePill(c, t.env)
		}
		if !narrow {
			a.usagePill(c, t)
		}
		if manager && give < giveProject {
			ui.Row(c).Key("header-manager").Shrink(0).Gap(4).AlignItems(ui.Center).Padding(2, pillPadX).Radius(999).
				Background(palette.accent.Alpha(0.14)).Border(1, palette.accent.Alpha(0.28)).Children(func() {
				ui.Icon(c, icManager).FontSize(11).TextColor(palette.accent)
				ui.Text(c, "Manager").FontSize(11).TextColor(palette.accent).NoWrap()
			})
		}
		if manager && give < givePlan {
			open := a.mgr.sweepPopover == t.id()
			label := sweepLabel(effectiveSweep(t))
			sweep := ui.ButtonBase(c.Key("header-sweep")).Padding(2, pillPadX).Radius(999).
				Border(1, palette.border).Shrink(0).Cursor(ui.CursorPointer).Label(label)
			if open || sweep.Hovered() {
				sweep.Background(palette.active)
			} else {
				sweep.Background(palette.hover)
			}
			if sweep.Children(func() { ui.Text(c, label).FontSize(11).TextColor(palette.muted).NoWrap() }).Clicked() {
				a.toggleSweepPopover(t)
			}
			a.sweepPopover(c, t, sweep)
		} else if manager && a.mgr.sweepPopover == t.id() {
			a.mgr.sweepPopover = api.ID{} // its button gave way
		}
		if manager && give < givePlan {
			a.overviewButton(c, t, needs, active)
		}
		drew := headerDrew{thread: t.id(), narrow: narrow, step: give, left: left, right: right, pills: pills}
		drew.plan = t.Transcript != nil && t.Transcript.Plan != nil
		drew.planV = b2i(give >= giveCompactPlan)
		var plan *ui.Rect
		if drew.plan && give < givePlan {
			drew.planShown = w.plan[drew.planV] >= 0
			at := a.planPill(c, t, drew.planV == 1, drew.planShown).Bounds()
			plan = &at
		} else {
			// No copy is drawn: the plugin hears its copy went.
			a.pluginComponent(c, "thread.header.plan", &t)
		}
		var slot float32
		switch {
		case t.id().IsZero():
		case give < givePlaces:
			drew.slotV = min(give/giveCompactPlaces, 1)
			slot, drew.slotDrawn, drew.slotShown = a.pluginSlot(c, "thread.header", t.id().String(), &t, drew.slotV == 1, w.slot[drew.slotV] >= 0)
		default:
			// Its copies are left out: the plugins hear they went. What
			// the last frame drew of them is what it last measured.
			a.frameOf("thread.header")
			if g.drew.step < givePlaces {
				slot = max(w.slot[g.drew.slotV], 0)
			}
		}
		spacer := ui.Spacer(c).Key("header-spacer")
		a.tabButtons(c)
		// The rest of the row is a plugin's: the built-in Header actions,
		// or one of the user's that replaces it.
		state := headerActions{Local: t.local(), Narrow: give >= giveActions, Panels: a.panels(), ChangedFiles: len(files)}
		cp := componentCopy{key: t.id().String(), context: func(r plugins.ThreadRecord) any {
			state.Thread = r
			return state
		}}
		drew.actionsV = min(give, giveActions)
		drew.actionsDrawn = a.headerActionsDrawn(cp.key, state.Narrow)
		drew.actionsShown = drew.actionsDrawn && w.actions[drew.actionsV] >= 0
		var actions ui.Element
		cp.box = func() ui.Element {
			actions = ui.Row(c).Key("component:thread.header.actions").Shrink(0).AlignItems(ui.Center)
			// Drawn for another width, or at one not measured yet, they
			// are measured out of the row, which never holds more than
			// fits it.
			if !drew.actionsShown {
				actions = actions.Absolute().Invisible()
			}
			return actions
		}
		a.pluginComponent(c, "thread.header.actions", &t, cp)
		// What this frame lays out but the header hasn't measured, the next
		// frame measures, and draws with: the window may not ask for one.
		unmeasured := w.base[b2i(narrow)] < 0 || drew.actionsDrawn && !drew.actionsShown || drew.slotDrawn && !drew.slotShown ||
			drew.plan && give < givePlan && !drew.planShown
		if g.measure(head.Bounds(), heading.Bounds().W, spacer.Bounds().W, actions.Bounds(), slot, plan) || unmeasured {
			c.Invalidate()
		}
		g.drew = drew
	})
}

// chatRoom is the chat's width in this frame: the room beside the
// sidebar, less the panel's beside it.
func (a *app) chatRoom() float32 {
	w := a.roomBeside
	switch {
	case a.showDiff:
		w -= a.diffWidth()
	case a.showPreview:
		w -= a.previewWidth()
	case a.showPluginTab:
		w -= a.diffWidth()
	case a.managerPanelShown(a.current()):
		w -= a.diffWidth()
	default:
		if _, _, ok := a.subagentShown(a.current()); ok {
			w -= a.diffWidth()
		}
	}
	return max(w, minChatWidth)
}

// headerTitleRoom is the most of its title's width the header keeps
// before it gives anything else way.
const headerTitleRoom = 160

func sweepLabel(s *api.SweepSchedule) string {
	if s == nil || s.IntervalSeconds <= 0 {
		return "Sweep off"
	}
	switch secs := s.IntervalSeconds; {
	case secs%3600 == 0:
		return fmt.Sprintf("Sweep %dh", secs/3600)
	case secs%60 == 0:
		return fmt.Sprintf("Sweep %dm", secs/60)
	default:
		return fmt.Sprintf("Sweep %ds", secs)
	}
}

// The header's gap between its controls, and a pill's padding at its sides.
const (
	headerGap = 8
	pillPadX  = 7
)

// headerPillIcon is what an icon adds to a pill: the icon and its gap;
// headerIconW is an icon button's width, its icon and its padding, and
// machinePillChrome what a machine's pill adds to its name: its dot, the
// dot's gap, its padding and its border.
const (
	headerPillIcon    = 11 + 4
	headerIconW       = 15 + 2*6
	machinePillChrome = 5 + 4 + 2*6 + 2
)

// What the header gives way, in order, while its row doesn't fit: the
// actions to their icons, the plan's pill to its count, the project's and
// the machine's pills, the branch's, the plugins' copies to their
// smallest, the plan's pill with a Manager's sweep and overview, then
// those copies. Under 700 points it starts with the pills gone, the
// actions as icons and the plan as its count.
const (
	giveNone = iota
	giveActions
	giveCompactPlan
	giveProject
	giveBranch
	giveCompactPlaces
	givePlan
	givePlaces
)

// headerPills are the widths, gaps included, of what the header draws
// itself and its steps take away, the project's pill with the machine's
// and the branch's, and of the sidebar's button.
type headerPills struct{ project, branch, sidebar, manager, sweep, overview float32 }

// headerGive is what the header gives way. It picks the step in the frame
// that draws it, from the chat's width in that frame and from what it has
// measured of each thread's row, so that no frame lays the row out past
// the window or one control under another. A plugin's drawing it hasn't
// measured at its width is drawn out of the row and measured there.
type headerGive struct {
	thread api.ID
	step   int
	seen   map[api.ID]*headerWidths
	// drew is what the last frame drew, which this frame's bounds lay out.
	drew headerDrew
}

// headerWidths are what the header measured of a thread's row, -1 for
// what it hasn't: base, the row but its title, pills, copies, plan and
// actions, narrow and not; the slot's copies full and smallest, gaps
// included; the plan's pill with its step and as its count, and the
// actions with their labels and as icons, each with the gap before it.
type headerWidths struct {
	base    [2]float32
	slot    [2]float32
	plan    [2]float32
	actions [2]float32
}

// headerDrew is what a frame drew of the header: the copies, the plan and
// the actions at which of their widths, whether drawn for it, and whether
// in the row; and whether the thread has a plan.
type headerDrew struct {
	thread                     api.ID
	narrow                     bool
	step                       int
	left, right                float32
	pills                      headerPills
	slotV, planV, actionsV     int
	slotDrawn, slotShown       bool
	plan, planShown            bool
	actionsDrawn, actionsShown bool
}

func (g *headerGive) widths() *headerWidths { return g.seen[g.thread] }

// row is how wide the header's row is at step k but its title, as far as
// it knows: a drawing it hasn't measured is drawn out of the row.
func (w *headerWidths) row(k int, narrow bool, p headerPills) float32 {
	r := max(w.base[b2i(narrow)], 0) + p.sidebar
	if k < giveProject {
		r += p.project + p.manager
	}
	if k < giveBranch {
		r += p.branch
	}
	if k < givePlaces {
		r += max(w.slot[min(k/giveCompactPlaces, 1)], 0)
	}
	if k < givePlan {
		r += max(w.plan[b2i(k >= giveCompactPlan)], 0) + p.sweep + p.overview
	}
	return r + max(w.actions[min(k, giveActions)], 0)
}

// choose is the step the header of thread draws at in room points, with
// title as much as its title keeps: the least it gives way and fits. It
// takes a step back only with room to spare, and under 700 points
// (narrow) gives the pills and the actions' labels way whatever the room.
// What it hasn't measured it counts as nothing: that is drawn out of the
// row till it has.
func (g *headerGive) choose(thread api.ID, narrow bool, room, title float32, p headerPills) int {
	if g.thread != thread {
		g.thread, g.step = thread, giveNone
	}
	if g.seen[thread] == nil {
		if len(g.seen) >= 64 || g.seen == nil {
			g.seen = map[api.ID]*headerWidths{}
		}
		g.seen[thread] = &headerWidths{base: [2]float32{-1, -1}, slot: [2]float32{-1, -1}, plan: [2]float32{-1, -1}, actions: [2]float32{-1, -1}}
	}
	w := g.seen[thread]
	least := giveNone
	if narrow {
		least = giveBranch
	}
	k := givePlaces
	for s := least; s < givePlaces; s++ {
		spare := float32(0)
		if s < g.step {
			spare = 16
		}
		if w.row(s, narrow, p)+title+spare <= room {
			k = s
			break
		}
	}
	g.step = k
	return k
}

// measure keeps what the last frame's layout says of the row it drew: h,
// the row's bounds, title and spacer the widths of its title and of its
// spacer, actions the actions' bounds, slot the slot's copies' width and
// plan the plan's pill's bounds, nil if this frame left it out. It says
// whether that changed what the header knows. A thread's plan gone, it
// forgets the pill's widths, which the next plan's needn't share.
func (g *headerGive) measure(h ui.Rect, title, spacer float32, actions ui.Rect, slot float32, plan *ui.Rect) bool {
	d := g.drew
	w := g.seen[d.thread]
	if w == nil || d.thread != g.thread || h.W <= 0 {
		return false
	}
	changed := false
	set := func(at *float32, v float32) {
		if *at != v {
			*at, changed = v, true
		}
	}
	if d.actionsDrawn {
		set(&w.actions[d.actionsV], actions.W+headerGap)
	}
	if d.slotDrawn && d.step < givePlaces {
		set(&w.slot[d.slotV], slot)
	}
	switch {
	case !d.plan:
		set(&w.plan[0], -1)
		set(&w.plan[1], -1)
	case d.step < givePlan && plan != nil:
		set(&w.plan[d.planV], plan.W+headerGap)
	}
	end := h.X + h.W - d.right
	if d.actionsShown {
		end = max(end, actions.X+actions.W)
	}
	base := end - h.X - d.left - title - spacer - d.pills.sidebar
	if d.step < giveProject {
		base -= d.pills.project + d.pills.manager
	}
	if d.step < giveBranch {
		base -= d.pills.branch
	}
	if d.slotShown && d.step < givePlaces {
		base -= slot
	}
	if d.planShown {
		base -= w.plan[d.planV]
	}
	if d.step < givePlan {
		base -= d.pills.sweep + d.pills.overview
	}
	if d.actionsShown {
		base -= actions.W + headerGap
	}
	set(&w.base[b2i(d.narrow)], max(base, 0))
	return changed
}

// headerButton is a button of the header's glass bar: a hairline pill,
// over the window's vibrancy rather than glass over the chat.
func headerButton(c *ui.Context, key any) ui.Element {
	return ghost(c, key, false).Padding(5, 11).Radius(999).Border(1, palette.border).TextColor(palette.text)
}

// glassButton is a pill of interactive glass, which swells as it's pressed.
func (a *app) glassButton(c *ui.Context, key any) ui.Element {
	b := ui.ButtonBase(c.Key(key)).Gap(6).AlignItems(ui.Center).Padding(6, 12).Radius(999).
		TextColor(palette.text).Cursor(ui.CursorPointer)
	return a.pane(b, glass.Glass{Interactive: true})
}

func pill(c *ui.Context, label string) ui.Element {
	return ui.Text(c, label).FontSize(11).TextColor(palette.muted).SingleLine().
		Padding(2, pillPadX).Radius(999).Border(1, palette.border)
}

// overviewButton opens and closes the Manager t's overview panel: a count
// of what waits on the user, in amber, or else of the workers working.
func (a *app) overviewButton(c *ui.Context, t thread, needs, active int) {
	on := a.managerPanelShown(t)
	b := ui.ButtonBase(c.Key("header-overview")).Padding(2, pillPadX).Radius(999).Gap(4).AlignItems(ui.Center).
		Border(1, palette.border).Shrink(0).Cursor(ui.CursorPointer).Label("Overview")
	switch {
	case on:
		b.Background(palette.active)
	case b.Hovered():
		b.Background(palette.hover)
	}
	if b.Children(func() {
		ui.Icon(c, icManager).FontSize(11).TextColor(palette.accent)
		ui.Text(c, "Overview").FontSize(11).TextColor(palette.muted).NoWrap()
		switch {
		case needs > 0:
			ui.Textf(c, "%d", needs).FontSize(10.5).FontWeight(600).TextColor(palette.amber).NoWrap()
		case active > 0:
			ui.Textf(c, "%d", active).FontSize(10.5).FontWeight(600).TextColor(palette.blue).NoWrap()
		}
	}).Clicked() {
		a.toggleOverview(t)
	}
}
