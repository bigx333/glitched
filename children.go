package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/plain"
	"glitched/internal/plugins"
	"glitched/internal/plugins/widget"
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
	for _, t := range a.recent() {
		if p := t.View.Parent; !p.IsZero() && !deleting(t) {
			kids[p] = append(kids[p], t)
		}
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
// longer there: the sidebar's open children, the composers seen, and the
// timeline's rows, markers, and views with their open Zen lines.
func (a *app) pruneThreadState() {
	gone := func(id api.ID, _ bool) bool {
		_, ok := a.find(id)
		return !id.IsZero() && !ok
	}
	maps.DeleteFunc(a.childrenOpen, gone)
	maps.DeleteFunc(a.composerSeen, gone)
	maps.DeleteFunc(a.rows, func(id api.ID, _ *rowsCache) bool { return gone(id, false) })
	maps.DeleteFunc(a.markers, func(id api.ID, _ *markerState) bool { return gone(id, false) })
	maps.DeleteFunc(a.listed, func(id api.ID, _ *timelineMemo) bool { return gone(id, false) })
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

// settleTree settles t, and the engine settles every thread under it in
// the same commit: it stops their runs, cancels what waits its turn on
// them, and starts nothing on a settled thread or a child under one, so
// nothing in the tree works on.
func (a *app) settleTree(t thread) {
	a.backend.SetDisposition(t.env, t.id(), api.Disposition{State: api.Settled})
}

// deleteTree deletes t, and the engine deletes every thread under it in
// the same commit, ending their runs, a child no GUI has seen yet
// included. The chat shows a deleted thread until the delete lands, so a
// refused one leaves it shown with why, and then shows a new thread in
// its project.
func (a *app) deleteTree(t thread, removeWorktree bool) {
	for _, n := range append(a.below(t), node{t, 0}) {
		if a.selected == n.t.id() {
			a.newDraft(draft{env: n.t.env, workspace: a.projectOf(n.t).key.workspace})
		}
	}
	a.backend.Delete(t.env, t.id(), removeWorktree)
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
	if a.revealRow(k.id()) {
		r.ScrollIntoView()
	}
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
		switch {
		case len(a.shown(k)) > 0:
			a.childToggle(c, k)
		case remote(k) != nil:
			a.remoteMark(c, k, 11)
		default:
			ui.Text(c, ago(k.View.UpdatedAt, a.now)).FontSize(11).TextColor(palette.faint).Shrink(0)
		}
	}).Clicked() {
		a.choose(k)
	}
}

// --- The chat

// heading is the thread's title in the header, and a child's way back up
// before it: its ancestors, the furthest first; narrow, only its parent.
// A child's is a copy of thread.header.breadcrumb, which the built-in
// Child threads draws from the natives thread.crumb, each ancestor's
// button as Glitched draws it, and thread.heading, the title. The title is
// in the copy so that the crumbs and it share the header's room as they
// did beside each other. It returns the title, which the header measures.
func (a *app) heading(c *ui.Context, t thread, narrow bool) ui.Element {
	up := a.ancestors(t)
	if len(up) == 0 && t.View.Origin != nil {
		var heading ui.Element
		ui.Row(c).Key("origin-heading").Gap(8).AlignItems(ui.Center).Shrink(1).Children(func() {
			a.originCrumb(c, t)
			ui.Icon(c, icChevronR).FontSize(11).TextColor(palette.faint).Shrink(0)
			heading = headingTitle(c, t)
		})
		return heading
	}
	if len(up) == 0 {
		return headingTitle(c, t)
	}
	b := headerBreadcrumb{Narrow: narrow}
	for _, x := range up {
		b.Ancestors = append(b.Ancestors, headerCrumb{ID: x.id().String(), Title: title(x)})
	}
	// The key says whether the header is narrow, so that a drawing of it
	// wide never stands for it narrow.
	key := t.id().String()
	if narrow {
		key += " narrow"
	}
	drawn := map[string]int{}
	var heading ui.Element
	a.pluginComponent(c, "thread.header.breadcrumb", &t, componentCopy{
		key: key,
		box: func() ui.Element {
			return ui.Row(c).Key("component:thread.header.breadcrumb").Gap(8).AlignItems(ui.Center).Shrink(1)
		},
		context: func(r plugins.ThreadRecord) any {
			b.Thread = r
			return b
		},
		action: func(n *widget.Node) {
			if n.Name == "thread.heading" {
				heading = headingTitle(c, t)
				return
			}
			var id string
			json.Unmarshal(n.Props["thread"], &id)
			if i := slices.IndexFunc(up, func(x thread) bool { return x.id().String() == id }); i >= 0 {
				drawn[id]++
				a.crumb(c, up[i], drawn[id])
			}
		},
		builtin: func() *widget.Node { return breadcrumbRow(b) },
	})
	return heading
}

// headerBreadcrumb is the context of thread.header.breadcrumb: the
// threads above the one shown, the furthest first, and whether the
// header is narrow, beside a panel.
type headerBreadcrumb struct {
	Thread    plugins.ThreadRecord `json:"thread"`
	Ancestors []headerCrumb        `json:"ancestors"`
	Narrow    bool                 `json:"narrow"`
}

type headerCrumb struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// breadcrumbRow is the built-in Child threads' drawing of the breadcrumb
// b, as its main.js draws it: each ancestor's thread.crumb and a chevron
// after it, narrow only the parent's, then thread.heading. Glitched draws
// the same while no plugin has drawn it.
func breadcrumbRow(b headerBreadcrumb) *widget.Node {
	up := b.Ancestors
	if b.Narrow && len(up) > 1 {
		up = up[len(up)-1:]
	}
	row := &widget.Node{Type: "row", Gap: 8}
	for _, x := range up {
		id, _ := json.Marshal(x.ID)
		row.Children = append(row.Children,
			&widget.Node{Type: "native", ID: "crumb-" + x.ID, Name: "thread.crumb", Props: map[string]json.RawMessage{"thread": id}},
			&widget.Node{Type: "icon", Name: "alt-arrow-right", FontSize: 11, Color: "$text.faint"})
	}
	row.Children = append(row.Children, &widget.Node{Type: "native", ID: "title", Name: "thread.heading"})
	return row
}

// headingTitle is the thread's title in the header.
func headingTitle(c *ui.Context, t thread) ui.Element {
	return ui.Text(c, title(t)).Key("heading-title").FontWeight(600).SingleLine().Shrink(1).MinWidth(80)
}

// crumb is the nth button back to x, an ancestor of the thread shown, in
// this frame's breadcrumb: a copy may draw one twice.
func (a *app) crumb(c *ui.Context, x thread, nth int) {
	key := "crumb:" + x.id().String()
	if nth > 1 {
		key += "#" + strconv.Itoa(nth)
	}
	b := a.tip(c, ghost(c, key, false).Padding(3, 6).Shrink(1).MinWidth(40).Label("Back to "+title(x)), title(x))
	if b.Children(func() {
		ui.Text(c, title(x)).FontSize(12.5).SingleLine().MaxWidth(180)
	}).Clicked() {
		a.choose(x)
	}
}

// usagePill is the thread's tokens, and with children, everything under it
// rolled up. It is a copy of thread.header.usage, which the built-in Child
// threads draws.
func (a *app) usagePill(c *ui.Context, t thread) {
	u := headerUsage{Tokens: t.View.Tokens, Total: a.usage(t), Context: t.View.Context}
	if u.Total == 0 && u.Context == nil {
		return
	}
	a.pluginComponent(c, "thread.header.usage", &t, componentCopy{
		key: t.id().String(),
		box: func() ui.Element {
			return ui.Row(c).Key("component:thread.header.usage").Shrink(0).AlignItems(ui.Center)
		},
		context: func(r plugins.ThreadRecord) any {
			u.Thread = r
			return u
		},
		builtin: func() *widget.Node { return usageBadge(u) },
	})
}

// headerUsage is the context of thread.header.usage: the thread's own
// tokens, its and every thread's under it, and how full its context is,
// nil before its agent said.
type headerUsage struct {
	Thread  plugins.ThreadRecord `json:"thread"`
	Tokens  int64                `json:"tokens"`
	Total   int64                `json:"total"`
	Context *api.Context         `json:"context,omitempty"`
}

// usageBadge is the built-in Child threads' drawing of the usage u, as its
// main.js draws it. Once the agent said how full the context is, a meter
// of it: a ring and the share used, amber from 75% and red from 90%, or
// what is known of it; the tokens move to its tooltip. Before, a pill of
// the tokens, and with children, everything under the thread rolled up.
func usageBadge(u headerUsage) *widget.Node {
	label, tip := fmtTokens(u.Tokens)+" tokens", "This thread's tokens"
	// Compared as main.js compares them, past 2^53 too.
	children := float64(u.Total) != float64(u.Tokens)
	if children {
		label = fmtTokens(u.Tokens) + " · " + fmtTokens(u.Total) + " with children"
		tip = fmt.Sprintf("This thread %s, its child threads %s", fmtTokens(u.Tokens), fmtTokens(u.Total-u.Tokens))
	}
	c := u.Context
	if c == nil {
		return &widget.Node{Type: "badge", Style: "pill", Text: label, Tooltip: tip}
	}
	if !children {
		tip = "This thread's tokens " + fmtTokens(u.Tokens)
	}
	// In float64, as main.js counts, so both draw the same.
	used, window := max(0, float64(c.Used)), float64(c.Window)
	switch {
	case c.Stale:
		return &widget.Node{Type: "badge", Style: "pill", Text: "Compacted", Value: json.RawMessage("0"), Color: "$text.faint",
			Tooltip: "Context compacted. Its size shows after the next reply.\n" + tip}
	case window <= 0:
		return &widget.Node{Type: "badge", Style: "pill", Text: contextTokens(used) + " in context",
			Tooltip: contextTokens(used) + " tokens in context. Context limit not reported.\n" + tip}
	}
	// The color follows the share as the label rounds it, as bb's does,
	// so a pill that says 75% is amber.
	share, pct := used/window, math.Floor((used*100+math.Floor(window/2))/window)
	color := ""
	switch {
	case pct >= 90:
		color = "$danger"
	case pct >= 75:
		color = "$warning"
	}
	return &widget.Node{Type: "badge", Style: "pill", Text: num(pct) + "%", Value: json.RawMessage(num(math.Round(min(1, share)*1e4) / 1e4)), Color: color,
		Tooltip: "Context window " + contextTokens(used) + " / " + contextTokens(window) + " tokens, " + num(max(0, 100-pct)) + "% left\n" + tip}
}

// num is n as JavaScript writes a whole number or a short fraction.
func num(n float64) string { return strconv.FormatFloat(n, 'f', -1, 64) }

// contextTokens is n as the meter writes it: whole under a thousand, else
// in whole thousands, else in millions to a tenth, a half rounded up.
func contextTokens(n float64) string {
	if n < 1000 {
		return num(n)
	}
	if k := math.Floor((n + 500) / 1000); k < 1000 {
		return num(k) + "k"
	}
	tenths := math.Floor((n + 50000) / 100000)
	if math.Mod(tenths, 10) == 0 {
		return num(tenths/10) + "m"
	}
	return num(math.Floor(tenths/10)) + "." + num(math.Mod(tenths, 10)) + "m"
}

// stillBlocked is whether a blocked report of child's, in t's transcript,
// still stands: the child's run waits on a request, and the report is the
// last the run made of one. A run that asked twice made two; the first was
// answered. A remote child's placeholder holds no requests: its child
// waits on one on its machine while the placeholder says it waits.
func stillBlocked(t, child thread, r client.ReportEntry) bool {
	waits := slices.ContainsFunc(child.View.Requests, func(q api.Request) bool { return q.Run == r.Run })
	if p := remote(child); p != nil {
		waits = child.View.Run == api.RunWaiting && p.Run == r.Run
	}
	if !waits || t.Transcript == nil {
		return false
	}
	for i := len(t.Transcript.Entries) - 1; i >= 0; i-- {
		if e, ok := t.Transcript.Entries[i].(*client.ReportEntry); ok && e.Child == r.Child && e.Run == r.Run && e.Kind == api.ReportBlocked {
			return e.At.Equal(r.At)
		}
	}
	return false
}

// timelineReport is the context of timeline.report: which card a copy is,
// how many reports it holds, and how they went to the parent's agent. The
// reports themselves are the native report.body's.
type timelineReport struct {
	Key     string `json:"key"`
	Reports int    `json:"reports"`
	Wake    string `json:"wake,omitempty"`
}

// reportCard is the children's reports in the parent's timeline: each
// one's state, a line or two of what came of it, and a way into it. A
// line under it says how the reports went to the parent's agent.
func (a *app) reportCard(c *ui.Context, t thread, key string, e entry) {
	m := timelineReport{Key: key, Reports: len(e.reports), Wake: wakeLine(t, e.reports)}
	a.pluginComponent(c, "timeline.report", &t, componentCopy{
		key: key,
		box: func() ui.Element {
			return ui.Column(c).Key(key).Transition(riseIn)
		},
		context: func(plugins.ThreadRecord) any { return m },
		native:  func(body *widget.Node) { a.reports(c, t, e) },
		builtin: func() *widget.Node { return reportCardRow(m) },
	})
}

// reportCardRow is the built-in Report card plugin's drawing of the card
// m, as its main.js draws it. Glitched draws the same while no plugin has
// drawn the card, so that the native body keeps its state.
func reportCardRow(m timelineReport) *widget.Node {
	children := []*widget.Node{{Type: "native", ID: "body", Name: "report.body"}}
	if m.Wake != "" {
		children = append(children, &widget.Node{Type: "text", ID: "wake", Text: m.Wake, FontSize: 11.5, Color: "$text.faint", Padding: widget.Spacing{0, 0, 0, 14}})
	}
	return &widget.Node{Type: "column", Gap: 6, Children: children}
}

// wakeLine is how a card's reports went to the parent's agent: waiting
// for its next turn, which after a stop is the user's next message, sent,
// or not sent. A card with no report that wakes the agent has none.
func wakeLine(t thread, rs []client.ReportEntry) string {
	var waiting, sent, lost bool
	for _, r := range rs {
		waiting, sent, lost = waiting || r.Wake == client.WakeWaiting, sent || r.Wake == client.WakeSent, lost || r.Wake == client.WakeLost
	}
	v := t.View
	switch {
	case waiting && v.Resumable:
		return "↳ Goes to the agent with your next message"
	case waiting:
		return "↳ Goes to the agent after this turn"
	case sent:
		return "↳ Sent to the agent"
	case lost:
		return "↳ Not sent to the agent"
	}
	return ""
}

func (a *app) reports(c *ui.Context, t thread, e entry) {
	ui.Column(c).Radius(12).Border(1, palette.border).Background(palette.bg.Alpha(0.6)).Padding(4).Gap(2).Children(func() {
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
			case n.Kind == api.ReportBlocked && found && stillBlocked(t, child, n) && remote(child) != nil:
				icon, col, state = icHand, palette.amber, "Needs you on "+a.remoteLabel(child)
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
				a.choose(child)
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
