package main

import (
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/plugins"
)

// The sidebar is an inbox of threads: those that need you, those the agent
// is working on, those snoozed until later and those settled. Threads move
// between them as the agent works and as they're snoozed and settled.
type category int

const (
	catInbox category = iota
	catWorking
	catSnoozed
	catSettled
)

var categories = []struct {
	cat   category
	title string
	empty string
	icon  *ui.SVG
	color ui.Color
}{
	{catInbox, "Inbox", "Inbox zero. Nothing needs you.", icInbox, ui.Hex("#a78bfa")},
	{catWorking, "Working", "", icZap, ui.Hex("#60a5fa")},
	{catSnoozed, "Snoozed", "", icClock, ui.Hex("#fbbf24")},
	{catSettled, "Settled", "", icCheckCirc, ui.Hex("#4ade80")},
}

// Colors of the sidebar's icons and actions.
var (
	tintSnooze = ui.Hex("#fbbf24")
	tintSettle = ui.Hex("#4ade80")
	tintBack   = ui.Hex("#60a5fa")
)

// rowHeight is a thread row's height, the same whether it shows its time
// or, pointed at, its actions: pointing at it moves nothing.
const rowHeight = 32

// categoryOf is where a thread goes: working while the agent runs, whatever
// else it is; then snoozed until its time or settled, as the user left it;
// else the inbox.
func (a *app) categoryOf(t thread) category {
	d := disposition(t)
	switch {
	case a.statusOf(t) == statusWorking:
		return catWorking
	case d.State == api.Snoozed && a.now.Before(d.Until):
		return catSnoozed
	case d.State == api.Settled:
		return catSettled
	}
	return catInbox
}

// shownIn is the section a thread shows in: its own, but for a moment
// after an action, the one it left, while its joke plays there.
func (a *app) shownIn(t thread) category {
	if fx, ok := a.rowFx[t.id()]; ok && a.now.Sub(fx.at) < fx.hold {
		return fx.from
	}
	return a.categoryOf(t)
}

type inboxItem struct {
	t thread
	p *project
}

// sortedAt is what sortThreads sorted the threads after: the changes
// posted and those made here, the threads, and the changes to the project
// filter, the rows' jokes and the projects added.
type sortedAt struct {
	posts, edits, filter, fx, added uint64
	threads                         int
	recent                          *thread
}

// sortedThreads are the threads of each section, of the projects filtered
// to, every machine's together, the most recently updated first; at is
// what they were sorted after, and until when a snooze or a row's joke
// ends, which moves a thread with no change at all. index is each
// thread's place in its section, unread how many in the inbox are, and
// snoozed when the first snoozed thread, of any project, wakes.
type sortedThreads struct {
	at             sortedAt
	until, snoozed time.Time
	items          map[category][]inboxItem
	index          map[category]map[api.ID]int
	unread         int
}

// sortThreads sorts the threads into the sidebar's sections, with their
// projects and children, as a change moves them: not each frame, as with
// 50,000 threads that took most of one.
func (a *app) sortThreads() {
	recent := a.recent()
	at := sortedAt{posts: a.posts, edits: a.backend.Edits(), filter: a.filterEdits, fx: a.fxEdits, added: a.addedEdits, threads: len(recent)}
	if len(recent) > 0 {
		at.recent = &recent[0]
	}
	s := &a.sorted
	if s.items != nil && s.at == at && (s.until.IsZero() || a.now.Before(s.until)) {
		return
	}
	a.projects = a.gather()
	a.kids = a.kidsOf()
	// A project gone, as with its server, leaves the filter, lest it hide
	// every thread left.
	maps.DeleteFunc(a.projectFilter, func(k projectKey, _ bool) bool {
		return !slices.ContainsFunc(a.projects, func(p *project) bool { return p.key == k })
	})
	*s = sortedThreads{at: at, items: map[category][]inboxItem{}, index: map[category]map[api.ID]int{}}
	soon := func(t time.Time) {
		if t.After(a.now) && (s.until.IsZero() || t.Before(s.until)) {
			s.until = t
		}
	}
	for _, fx := range a.rowFx {
		soon(fx.at.Add(fx.hold))
	}
	shown := map[envProject]*project{}
	for _, p := range a.projects {
		if len(a.projectFilter) == 0 || a.projectFilter[p.key] {
			shown[envProject{p.env, p.key}] = p
		}
	}
	for _, t := range recent {
		if deleting(t) {
			continue
		}
		c, d := a.shownIn(t), disposition(t)
		if d.State == api.Snoozed {
			soon(d.Until)
		}
		// A row still playing its way out of Snoozed has no time.
		if c == catSnoozed && !d.Until.IsZero() && (s.snoozed.IsZero() || d.Until.Before(s.snoozed)) {
			s.snoozed = d.Until
		}
		p := shown[envProject{t.env, a.threadKey(t)}]
		// A child shows under its parent, but for one lifted into
		// Working.
		if p == nil || a.nested(t) && !a.lifted(t) {
			continue
		}
		if s.index[c] == nil {
			s.index[c] = map[api.ID]int{}
		}
		s.index[c][t.id()] = len(s.items[c])
		s.items[c] = append(s.items[c], inboxItem{t, p})
		if c == catInbox && a.backend.Unread(t.Thread) {
			s.unread++
		}
	}
}

// itemsIn returns the threads of a category, of the projects filtered to,
// every machine's together, the most recently updated first.
func (a *app) itemsIn(cat category) []inboxItem {
	a.sortThreads()
	return a.sorted.items[cat]
}

// lifted is whether t, a child, shows in Working on its own, as well as
// under its parent: its agent works while the thread at the top of its
// line is snoozed, where the child would be out of sight. It nests again
// once it stops or that thread is back in the inbox. Under a working
// thread lifted already, it shows in that one's rows instead. Settling a
// thread halts what works under it, so none is lifted out of Settled.
func (a *app) lifted(t thread) bool {
	if a.categoryOf(t) != catWorking {
		return false
	}
	up := a.ancestors(t)
	if len(up) == 0 {
		return false
	}
	// A working thread between them is lifted itself, with t in its rows.
	for _, x := range up[1:] {
		if a.categoryOf(x) == catWorking {
			return false
		}
	}
	return a.shownIn(up[0]) == catSnoozed
}

func (a *app) threadsNav(c *ui.Context) {
	ui.Row(c).Padding(0, 16, 6).Gap(6).AlignItems(ui.Center).Children(func() {
		ui.Image(c, appIcon).Size(18, 18)
		ui.Text(c, "Glitched").Bold().FontSize(13)
		ui.Text(c, "ALPHA").FontSize(9).Bold().LetterSpacing(0.6).TextColor(palette.muted).
			Padding(1, 5).Radius(4).Background(palette.hover)
	})
	ui.Column(c).Padding(0, 8, 4).Gap(1).Children(func() {
		if ghost(c, "new-thread", false).Padding(7, 8).Children(func() {
			ui.Icon(c, icSquarePen).FontSize(15)
			ui.Text(c, "New thread").Grow(1)
			shortcutHint(c, "⌘", "N")
		}).Clicked() {
			a.newThread()
		}
		if ghost(c, "search", false).Padding(7, 8).Children(func() {
			ui.Icon(c, icSearch).FontSize(15)
			ui.Text(c, "Search").Grow(1)
			shortcutHint(c, "⌘", "K")
		}).Clicked() {
			a.openCommandPalette()
		}
	})
	a.projectStrip(c)
	// What needs the user at the top, the inbox; what's out of their hands
	// at the bottom, over Settings, Working, Snoozed and Settled, each only
	// while it has threads.
	sections := func(cats ...category) {
		for _, s := range categories {
			for _, cat := range cats {
				if s.cat == cat {
					a.section(c, s.cat, s.title, s.empty, s.icon, s.color)
				}
			}
		}
	}
	body := []componentCopy{a.sidebarCopy(c, "inbox", 0, func() ui.Element {
		return ui.Column(c).Key("component:sidebar.body:inbox").Grow(1).Shrink(1).Basis(0)
	}, func() {
		ui.Scroll(c).Grow(1).Padding(4, 8, 8).Gap(10).Children(func() {
			sections(catInbox)
		})
	}, catInbox)}
	a.wakeSnoozed(c)
	if len(a.itemsIn(catWorking))+len(a.itemsIn(catSnoozed))+len(a.itemsIn(catSettled)) > 0 {
		// Settled may hold many: past half the sidebar, they scroll.
		body = append(body, a.sidebarCopy(c, "bottom", a.sidebarTall/2, func() ui.Element {
			return ui.Column(c).Key("component:sidebar.body:bottom").MaxHeightPercent(50).Shrink(0)
		}, func() {
			ui.Scroll(c).Key("bottom").Shrink(1).Padding(6, 8).Gap(6).Children(func() {
				sections(catWorking, catSnoozed, catSettled)
			})
		}, catWorking, catSnoozed, catSettled))
	} else {
		delete(a.sidebarRows, "bottom")
	}
	a.pluginComponent(c, "sidebar.body", nil, body...)
	ui.Column(c).Padding(8).Gap(1).BorderWidth(1, 0, 0, 0).BorderColor(palette.border).Children(func() {
		a.pluginSlot(c, "sidebar.footer", "", nil, slotAsk{})
		a.pluginNav(c)
		if ghost(c, "settings", false).Padding(7, 8).Children(func() {
			ui.Icon(c, icSettings).FontSize(15)
			ui.Text(c, "Settings").Grow(1)
			shortcutHint(c, "⌘", ",")
		}).Clicked() {
			a.router.Push("/settings/general")
		}
	})
}

// wakeSnoozed asks for a frame when the next snoozed thread's time comes,
// to bring it back to the inbox, and tells the plugins once it has: no
// change from the engine says so.
func (a *app) wakeSnoozed(c *ui.Context) {
	if !a.snoozeWake.IsZero() && !a.now.Before(a.snoozeWake) {
		a.snoozeWake = time.Time{}
		a.emitThreadChanges()
	}
	// Every project's, those filtered out of sight included, whose plugins
	// still hear them come back.
	a.sortThreads()
	next := a.sorted.snoozed
	if !next.IsZero() {
		c.After(next.Sub(a.now))
	}
	a.snoozeWake = next
}

// projectTile is a project's mark: its icon, white on its color.
func projectTile(c *ui.Context, p *project, size float32) ui.Element {
	icon, color := p.mark()
	return tile(c, icon, color, size)
}

// tile is a mark, a project's: the icon, white on the color.
func tile(c *ui.Context, icon *ui.SVG, color ui.Color, size float32) ui.Element {
	return ui.Box(c).Size(size, size).Radius(size * 0.28).Center().Shrink(0).
		LinearGradient(ui.LinearGradient{From: color.Mix(ui.Hex("#ffffff"), 0.1), To: color.Mix(ui.Hex("#000000"), 0.2), Angle: 135, Oklab: true}).
		Children(func() { ui.Icon(c, icon).FontSize(size * 0.6).TextColor(ui.Hex("#ffffff")) })
}

// projectStrip filters the inbox to some of the projects, a plugin's: the
// built-in draws All, a chip per project, and Add project.
func (a *app) projectStrip(c *ui.Context) {
	box := func() ui.Element { return ui.Column(c).Key("component:sidebar.projects") }
	strip := projectStrip{Projects: a.projectRecords(), Filtered: len(a.projectFilter) > 0, Picking: a.picking, Machines: len(a.backend.Envs()) > 1}
	a.pluginComponent(c, "sidebar.projects", nil, componentCopy{key: "strip", box: box, context: func(plugins.ThreadRecord) any { return strip }})
}

// section lists a category's threads under a header that opens and closes
// it, with their count. Its column is built even while empty, so that a
// thread moving into it folds in rather than appearing.
func (a *app) section(c *ui.Context, cat category, title, empty string, icon *ui.SVG, color ui.Color) {
	items := a.itemsIn(cat)
	if len(items) == 0 && cat != catInbox {
		return // Working, Snoozed and Settled show only while they have threads
	}
	// The inbox is always open: its header is a title, not a button.
	open := cat == catInbox || !a.sectionClosed[cat]
	ui.Column(c).Key(title).Gap(2).Children(func() {
		a.sectionHead(c, cat, len(items))
		ui.Column(c).Key("rows").Gap(2).Children(func() {
			if !open {
				return
			}
			if len(items) == 0 && empty != "" {
				ui.Text(c, empty).Key("empty").FontSize(12).TextColor(palette.faint).Padding(6, 10).Transition(fold)
			}
			for _, it := range items {
				a.inboxRow(c, it, cat)
				a.childRows(c, it.t, 1)
			}
		})
	})
}

// sectionHead is a section's header: its icon, title and count of threads,
// and, but for the inbox's, which is always open, a chevron; a click opens
// or closes it.
func (a *app) sectionHead(c *ui.Context, cat category, count int) {
	s := categories[cat]
	collapsible := cat != catInbox
	open := !collapsible || !a.sectionClosed[cat]
	var head ui.Element
	if collapsible {
		head = ghost(c, "head", false)
	} else {
		head = ui.Row(c).Key("head").AlignItems(ui.Center)
	}
	head.Height(30).Padding(0, 8).Gap(8).Children(func() {
		ui.Icon(c, s.icon).FontSize(14).TextColor(s.color)
		ui.Text(c, s.title).FontSize(12.5).FontWeight(600).TextColor(palette.text)
		if count > 0 {
			n := ui.Textf(c, "%d", count).FontSize(11).FontWeight(600).Padding(1, 7).Radius(999)
			if cat == catInbox && a.unreadCount() > 0 {
				n.Background(s.color.Alpha(0.2)).TextColor(s.color)
			} else {
				n.Background(palette.hover).TextColor(palette.muted)
			}
		}
		if collapsible {
			ui.Spacer(c)
			chevron(c, open).TextColor(palette.faint)
		}
	})
	if collapsible && head.Clicked() {
		a.sectionClosed[cat] = open
	}
}

func (a *app) unreadCount() int {
	a.sortThreads()
	return a.sorted.unread
}

// inboxRow is a thread: its project's tile, its title, bold while unread,
// and when it last changed; pointed at, the snooze and settle actions take
// the time's place. It folds in and out as it moves between sections.
func (a *app) inboxRow(c *ui.Context, it inboxItem, cat category) {
	t := it.t
	selected := t.id() == a.selected && a.router.Path() == "/"
	r := row(c, t.id(), selected).Height(rowHeight).Padding(0, 6, 0, 8).Gap(9).Transition(fold)
	if a.revealRow(t.id()) {
		r.ScrollIntoView()
	}
	if a.arriving(t) {
		r.Transition(arrive) // just sent from the hero, sliding in
	}
	if fx, playing := a.rowJoke(c, t.id()); playing {
		rowGag(r, fx)
	}
	hovered := r.Hovered()
	r.ContextMenu(func(m *ui.Menu) { a.threadMenu(c, m, t, cat) })
	if r.Children(func() {
		a.threadTile(c, t, it.p)
		if a.renaming == t.id() {
			a.renameInput(c, t)
			return
		}
		a.rowTitle(c, t)
		a.pluginSlot(c, "thread.row", t.id().String(), &t, slotAsk{})
		if len(a.backend.Envs()) > 1 {
			a.machineBadge(c, t.env)
		}
		// Its children's toggle, at the row's end, where pointing at the
		// row doesn't move it.
		if len(a.shown(t)) > 0 {
			defer a.childToggle(c, t)
		}
		// The actions show while it's pointed at, and while its snooze
		// popover is open, which takes the pointer away from the row.
		if hovered || a.snoozeFor == t.id() {
			a.rowActions(c, t, cat)
			return
		}
		a.rowMark(c, t, cat, selected)
	}).Clicked() {
		a.choose(t)
	}
}

// rowTitle is a row's thread's title, bold while unread; a child lifted
// into Working has the way to its parent under it.
func (a *app) rowTitle(c *ui.Context, t thread) {
	var name ui.Element
	if p, ok := a.parentOf(t); ok {
		ui.Column(c).Grow(1).Shrink(1).Children(func() {
			name = ui.Text(c, title(t)).FontSize(13).SingleLine()
			ui.Text(c, "under "+title(p)).FontSize(10.5).TextColor(palette.faint).SingleLine()
		})
	} else {
		name = ui.Text(c, title(t)).FontSize(13).SingleLine().Grow(1).Shrink(1)
	}
	if a.backend.Unread(t.Thread) {
		name.FontWeight(600).TextColor(palette.text)
	}
}

// rowMark is what a row, in the section cat, shows at its end while it
// isn't pointed at: a command failed, when a snoozed thread wakes, that
// it is unread, or how long ago it changed. The selected row's face is
// darker: its faint text steps up.
func (a *app) rowMark(c *ui.Context, t thread, cat category, selected bool) {
	dim := palette.faint
	if selected {
		dim = palette.muted
	}
	switch {
	case failed(t):
		a.tip(c, ui.Box(c).Size(7, 7).Radius(4).Background(palette.red).Label("A command failed"), "A command failed")
	case cat == catSnoozed:
		ui.Row(c).Gap(3).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, icClock).FontSize(10).TextColor(dim)
			ui.Text(c, snoozeLabel(disposition(t).Until, a.now)).FontSize(11).TextColor(dim)
		})
	case a.backend.Unread(t.Thread):
		ui.Box(c).Size(7, 7).Radius(4).Background(palette.accent)
	default:
		ui.Text(c, ago(t.View.UpdatedAt, a.now)).FontSize(11).TextColor(dim)
	}
}

// rowJoke is the joke playing on the row of the thread id, if one is; one
// done is over.
func (a *app) rowJoke(c *ui.Context, id api.ID) (rowGagFx, bool) {
	fx, playing := a.rowFx[id]
	if playing && fx.done(c.Now()) {
		delete(a.rowFx, id)
		return fx, false
	}
	return fx, playing
}

// threadTile is the tile of t's project, p, with t's state as a badge on
// it, and the joke playing on t's row.
func (a *app) threadTile(c *ui.Context, t thread, p *project) {
	fx, playing := a.rowJoke(c, t.id())
	wrap := ui.Box(c).Children(func() {
		tile := projectTile(c, p, 20)
		if playing {
			tileGag(tile, fx)
		}
		// The thread's state, as a badge on the project's tile.
		if mark := a.statusBadge(c, a.statusOf(t)); mark.Valid() {
			mark.Absolute().Right(-3).Bottom(-3)
		}
		if playing {
			floatGag(c, fx)
		}
	})
	if playing && fx.kind == gagSettle {
		burstGag(wrap, fx)
	}
}

// statusBadge is a small mark of what a thread is doing, on its project's
// tile: the agent working, or waiting for an approval.
func (a *app) statusBadge(c *ui.Context, s status) ui.Element {
	switch s {
	case statusWorking:
		return ui.Box(c).Size(11, 11).Radius(6).Center().Background(palette.sidebar).Children(func() {
			a.statusMark(c, statusWorking)
		})
	case statusApproval:
		return a.tip(c, ui.Box(c).Size(9, 9).Radius(5).Background(palette.amber).Border(2, palette.sidebar), "Waits on you")
	}
	return ui.Element{}
}

var snoozeTimes = []struct {
	when, joke, emoji string
	until             func(now time.Time) time.Time
}{
	{"Later today", "a power nap", "☕", func(now time.Time) time.Time { return now.Add(3 * time.Hour).Truncate(time.Minute) }},
	{"Tomorrow", "sleep on it", "🌙", func(now time.Time) time.Time { return morning(now, 1) }},
	{"Next week", "a long vacation", "🏝", func(now time.Time) time.Time {
		return morning(now, 7-(int(now.Weekday())+6)%7) // Monday's
	}},
}

// morning is 9:00, days after now's day.
func morning(now time.Time, days int) time.Time {
	y, m, d := now.Date()
	return time.Date(y, m, d+days, 9, 0, 0, 0, now.Location())
}

func lower(s string) string {
	if s == "Later today" {
		return "later today"
	}
	if s == "" {
		return s
	}
	return fmt.Sprintf("%c%s", s[0]|0x20, s[1:])
}

func (a *app) snooze(c *ui.Context, t thread, when string, until time.Time, from category) {
	a.backend.SetDisposition(t.env, t.id(), api.Disposition{State: api.Snoozed, Until: until})
	a.snoozeFor = api.ID{}
	a.gag(c, t.id(), gagSnooze, from)
	c.AddToast(ui.Toast{Title: "Sleeping until " + lower(when) + " 💤", Description: t.View.Title, Type: "info"})
}
