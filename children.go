package main

import (
	"fmt"
	"maps"
	"slices"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/plain"
	"glitched/internal/runtime"
)

// Child threads: an agent's thread can start others on its engine (the
// user never does), which nest under it in the sidebar and show over its
// composer as Cursor's agents do. A child reports back to its parent as it
// finishes, fails or blocks on the user, as a card in the parent's
// timeline (the engine posts it). Deleting or settling a thread takes
// everything under it, and the agents panel's Stop all stops what works
// under it; stopping or snoozing a thread leaves its children as they are.
// A child runs at its parent's access or less, and its tokens count in its
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

// shown is what the sidebar and the agents panel list under t, which is
// every thread below it.
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

// pruneThreadState drops what the app keeps by thread for threads no
// longer there: the sidebar's open children and the composers seen.
func (a *app) pruneThreadState() {
	gone := func(id api.ID, _ bool) bool {
		_, ok := a.find(id)
		return !id.IsZero() && !ok
	}
	maps.DeleteFunc(a.childrenOpen, gone)
	maps.DeleteFunc(a.composerSeen, gone)
}

// --- Stopping and deleting trees

// stopAll stops every live thread of ns; it returns how many.
func (a *app) stopAll(ns []node) int {
	n := 0
	for _, d := range ns {
		if d.t.View.Live() {
			a.backend.Interrupt(d.t.env, d.t.id())
			n++
		}
	}
	return n
}

// settleTree settles t and every thread in its tree. The engine stops a
// thread's run and cancels what waits its turn as it settles it, and
// starts nothing on a settled thread, so nothing in the tree works on.
func (a *app) settleTree(t thread) {
	a.backend.SettleTree(t.env, t.id())
}

// deleteTree deletes t and every thread under it. The engine ends a
// deleted thread's run with it, so none waits on a stop, whatever this
// GUI last saw of the thread. The chat shows a deleted thread until the
// delete lands, so a refused one leaves it shown with why, and then shows
// a new thread in its workspace.
func (a *app) deleteTree(t thread) {
	for _, n := range append(a.below(t), node{t, 0}) {
		if a.selected == n.t.id() {
			a.fresh = draft{n.t.env, n.t.View.Workspace}
		}
	}
	a.backend.DeleteTree(t.env, t.id())
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
	b := a.tip(c, ghost(c, "children", open).Padding(2, 5).Gap(3).Shrink(0).Label(label), label)
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
		a.statusMark(c, a.statusOf(k))
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
	if narrow && len(up) > 1 {
		up = up[len(up)-1:]
	}
	for _, x := range up {
		b := a.tip(c, ghost(c, "crumb:"+x.id().String(), false).Padding(3, 6).Shrink(1).MinWidth(40).Label("Back to "+title(x)), title(x))
		if b.Children(func() {
			ui.Text(c, title(x)).FontSize(12.5).SingleLine().MaxWidth(180)
		}).Clicked() {
			a.select_(x)
		}
		ui.Icon(c, icChevronR).FontSize(11).TextColor(palette.faint).Shrink(0)
	}
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
	a.tip(c, pill(c, label).Shrink(0), tip)
}

// stillBlocked is whether a blocked report of child's, in t's transcript,
// still stands: the child's run waits on a request, and the report is the
// last the run made of one. A run that asked twice made two; the first was
// answered.
func stillBlocked(t, child thread, r client.ReportEntry) bool {
	if !slices.ContainsFunc(child.View.Requests, func(q api.Request) bool { return q.Run == r.Run }) || t.Transcript == nil {
		return false
	}
	for i := len(t.Transcript.Entries) - 1; i >= 0; i-- {
		if e, ok := t.Transcript.Entries[i].(*client.ReportEntry); ok && e.Child == r.Child && e.Run == r.Run && e.Kind == api.ReportBlocked {
			return e.At.Equal(r.At)
		}
	}
	return false
}

// reportCard is the children's reports in the parent's timeline: each
// one's state, a line or two of what came of it, and a way into it.
func (a *app) reportCard(c *ui.Context, t thread, key string, e entry) {
	ui.Column(c).Key(key).Radius(12).Border(1, palette.border).Background(palette.bg.Alpha(0.6)).Padding(4).Gap(2).
		Transition(riseIn).Children(func() {
		for i, n := range e.reports {
			child, found := thread{}, false
			if t.env != nil {
				if th := t.env.Threads[n.Child]; th != nil && !deleting(thread{t.env, th}) {
					child, found = thread{t.env, th}, true
				}
			}
			icon, col, state := icCheckCirc, palette.green, "Done"
			switch {
			case n.Kind == api.ReportFailed:
				icon, col, state = icX, palette.red, "Failed"
			case n.Kind == api.ReportBlocked && found && stillBlocked(t, child, n):
				icon, col, state = icHand, palette.amber, "Needs you"
			case n.Kind == api.ReportBlocked:
				// A report is never updated: once the request is answered or
				// the run stopped, it only says what the child waited on.
				icon, col, state = icHand, palette.faint, "Was waiting"
			}
			r := ghost(c, i, false).Padding(7, 10).Gap(10).AlignItems(ui.Start).Label(n.Title).Disabled(!found)
			if r.Children(func() {
				ui.Icon(c, icon).FontSize(14).TextColor(col).Margin(1, 0, 0)
				ui.Column(c).Grow(1).Shrink(1).Gap(2).Children(func() {
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						ui.Text(c, n.Title).FontSize(12.5).FontWeight(600).TextColor(palette.text).SingleLine().Shrink(1)
						ui.Text(c, state).FontSize(11).FontWeight(500).TextColor(col).Shrink(0)
					})
					outcome, escaped := n.Outcome, false
					if n.Kind == api.ReportBlocked {
						outcome, escaped = askedLine(n.Outcome)
					}
					if outcome != "" {
						ui.Text(c, outcome).FontSize(12).TextColor(palette.muted).LineHeight(1.4)
					}
					if escaped {
						asciiNotice(c)
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

// askedLine is a blocked report's line, what the child asks, as its card
// shows it: printable ASCII, and whether it escaped any. The ellipsis of
// a line the engine cut is the app's, not the agent's.
func askedLine(s string) (string, bool) {
	s, cut := runtime.Cut(s)
	s, escaped := plain.Text(s, false)
	if cut {
		s += "…"
	}
	return s, escaped
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
