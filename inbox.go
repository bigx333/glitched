package main

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
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

// itemsIn returns the threads of a category, of the project filtered to.
func (a *app) itemsIn(cat category) []inboxItem {
	var items []inboxItem
	for _, p := range a.projects {
		if len(a.projectFilter) > 0 && !a.projectFilter[p.key] {
			continue
		}
		for _, t := range p.threads {
			if a.shownIn(t) == cat {
				items = append(items, inboxItem{t, p})
			}
		}
	}
	return items
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
	ui.Scroll(c).Grow(1).Padding(4, 8, 8).Gap(10).Children(func() {
		sections(catInbox)
	})
	a.wakeSnoozed(c)
	if len(a.itemsIn(catWorking))+len(a.itemsIn(catSnoozed))+len(a.itemsIn(catSettled)) > 0 {
		// Settled may hold many: past half the sidebar, they scroll.
		ui.Scroll(c).Key("bottom").MaxHeightPercent(50).Shrink(0).Padding(6, 8).Gap(6).Children(func() {
			sections(catWorking, catSnoozed, catSettled)
		})
	}
	ui.Column(c).Padding(8).BorderWidth(1, 0, 0, 0).BorderColor(palette.border).Children(func() {
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
// to bring it back to the inbox.
func (a *app) wakeSnoozed(c *ui.Context) {
	var next time.Time
	for _, it := range a.itemsIn(catSnoozed) {
		// A row still playing its way out of Snoozed has no time.
		if u := disposition(it.t).Until; !u.IsZero() && (next.IsZero() || u.Before(next)) {
			next = u
		}
	}
	if !next.IsZero() {
		c.After(next.Sub(a.now))
	}
}

// projectTile is a project's mark: its icon, white on its color.
func projectTile(c *ui.Context, p *project, size float32) ui.Element {
	icon, color := p.mark()
	return ui.Box(c).Size(size, size).Radius(size * 0.28).Center().Shrink(0).
		LinearGradient(ui.LinearGradient{From: color.Mix(ui.Hex("#ffffff"), 0.1), To: color.Mix(ui.Hex("#000000"), 0.2), Angle: 135, Oklab: true}).
		Children(func() { ui.Icon(c, icon).FontSize(size * 0.6).TextColor(ui.Hex("#ffffff")) })
}

// projectStrip filters the inbox to some of the projects: each project's
// tile toggles it in or out, All shows them all again, as does choosing
// every one; then a button adding a project.
func (a *app) projectStrip(c *ui.Context) {
	filtered := len(a.projectFilter) > 0
	ui.Row(c).Padding(6, 12, 6, 14).Gap(4).AlignItems(ui.Center).Children(func() {
		all := ghost(c, "all", !filtered).Padding(4, 8).Tooltip("All projects")
		if all.Children(func() { ui.Text(c, "All").FontSize(11.5).FontWeight(500) }).Clicked() {
			clear(a.projectFilter)
		}
		for _, p := range a.projects {
			on := a.projectFilter[p.key]
			name := p.name()
			if len(a.backend.Envs()) > 1 {
				name += " on " + p.env.Name
			}
			tip := "Show " + name
			if on {
				tip = "Hide " + name
			}
			b := ghost(c, p.key, on).Padding(3).Label(p.name()).Tooltip(tip)
			if filtered && !on {
				b.Opacity(0.45)
			}
			if b.Children(func() { projectTile(c, p, 20) }).Clicked() {
				a.toggleProject(p.key)
			}
		}
		ui.Spacer(c)
		iconButton(c, "add-project", icPlus, "Add project", false).Padding(4)
	})
}

// toggleProject puts a project in the sidebar's filter or takes it out;
// a filter of every project, or of none, is no filter.
func (a *app) toggleProject(id projectKey) {
	if a.projectFilter[id] {
		delete(a.projectFilter, id)
	} else {
		a.projectFilter[id] = true
	}
	if len(a.projectFilter) == len(a.projects) {
		clear(a.projectFilter)
	}
}

// section lists a category's threads under a header that opens and closes
// it, with their count. Its column is built even while empty, so that a
// thread moving into it folds in rather than appearing.
func (a *app) section(c *ui.Context, cat category, title, empty string, icon *ui.SVG, color ui.Color) {
	items := a.itemsIn(cat)
	open := !a.sectionClosed[cat]
	if len(items) == 0 && cat != catInbox {
		return // Working, Snoozed and Settled show only while they have threads
	}
	// The inbox is always open: its header is a title, not a button.
	collapsible := cat != catInbox
	if !collapsible {
		open = true
	}
	ui.Column(c).Key(title).Gap(2).Children(func() {
		var head ui.Element
		if collapsible {
			head = ghost(c, "head", false)
		} else {
			head = ui.Row(c).Key("head").AlignItems(ui.Center)
		}
		head.Height(30).Padding(0, 8).Gap(8).Children(func() {
			ui.Icon(c, icon).FontSize(14).TextColor(color)
			ui.Text(c, title).FontSize(12.5).FontWeight(600).TextColor(palette.text)
			if len(items) > 0 {
				count := ui.Textf(c, "%d", len(items)).FontSize(11).FontWeight(600).Padding(1, 7).Radius(999)
				if cat == catInbox && a.unreadCount() > 0 {
					count.Background(color.Alpha(0.2)).TextColor(color)
				} else {
					count.Background(palette.hover).TextColor(palette.muted)
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
		ui.Column(c).Key("rows").Gap(2).Children(func() {
			if !open {
				return
			}
			if len(items) == 0 && empty != "" {
				ui.Text(c, empty).Key("empty").FontSize(12).TextColor(palette.faint).Padding(6, 10).Transition(fold)
			}
			for _, it := range items {
				a.inboxRow(c, it, cat)
			}
		})
	})
}

func (a *app) unreadCount() int {
	n := 0
	for _, it := range a.itemsIn(catInbox) {
		if a.backend.Unread(it.t.Thread) {
			n++
		}
	}
	return n
}

// inboxRow is a thread: its project's tile, its title, bold while unread,
// and when it last changed; pointed at, the snooze and settle actions take
// the time's place. It folds in and out as it moves between sections.
func (a *app) inboxRow(c *ui.Context, it inboxItem, cat category) {
	t := it.t
	selected := t.id() == a.selected && a.router.Path() == "/"
	unread := a.backend.Unread(t.Thread)
	r := row(c, t.id(), selected).Height(rowHeight).Padding(0, 6, 0, 8).Gap(9).Transition(fold)
	fx, playing := a.rowFx[t.id()]
	if playing && fx.done(c.Now()) {
		delete(a.rowFx, t.id())
		playing = false
	}
	if playing {
		rowGag(r, fx)
	}
	hovered := r.Hovered()
	if r.Children(func() {
		wrap := ui.Box(c).Children(func() {
			tile := projectTile(c, it.p, 20)
			if playing {
				tileGag(tile, fx)
			}
			// The thread's state, as a badge on the project's tile.
			if mark := statusBadge(c, a.statusOf(t)); mark.Valid() {
				mark.Absolute().Right(-3).Bottom(-3)
			}
			if playing {
				floatGag(c, fx)
			}
		})
		if playing && fx.kind == gagSettle {
			burstGag(wrap, fx)
		}
		title := ui.Text(c, t.View.Title).FontSize(13).SingleLine().Grow(1).Shrink(1)
		if unread {
			title.FontWeight(600).TextColor(palette.text)
		}
		if len(a.backend.Envs()) > 1 {
			a.machineBadge(c, t.env)
		}
		// The actions show while it's pointed at, and while its snooze
		// popover is open, which takes the pointer away from the row.
		if hovered || a.snoozeFor == t.id() {
			a.rowActions(c, t, cat)
			return
		}
		switch {
		case failed(t):
			ui.Box(c).Size(7, 7).Radius(4).Background(palette.red).Label("A command failed").Tooltip("A command failed")
		case cat == catSnoozed:
			ui.Row(c).Gap(3).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, icClock).FontSize(10).TextColor(palette.faint)
				ui.Text(c, snoozeLabel(disposition(t).Until, a.now)).FontSize(11).TextColor(palette.faint)
			})
		case unread:
			ui.Box(c).Size(7, 7).Radius(4).Background(palette.accent)
		default:
			ui.Text(c, ago(t.View.UpdatedAt, a.now)).FontSize(11).TextColor(palette.faint)
		}
	}).Clicked() {
		a.select_(t)
	}
}

// statusBadge is a small mark of what a thread is doing, on its project's
// tile: the agent working, or waiting for an approval.
func statusBadge(c *ui.Context, s status) ui.Element {
	switch s {
	case statusWorking:
		return ui.Box(c).Size(11, 11).Radius(6).Center().Background(palette.sidebar).Children(func() {
			statusMark(c, statusWorking)
		})
	case statusApproval:
		return ui.Box(c).Size(9, 9).Radius(5).Background(palette.amber).Border(2, palette.sidebar).Tooltip("Pending approval")
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

// rowActions are a pointed-at thread's actions: snooze it until a time,
// and settle it; a settled or snoozed thread goes back to the inbox
// instead.
func (a *app) rowActions(c *ui.Context, t thread, cat category) {
	ui.Row(c).Gap(0).Children(func() {
		switch cat {
		case catSettled, catSnoozed:
			if iconButton(c, "back", icUndo, "Back to inbox", false).Padding(3).TextColor(tintBack).Clicked() {
				a.backend.SetDisposition(t.env, t.id(), api.Disposition{})
				a.gag(c, t.id(), gagUndo, cat)
			}
		case catInbox:
			a.snoozeButton(c, t, cat)
			if iconButton(c, "settle", icCheckCirc, "Settle", false).Padding(3).TextColor(tintSettle).Clicked() {
				a.backend.SetDisposition(t.env, t.id(), api.Disposition{State: api.Settled})
				a.markRead(t)
				a.gag(c, t.id(), gagSettle, cat)
				c.AddToast(ui.Toast{Title: "Settled. Phew.", Description: t.View.Title})
			}
		}
	})
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

// snoozeButton opens a popover of when to snooze the thread until, each
// with a joke; a native menu would take the pointer from the row, which
// stops building its actions, and lose the choice.
func (a *app) snoozeButton(c *ui.Context, t thread, cat category) {
	open := a.snoozeFor == t.id()
	b := iconButton(c, "snooze", icClock, "Snooze", open).Padding(3).TextColor(tintSnooze)
	if b.Clicked() {
		if open {
			a.snoozeFor = api.ID{}
		} else {
			a.snoozeFor = t.id()
		}
		open = !open
	}
	ui.PopoverBase(c, b, &open, func(panel ui.Element) {
		panel.AttachTo(b, ui.AnchorTopEnd, ui.AnchorBottomEnd).Margin(0, 0, 6, 0).Width(220).Radius(12).Clip()
		a.pane(panel, glass.Glass{}).Shadow(0, 12, 32, 0, ui.RGBA(0, 0, 0, 0.35)).Children(func() {
			ui.Column(c).Padding(5).Gap(1).Children(func() {
				ui.Text(c, "Snooze until…").FontSize(11).FontWeight(600).TextColor(palette.faint).Padding(4, 8, 3)
				for _, s := range snoozeTimes {
					if row(c, s.when, false).Padding(6, 8).Gap(10).Children(func() {
						ui.Text(c, s.emoji).FontSize(15)
						ui.Column(c).Grow(1).Gap(1).Children(func() {
							ui.Text(c, s.when).FontWeight(500).TextColor(palette.text)
							ui.Text(c, s.joke).FontSize(11.5).TextColor(palette.muted).Italic()
						})
					}).Clicked() {
						a.snooze(c, t, s.when, s.until(a.now), cat)
					}
				}
			})
		})
	})
	if !open && a.snoozeFor == t.id() {
		a.snoozeFor = api.ID{} // dismissed
	}
}

func (a *app) snooze(c *ui.Context, t thread, when string, until time.Time, from category) {
	a.backend.SetDisposition(t.env, t.id(), api.Disposition{State: api.Snoozed, Until: until})
	a.snoozeFor = api.ID{}
	a.gag(c, t.id(), gagSnooze, from)
	c.AddToast(ui.Toast{Title: "Sleeping until " + lower(when) + " 💤", Description: t.View.Title, Type: "info"})
}
