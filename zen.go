package main

import (
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/client"
)

// A zenFold is one line in Zen mode standing for a run's work in a turn:
// its work entries, plan cards and the replies in between.
type zenFold struct {
	key string
	// rows are the rows it folds, in order.
	rows          []int
	steps, failed int           // failed is the folded steps that failed
	worked        time.Duration // the time its work covers, once each
	working       bool
	paused        string
}

// A listItem is a row of the timeline, or a fold of rows, which row is -1
// for.
type listItem struct {
	row  int
	fold *zenFold
}

// A timelineView is what the timeline lists of its rows: each row as it
// is, or, in Zen mode, with each turn's work folded into a line.
type timelineView struct {
	es []entry
	// items are the list's, nil when each row is its own; at is the item
	// that shows each row, its fold's while the fold is closed.
	items []listItem
	at    []int
}

func (v timelineView) len() int {
	if v.items == nil {
		return len(v.es)
	}
	return len(v.items)
}

// key is the list's key for item j.
func (v timelineView) key(j int) any {
	if f := v.fold(j); f != nil {
		return f.key
	}
	return v.es[v.row(j)].key
}

// row is the row item j shows, -1 for a fold.
func (v timelineView) row(j int) int {
	if v.items == nil {
		return j
	}
	return v.items[j].row
}

func (v timelineView) fold(j int) *zenFold {
	if v.items == nil {
		return nil
	}
	return v.items[j].fold
}

// pos is the item that shows row i, or its fold while the fold is closed;
// -1 for no row.
func (v timelineView) pos(i int) int {
	switch {
	case i < 0 || i >= len(v.es):
		return -1
	case v.items == nil:
		return i
	}
	return v.at[i]
}

// anchor is the row item j shows, or a fold's first.
func (v timelineView) anchor(j int) int {
	if f := v.fold(j); f != nil {
		return f.rows[0]
	}
	return v.row(j)
}

// thinkingPos is the item the thinking row goes under when it goes under
// row at: the last that starts at or before it; -1 for no row.
func (v timelineView) thinkingPos(at int) int {
	switch {
	case at < 0:
		return -1
	case v.items == nil:
		return at
	case at >= len(v.es):
		return -1
	}
	return sort.Search(len(v.items), func(j int) bool { return v.anchor(j) > at }) - 1
}

// viewOf is how the timeline lists es: in Zen mode, folded.
func (a *app) viewOf(es []entry) timelineView {
	if !a.settings.zen {
		return timelineView{es: es}
	}
	return zenView(es, a.zenOpen)
}

// A timelineMemo is what a thread's timeline keeps from frame to frame:
// the view it listed last, and the Zen view of its rows with what that
// was built from, the rows' change and the lines open, to build it again
// only once they change.
type timelineMemo struct {
	listed timelineView
	zen    timelineView
	change uint64
	first  *entry
	n      int
	open   uint64
}

func (a *app) memoOf(id api.ID) *timelineMemo {
	m := a.listed[id]
	if m == nil {
		m = &timelineMemo{}
		a.listed[id] = m
	}
	return m
}

// viewFor is how the timeline lists the thread's rows, as viewOf, built
// again in Zen mode only as the rows or the lines open change.
func (a *app) viewFor(id api.ID, rs rows) timelineView {
	es := rs.es
	if !a.settings.zen || len(es) == 0 {
		return timelineView{es: es}
	}
	m := a.memoOf(id)
	if m.zen.items == nil || m.change != rs.change || m.first != &es[0] || m.n != len(es) || m.open != a.zenOpened {
		m.zen, m.change, m.first, m.n, m.open = zenView(es, a.zenOpen), rs.change, &es[0], len(es), a.zenOpened
	}
	return m.zen
}

// listReach is how far from where it was MyGo's list looks for the key of
// the item at its top as its items change (its listSearch).
const listReach = 1000

// keepPlace keeps the thread's timeline l on what showed at its top as
// the view changes to v, as Zen mode turning on or off, a line opening or
// closing, or a reply folding as work comes after it does. The list keeps
// its place by the key of the item at its top, which v may no longer
// list, or list too far from where it was for the list to find: it
// scrolls to the item that shows that row in v, its line while folded.
// At the end, the list follows the end.
func (a *app) keepPlace(id api.ID, l *ui.ListState, v timelineView) {
	m := a.memoOf(id)
	was := m.listed
	m.listed = v
	if sameItems(was, v) || l.AtEnd() {
		return
	}
	first, last := l.Visible()
	if first > last || first >= was.len() {
		return
	}
	key, row := was.key(first), was.anchor(first)
	i := row
	if i >= len(v.es) || v.es[i].key != was.es[row].key {
		k := was.es[row].key
		if i = slices.IndexFunc(v.es, func(e entry) bool { return e.key == k }); i < 0 {
			return
		}
	}
	// An open fold's line is just above its first row.
	j := v.pos(i)
	for _, k := range []int{j, j - 1} {
		if k >= 0 && v.key(k) == key && max(k-first, first-k) <= listReach {
			return
		}
	}
	l.ScrollTo(j, ui.Start)
}

// sameItems is whether a and b list the same items: both each row as it
// is, or the same Zen view.
func sameItems(a, b timelineView) bool {
	switch {
	case a.items == nil || b.items == nil:
		return a.items == nil && b.items == nil
	case len(a.items) == 0 || len(b.items) == 0:
		return len(a.items) == len(b.items)
	}
	return len(a.items) == len(b.items) && &a.items[0] == &b.items[0]
}

// zenLine is a Zen fold's line: how its run's work went, quiet, with no
// card, at the work card head's inset. A click opens the fold under it,
// or closes it, with the line where it was.
func (a *app) zenLine(c *ui.Context, t thread, f *zenFold) {
	open := a.zenOpen[f.key]
	ui.Row(c).Key(f.key).AlignItems(ui.Center).Radius(6).Transition(riseIn).Children(func() {
		if ghost(c, "head", false).Padding(8, 12, 8, 13).Grow(1).Children(func() {
			switch {
			case f.paused != "":
				ui.Icon(c, icClock).FontSize(12).TextColor(palette.amber)
				ui.Text(c, f.paused).FontSize(12).Grow(1)
			case f.working:
				a.statusMark(c, statusWorking)
				shimmerText(c, "Working…", 12, palette.muted, palette.text)
				ui.Spacer(c)
			default:
				took := "Worked"
				if f.worked > 0 {
					took = "Worked for " + f.worked.Round(time.Second).String()
				}
				ui.Text(c, took).FontSize(12).Grow(1)
			}
			if f.steps > 0 {
				ui.Text(c, plural(f.steps, "step")).FontSize(11).TextColor(palette.faint)
			}
			if f.failed > 0 {
				ui.Text(c, fmt.Sprintf("%d failed", f.failed)).FontSize(11).TextColor(palette.red)
			}
			chevron(c, open)
		}).Clicked() {
			if open {
				delete(a.zenOpen, f.key)
			} else {
				a.zenOpen[f.key] = true
			}
			a.zenOpened++
			a.listFor(t.id()).HoldPlace()
		}
	})
}

// foldable is whether a row of the kind can fold into a Zen line.
func foldable(k entryKind) bool { return k == entryWork || k == entryPlan || k == entryAssistant }

// zenView folds, in each turn, the work, plans and replies of each of its
// runs into a line, but for the run's final reply, the reply it's writing,
// and the reply that explains a request waiting. A run with no work or plan folds nothing. The
// folds open says are open list their rows under their lines.
func zenView(es []entry, open map[string]bool) timelineView {
	v := timelineView{es: es, items: make([]listItem, 0, len(es)), at: make([]int, len(es))}
	for start, end := 0, 0; start < len(es); start = end {
		end = start + 1
		for end < len(es) && es[end].kind != entryUser {
			end++
		}
		v.addTurn(start, end, open)
	}
	return v
}

// addTurn lists the rows from start to end, a turn's, or those before the
// first message.
func (v *timelineView) addTurn(start, end int, open map[string]bool) {
	es := v.es
	runs := map[api.ID][]int{}
	for i := start; i < end; i++ {
		if foldable(es[i].kind) {
			runs[es[i].run] = append(runs[es[i].run], i)
		}
	}
	// A request waiting keeps the reply that explains it, its run's last
	// before it, and its run's line says what it waits on.
	kept := map[int]bool{}
	paused := map[api.ID]string{}
	for k := start; k < end; k++ {
		r := es[k].request
		if r == nil || r.State != api.RequestPending {
			continue
		}
		paused[r.Run] = pausedFor[r.Kind != api.KindApproval]
		for i := k - 1; i >= start; i-- {
			if e := es[i]; e.kind == entryAssistant && e.run == r.Run {
				kept[i] = true
				break
			} else if e.request != nil && e.request.Run == r.Run {
				break
			}
		}
	}
	of := make([]*zenFold, end-start)
	for r, rows := range runs {
		final := func(i int) bool { return es[i].kind == entryAssistant && !es[i].turn.IsZero() }
		last := rows[len(rows)-1]
		keepLast := es[last].kind == entryAssistant
		for _, i := range rows {
			if final(i) {
				keepLast = false
			}
		}
		f := &zenFold{key: "z/" + r.String() + "/" + es[start].key, paused: paused[r]}
		work := false
		for _, i := range rows {
			if final(i) || keepLast && i == last || kept[i] {
				continue
			}
			f.rows = append(f.rows, i)
			work = work || es[i].kind != entryAssistant
		}
		if !work {
			continue
		}
		var spans [][2]time.Time
		for _, i := range f.rows {
			of[i-start] = f
			if e := es[i]; e.kind == entryWork {
				f.steps += len(e.work)
				f.failed += failedSteps(e.work)
				f.working = f.working || e.working
				if !e.started.IsZero() {
					spans = append(spans, [2]time.Time{e.started, e.ended})
				}
			}
		}
		f.worked = covered(spans)
	}
	lines := map[*zenFold]int{}
	for i := start; i < end; i++ {
		f := of[i-start]
		if f != nil && i == f.rows[0] {
			lines[f] = len(v.items)
			v.items = append(v.items, listItem{row: -1, fold: f})
		}
		if f != nil && !open[f.key] {
			v.at[i] = lines[f]
			continue
		}
		v.at[i] = len(v.items)
		v.items = append(v.items, listItem{row: i})
	}
}

// covered is how long the spans cover, counting a time two of them share
// once.
func covered(spans [][2]time.Time) time.Duration {
	slices.SortFunc(spans, func(x, y [2]time.Time) int { return x[0].Compare(y[0]) })
	var d time.Duration
	var end time.Time
	for _, s := range spans {
		if s[0].After(end) {
			end = s[0]
		}
		if s[1].After(end) {
			d += s[1].Sub(end)
			end = s[1]
		}
	}
	return d
}

func failedSteps(ws []client.WorkStep) int {
	n := 0
	for _, w := range ws {
		if w.Status == api.WorkFailed {
			n++
		}
	}
	return n
}
