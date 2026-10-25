package main

import (
	"fmt"
	"slices"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/client"
)

// Child threads: a thread can start others on its engine, which nest under
// it in the sidebar and show over its composer as Cursor's agents do. A
// child reports back to its parent as it finishes, fails or blocks on the
// user, as a card in the parent's timeline (the engine posts it); stopping
// or deleting a thread does as much to everything under it; settling one
// leaves its children as they are, nested under it wherever it shows; a
// child runs at its parent's access or less, and its tokens count in its
// parent's total.

const maxDepth = api.MaxDepth

// kidsOf indexes every machine's threads by parent, not those being
// deleted, each parent's newest first: what the sidebar,
// the agents panel and the cascades walk.
func (a *app) kidsOf() map[api.ID][]thread {
	kids := map[api.ID][]thread{}
	for _, e := range a.backend.Envs() {
		for _, th := range e.Threads {
			t := thread{e, th}
			if p := th.View.Parent; !p.IsZero() && !deleting(t) {
				kids[p] = append(kids[p], t)
			}
		}
	}
	for _, ts := range kids {
		slices.SortFunc(ts, newestFirst)
	}
	return kids
}

// parentOf is the thread that started t, on t's machine, unless it's gone
// or going: a thread whose parent was deleted stands on its own.
func (a *app) parentOf(t thread) (thread, bool) {
	if t.View.Parent.IsZero() || t.env == nil {
		return thread{}, false
	}
	th := t.env.Threads[t.View.Parent]
	if th == nil || deleting(thread{t.env, th}) {
		return thread{}, false
	}
	return thread{t.env, th}, true
}

// ancestors are t's parent, its parent's and so on, the furthest first.
// The walk stops past maxDepth, so a cycle can't hang it.
func (a *app) ancestors(t thread) []thread {
	var up []thread
	for p, ok := a.parentOf(t); ok && len(up) <= maxDepth; p, ok = a.parentOf(p) {
		up = append([]thread{p}, up...)
	}
	return up
}

func (a *app) depth(t thread) int { return len(a.ancestors(t)) }

// nested is whether t shows under its parent rather than on its own: its
// parent is there.
func (a *app) nested(t thread) bool {
	_, ok := a.parentOf(t)
	return ok
}

// node is a thread in a tree, with its depth below the tree's top.
type node struct {
	t     thread
	depth int
}

// below is every thread under t, depth first.
// The walk stops past maxDepth, so a cycle can't hang it.
func (a *app) below(t thread) []node {
	var out []node
	seen := map[api.ID]bool{t.id(): true}
	var walk func(id api.ID, d int)
	walk = func(id api.ID, d int) {
		if d > maxDepth {
			return
		}
		for _, k := range a.kids[id] {
			if seen[k.id()] {
				continue
			}
			seen[k.id()] = true
			out = append(out, node{k, d})
			walk(k.id(), d+1)
		}
	}
	walk(t.id(), 1)
	return out
}

// shown are the threads under t that show, in the sidebar and the agents
// panel: all of them.
func (a *app) shown(t thread) []node { return a.below(t) }

func needsYou(ns []node) bool {
	return slices.ContainsFunc(ns, func(n node) bool { return len(n.t.View.Requests) > 0 })
}

func working(ns []node) int {
	n := 0
	for _, x := range ns {
		if x.t.View.Live() {
			n++
		}
	}
	return n
}

// usage is the tokens t and every thread under it used.
func (a *app) usage(t thread) int64 {
	n := t.View.Tokens
	for _, d := range a.below(t) {
		n += d.t.View.Tokens
	}
	return n
}

func fmtTokens(n int64) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}

// startChild opens a draft of a child of t, on its machine and in its
// workspace, its line opened in the sidebar to show it once sent.
func (a *app) startChild(t thread) {
	if a.depth(t) >= maxDepth || t.id().IsZero() {
		return
	}
	a.startIn(draft{t.env, t.View.Workspace})
	a.freshParent = t.id()
	for _, x := range append(a.ancestors(t), t) {
		a.childrenOpen[x.id()] = true
	}
}

// accessUnder is the widest access a child of parent may have: its
// parent's.
func accessUnder(want api.Access, parent thread) api.Access {
	return min(want, parent.View.Access)
}

// --- Stopping and deleting trees

// stopTree stops t's run and every live run under it; it returns how many it stopped under t.
func (a *app) stopTree(t thread) int {
	if t.View.Live() {
		a.backend.Interrupt(t.env, t.id())
	}
	n := 0
	for _, d := range a.below(t) {
		if d.t.View.Live() {
			a.backend.Interrupt(d.t.env, d.t.id())
			n++
		}
	}
	return n
}

// deleteTree deletes t and every thread under it. The
// engine deletes no thread whose agent works: those are stopped, and
// deleted once they have.
func (a *app) deleteTree(t thread) {
	tree := append([]node{{t, 0}}, a.below(t)...)
	a.stopTree(t)
	for _, d := range tree {
		if d.t.View.Live() {
			a.afterStop[d.t.id()] = true
			continue
		}
		a.delete(d.t)
	}
}

// deleteStopped deletes the threads deleteTree stopped, once their agents
// have: each frame, before the view reads them.
func (a *app) deleteStopped() {
	for id := range a.afterStop {
		t, ok := a.find(id)
		switch {
		case !ok || deleting(t):
			delete(a.afterStop, id)
		case !t.View.Live():
			a.delete(t)
			delete(a.afterStop, id)
		}
	}
}

func isAre(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}

// --- Sidebar

// childToggle opens and closes a thread's children in the sidebar: their
// count, an amber dot while one under it waits on an approval, and a
// chevron.
func (a *app) childToggle(c *ui.Context, t thread) {
	tree := a.shown(t)
	open := a.childrenOpen[t.id()]
	label := plural(len(tree), "child thread")
	b := ghost(c, "children", open).Padding(2, 5).Gap(3).Shrink(0).Label(label).Tooltip(label)
	if b.Children(func() {
		if needsYou(tree) {
			ui.Box(c).Size(6, 6).Radius(3).Background(palette.amber)
		}
		ui.Textf(c, "%d", len(tree)).FontSize(11)
		chevron(c, open).FontSize(10)
	}).Clicked() {
		a.childrenOpen[t.id()] = !open
	}
}

// childRows lists t's children under its row while they're open, each
// indented by its depth, theirs under them.
func (a *app) childRows(c *ui.Context, t thread, depth int) {
	if !a.childrenOpen[t.id()] || depth > maxDepth {
		return
	}
	for _, k := range a.kids[t.id()] {
		a.childRow(c, k, depth)
		a.childRows(c, k, depth+1)
	}
}

// childRow is a child thread in the sidebar, under its parent: what it's
// doing, its title, bold while unread, and its children or its age. Its
// menu is the thread menu.
func (a *app) childRow(c *ui.Context, k thread, depth int) {
	selected := k.id() == a.selected && a.router.Path() == "/"
	r := row(c, k.id(), selected).Height(28).Padding(0, 6, 0, 8+float32(depth)*16).Gap(8).Transition(fold)
	r.ContextMenu(func(m *ui.Menu) { a.threadMenu(c, m, k, a.categoryOf(k)) })
	if r.Children(func() {
		ui.Icon(c, icChild).FontSize(12).TextColor(palette.faint).Shrink(0)
		if a.renaming == k.id() {
			a.renameInput(c, k)
			return
		}
		name := ui.Text(c, title(k)).FontSize(12.5).SingleLine().Grow(1).Shrink(1)
		if a.backend.Unread(k.Thread) {
			name.FontWeight(600).TextColor(palette.text)
		}
		statusMark(c, a.statusOf(k))
		if len(a.shown(k)) > 0 {
			a.childToggle(c, k)
		} else {
			ui.Text(c, ago(k.View.UpdatedAt, a.now)).FontSize(11).TextColor(palette.faint).Shrink(0)
		}
	}).Clicked() {
		a.select_(k)
	}
}

// --- The chat

// breadcrumb is the way back up from a child: its ancestors in the header,
// the furthest first; narrow, only its parent.
func (a *app) breadcrumb(c *ui.Context, t thread, narrow bool) {
	up := a.ancestors(t)
	if p, ok := a.draftParent(); ok && t.id().IsZero() {
		up = append(a.ancestors(p), p)
	}
	if narrow && len(up) > 1 {
		up = up[len(up)-1:]
	}
	for _, x := range up {
		b := ghost(c, "crumb:"+x.id().String(), false).Padding(3, 6).Shrink(1).MinWidth(40).Label("Back to " + title(x)).Tooltip(title(x))
		if b.Children(func() {
			ui.Text(c, title(x)).FontSize(12.5).SingleLine().MaxWidth(180)
		}).Clicked() {
			a.select_(x)
		}
		ui.Icon(c, icChevronR).FontSize(11).TextColor(palette.faint).Shrink(0)
	}
}

// draftParent is the thread the draft is a child of, while it's there.
func (a *app) draftParent() (thread, bool) {
	if a.freshParent.IsZero() {
		return thread{}, false
	}
	p, ok := a.find(a.freshParent)
	if !ok || deleting(p) {
		return thread{}, false
	}
	return p, true
}

// usagePill is the thread's tokens, and with children, everything under it
// rolled up.
func (a *app) usagePill(c *ui.Context, t thread) {
	own, total := t.View.Tokens, a.usage(t)
	if total == 0 {
		return
	}
	label, tip := fmtTokens(own)+" tokens", "This thread's tokens"
	if total != own {
		label = fmtTokens(own) + " · " + fmtTokens(total) + " with children"
		tip = fmt.Sprintf("This thread %s, its child threads %s", fmtTokens(own), fmtTokens(total-own))
	}
	pill(c, label).Shrink(0).Tooltip(tip)
}

// reportCard is the children's reports in the parent's timeline: each
// one's state, a line or two of what came of it, and a way into it.
func (a *app) reportCard(c *ui.Context, t thread, key string, e entry) {
	ui.Column(c).Key(key).Radius(12).Border(1, palette.border).Background(palette.bg.Alpha(0.6)).Padding(4).Gap(2).
		Transition(riseIn).Children(func() {
		for i, n := range e.reports {
			icon, col, state := icCheckCirc, palette.green, "Done"
			switch n.Kind {
			case api.ReportFailed:
				icon, col, state = icX, palette.red, "Failed"
			case api.ReportBlocked:
				icon, col, state = icHand, palette.amber, "Needs you"
			}
			child, found := thread{}, false
			if t.env != nil {
				if th := t.env.Threads[n.Child]; th != nil && !deleting(thread{t.env, th}) {
					child, found = thread{t.env, th}, true
				}
			}
			r := ghost(c, i, false).Padding(7, 10).Gap(10).AlignItems(ui.Start).Label(n.Title).Disabled(!found)
			if r.Children(func() {
				ui.Icon(c, icon).FontSize(14).TextColor(col).Margin(1, 0, 0)
				ui.Column(c).Grow(1).Shrink(1).Gap(2).Children(func() {
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						ui.Text(c, n.Title).FontSize(12.5).FontWeight(600).TextColor(palette.text).SingleLine().Shrink(1)
						ui.Text(c, state).FontSize(11).FontWeight(500).TextColor(col).Shrink(0)
					})
					if n.Outcome != "" {
						ui.Text(c, n.Outcome).FontSize(12).TextColor(palette.muted).LineHeight(1.4)
					}
				})
				if found {
					ui.Icon(c, icArrowRight).FontSize(13).TextColor(palette.faint).Margin(1, 0, 0)
				} else {
					ui.Text(c, "Deleted").FontSize(11).TextColor(palette.faint)
				}
			}).Clicked() && found {
				a.select_(child)
			}
		}
	})
}

// addReport joins a report to a run of them, one card's, a child's later report
// standing for its earlier ones.
func addReport(rs []client.ReportEntry, r client.ReportEntry) []client.ReportEntry {
	if i := slices.IndexFunc(rs, func(x client.ReportEntry) bool { return x.Child == r.Child }); i >= 0 {
		rs[i] = r
		return rs
	}
	return append(rs, r)
}

// accessCapNote says, under a child's draft, when its parent's access is
// narrower than the composer's: the child runs at its parent's.
func (a *app) accessCapNote(c *ui.Context, t thread) {
	p, ok := a.draftParent()
	if !ok || !t.id().IsZero() || accessOf[a.access] <= p.View.Access {
		return
	}
	ui.Text(c, "Runs at “"+accessLabel(p.View.Access)+"”, as its parent does").FontSize(11.5).TextColor(palette.faint)
}
