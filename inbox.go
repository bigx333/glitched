package main

import (
	"fmt"

	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"
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
	tintNew      = ui.Hex("#a78bfa")
	tintSearch   = ui.Hex("#60a5fa")
	tintSettings = ui.Hex("#fb923c")
	tintSnooze   = ui.Hex("#fbbf24")
	tintSettle   = ui.Hex("#4ade80")
	tintBack     = ui.Hex("#60a5fa")
)

// rowHeight is a thread row's height, the same whether it shows its time
// or, pointed at, its actions: pointing at it moves nothing.
const rowHeight = 32

// categoryOf is where a thread goes: working while the agent runs, whatever
// else it is; then snoozed or settled as the user left it; else the inbox.
func categoryOf(t *thread) category {
	switch {
	case t.status == statusWorking:
		return catWorking
	case t.snoozed != "":
		return catSnoozed
	case t.settled:
		return catSettled
	}
	return catInbox
}

// shownIn is the section a thread shows in: its own, but for a moment
// after an action, the one it left, while its joke plays there.
func (a *app) shownIn(t *thread) category {
	if fx, ok := a.rowFx[t.id]; ok && a.now.Sub(fx.at) < fx.hold {
		return fx.from
	}
	return categoryOf(t)
}

type inboxItem struct {
	t *thread
	p *project
}

// itemsIn returns the threads of a category, of the project filtered to.
func (a *app) itemsIn(cat category) []inboxItem {
	var items []inboxItem
	for _, p := range a.projects {
		if a.projectFilter != "" && p.id != a.projectFilter {
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
		ui.Text(c, "T3 Code").Bold().FontSize(13)
		ui.Text(c, "ALPHA").FontSize(9).Bold().LetterSpacing(0.6).TextColor(palette.muted).
			Padding(1, 5).Radius(4).Background(palette.hover)
	})
	ui.Column(c).Padding(0, 8, 4).Gap(1).Children(func() {
		if ghost(c, "new-thread", false).Padding(7, 8).Children(func() {
			ui.Icon(c, icSquarePen).FontSize(15).TextColor(tintNew)
			ui.Text(c, "New thread").Grow(1)
			shortcutHint(c, "⌘", "N")
		}).Clicked() {
			a.newThread()
		}
		if ghost(c, "search", false).Padding(7, 8).Children(func() {
			ui.Icon(c, icSearch).FontSize(15).TextColor(tintSearch)
			ui.Text(c, "Search").Grow(1)
			shortcutHint(c, "⌘", "K")
		}).Clicked() {
			a.openCommandPalette()
		}
	})
	a.projectStrip(c)
	ui.Scroll(c).Grow(1).Padding(4, 8, 8).Gap(10).Children(func() {
		for _, s := range categories {
			a.section(c, s.cat, s.title, s.empty, s.icon, s.color)
		}
	})
	ui.Column(c).Padding(8).BorderWidth(1, 0, 0, 0).BorderColor(palette.border).Children(func() {
		if ghost(c, "settings", false).Padding(7, 8).Children(func() {
			ui.Icon(c, icSettings).FontSize(15).TextColor(tintSettings)
			ui.Text(c, "Settings").Grow(1)
			shortcutHint(c, "⌘", ",")
		}).Clicked() {
			a.router.Push("/settings/general")
		}
	})
}

// projectTile is a project's mark: its icon, white on its color.
func projectTile(c *ui.Context, p *project, size float32) ui.Element {
	return ui.Box(c).Size(size, size).Radius(size * 0.28).Center().Shrink(0).
		LinearGradient(ui.LinearGradient{From: p.color.Mix(ui.Hex("#ffffff"), 0.1), To: p.color.Mix(ui.Hex("#000000"), 0.2), Angle: 135, Oklab: true}).
		Children(func() { ui.Icon(c, p.icon).FontSize(size * 0.6).TextColor(ui.Hex("#ffffff")) })
}

// projectStrip filters the inbox to one project: All, then each project's
// tile, then a button adding one.
func (a *app) projectStrip(c *ui.Context) {
	ui.Row(c).Padding(6, 12, 6, 14).Gap(4).AlignItems(ui.Center).Children(func() {
		all := ghost(c, "all", a.projectFilter == "").Padding(4, 8).Tooltip("All projects")
		if all.Children(func() { ui.Text(c, "All").FontSize(11.5).FontWeight(500) }).Clicked() {
			a.projectFilter = ""
		}
		for _, p := range a.projects {
			on := a.projectFilter == p.id
			b := ghost(c, p.id, on).Padding(3).Label(p.name).Tooltip(p.name)
			if !on && a.projectFilter != "" {
				b.Opacity(0.5)
			}
			if b.Children(func() { projectTile(c, p, 20) }).Clicked() {
				if on {
					a.projectFilter = ""
				} else {
					a.projectFilter = p.id
				}
			}
		}
		ui.Spacer(c)
		iconButton(c, "add-project", icPlus, "Add project", false).Padding(4)
	})
}

// section lists a category's threads under a header that opens and closes
// it, with their count. Its column is built even while empty, so that a
// thread moving into it folds in rather than appearing.
func (a *app) section(c *ui.Context, cat category, title, empty string, icon *ui.SVG, color ui.Color) {
	items := a.itemsIn(cat)
	open := !a.sectionClosed[cat]
	ui.Column(c).Key(title).Gap(2).Children(func() {
		if len(items) > 0 || cat == catInbox {
			if ghost(c, "head", false).Height(30).Padding(0, 8).Gap(8).Children(func() {
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
				ui.Spacer(c)
				chevron(c, open).TextColor(palette.faint)
			}).Clicked() {
				a.sectionClosed[cat] = open
			}
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
		if it.t.unread {
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
	selected := t.id == a.selected && a.router.Path() == "/"
	r := row(c, t.id, selected).Height(rowHeight).Padding(0, 6, 0, 8).Gap(9).Transition(fold)
	fx, playing := a.rowFx[t.id]
	if playing && fx.done(c.Now()) {
		delete(a.rowFx, t.id)
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
			if mark := statusBadge(c, t); mark.Valid() {
				mark.Absolute().Right(-3).Bottom(-3)
			}
			if playing {
				floatGag(c, fx)
			}
		})
		if playing && fx.kind == gagSettle {
			burstGag(wrap, fx)
		}
		title := ui.Text(c, t.title).FontSize(13).SingleLine().Grow(1).Shrink(1)
		if t.unread {
			title.FontWeight(600).TextColor(palette.text)
		}
		// The actions show while it's pointed at, and while its snooze
		// popover is open, which takes the pointer away from the row.
		if hovered || a.snoozeFor == t.id {
			a.rowActions(c, t, cat)
			return
		}
		switch {
		case cat == catSnoozed:
			ui.Row(c).Gap(3).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, icClock).FontSize(10).TextColor(palette.faint)
				ui.Text(c, t.snoozed).FontSize(11).TextColor(palette.faint)
			})
		case t.unread:
			ui.Box(c).Size(7, 7).Radius(4).Background(palette.accent)
		default:
			ui.Text(c, t.age).FontSize(11).TextColor(palette.faint)
		}
	}).Clicked() {
		t.unread = false
		a.select_(t.id)
	}
}

// statusBadge is a small mark of what a thread is doing, on its project's
// tile: the agent working, or waiting for an approval.
func statusBadge(c *ui.Context, t *thread) ui.Element {
	switch t.status {
	case statusWorking:
		return ui.Box(c).Size(11, 11).Radius(6).Center().Background(palette.sidebar).Children(func() {
			statusMark(c, statusWorking)
		})
	case statusApproval:
		return ui.Box(c).Size(9, 9).Radius(5).Background(palette.amber).Border(2, palette.sidebar).Tooltip("Pending approval")
	}
	return ui.Element{}
}

var snoozeTimes = []struct{ when, joke, emoji string }{
	{"Later today", "a power nap", "☕"},
	{"Tomorrow", "sleep on it", "🌙"},
	{"Next week", "a long vacation", "🏝"},
}

// rowActions are a pointed-at thread's actions: snooze it until a time,
// and settle it; a settled or snoozed thread goes back to the inbox
// instead.
func (a *app) rowActions(c *ui.Context, t *thread, cat category) {
	ui.Row(c).Gap(0).Children(func() {
		switch cat {
		case catSettled, catSnoozed:
			if iconButton(c, "back", icUndo, "Back to inbox", false).Padding(3).TextColor(tintBack).Clicked() {
				t.settled, t.snoozed = false, ""
				a.gag(c, t, gagUndo, cat)
			}
		case catInbox:
			a.snoozeButton(c, t, cat)
			if iconButton(c, "settle", icCheckCirc, "Settle", false).Padding(3).TextColor(tintSettle).Clicked() {
				t.settled, t.unread = true, false
				a.gag(c, t, gagSettle, cat)
				c.AddToast(ui.Toast{Title: "Settled. Phew.", Description: t.title})
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
func (a *app) snoozeButton(c *ui.Context, t *thread, cat category) {
	open := a.snoozeFor == t.id
	b := iconButton(c, "snooze", icClock, "Snooze", open).Padding(3).TextColor(tintSnooze)
	if b.Clicked() {
		if open {
			a.snoozeFor = ""
		} else {
			a.snoozeFor = t.id
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
						a.snooze(c, t, s.when, cat)
					}
				}
			})
		})
	})
	if !open && a.snoozeFor == t.id {
		a.snoozeFor = "" // dismissed
	}
}

func (a *app) snooze(c *ui.Context, t *thread, when string, from category) {
	t.snoozed, t.settled = when, false
	a.snoozeFor = ""
	a.gag(c, t, gagSnooze, from)
	c.AddToast(ui.Toast{Title: "Sleeping until " + lower(when) + " 💤", Description: t.title, Type: "info"})
}
