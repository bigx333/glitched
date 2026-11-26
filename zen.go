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

// viewOf is how the thread's timeline lists es: in Zen mode, folded.
func (a *app) viewOf(id api.ID, es []entry) timelineView {
	if !a.settings.zen {
		return timelineView{es: es}
	}
	var open map[string]bool
	if m := a.listed[id]; m != nil {
		open = m.open
	}
	return zenView(es, open)
}

// A timelineMemo is what a thread's timeline keeps from frame to frame:
// the view it listed last, the Zen lines open, by key, with a count of
// their changes, and the Zen view of its rows with what that was built
// from, the rows' change and the lines open, to build it again only once
// they change, and its turns, whose folds it keeps while their rows stay
// the same.
type timelineMemo struct {
	listed timelineView
	// top is the place at the top of what it listed, noted before the
	// rows change, as noted says.
	top    place
	noted  bool
	open   map[string]bool
	opened uint64
	zen    timelineView
	change uint64
	first  *entry
	n      int
	built  uint64 // opened, as the view was built
	turns  []zenTurn
	epoch  uint64
	// disclosures are the work logs and plans the user opened or closed,
	// by key, against how they start.
	disclosures map[string]disclosureOverride
}

// A disclosureOverride is how the user left a work log or plan: as it
// starts, open or closed.
type disclosureOverride uint8

const (
	disclosureAuto disclosureOverride = iota
	disclosureOpen
	disclosureClosed
)

// disclosureOpen is whether the work log or plan with the key is open,
// which it starts as auto says.
func (m *timelineMemo) disclosureOpen(key string, auto bool) bool {
	switch m.disclosures[key] {
	case disclosureOpen:
		return true
	case disclosureClosed:
		return false
	}
	return auto
}

// toggleDisclosure opens the work log or plan with the key, or closes it;
// back as it starts, it forgets it.
func (m *timelineMemo) toggleDisclosure(key string, auto bool) {
	open := !m.disclosureOpen(key, auto)
	if open == auto {
		delete(m.disclosures, key)
		return
	}
	if m.disclosures == nil {
		m.disclosures = map[string]disclosureOverride{}
	}
	m.disclosures[key] = disclosureClosed
	if open {
		m.disclosures[key] = disclosureOpen
	}
}

// toggle opens the line with the key, or closes it.
func (m *timelineMemo) toggle(key string) {
	if m.open[key] {
		delete(m.open, key)
	} else if m.open == nil {
		m.open = map[string]bool{key: true}
	} else {
		m.open[key] = true
	}
	m.opened++
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
	if m.zen.items != nil && m.change == rs.change && m.first == &es[0] && m.n == len(es) && m.built == m.opened {
		return m.zen
	}
	if m.epoch != rs.epoch {
		m.turns = nil
	}
	m.zen, m.turns = zenViewKeeping(es, m.open, m.turns, rs.stable, rs.asks)
	m.change, m.first, m.n, m.built, m.epoch = rs.change, &es[0], len(es), m.opened, rs.epoch
	return m.zen
}

// A place is where a listed view showed at its top: the item there, by
// index and key, and the row it shows, or its fold's first, by index and
// key; ok is whether it showed one.
type place struct {
	ok    bool
	first int
	key   any
	at    int
	row   string
}

// top is the place at the top of l, which listed v.
func (v timelineView) top(l *ui.ListState) place {
	first, last := l.Visible()
	if first > last || first >= v.len() {
		return place{}
	}
	i := v.anchor(first)
	return place{ok: true, first: first, key: v.key(first), at: i, row: v.es[i].key}
}

// notePlace notes the place at the top of the thread's timeline as its
// rows are about to change: the rows the timeline listed share their array
// with those derived again, which write over them.
func (a *app) notePlace(id api.ID) {
	m := a.listed[id]
	if m == nil || m.noted {
		return
	}
	m.top, m.noted = place{}, true
	if l := a.lists[id]; l != nil {
		m.top = m.listed.top(l)
	}
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
	was, top := m.listed, m.top
	if !m.noted {
		top = was.top(l)
	}
	m.listed, m.top, m.noted = v, place{}, false
	if sameItems(was, v) || l.AtEnd() || !top.ok {
		return
	}
	i := top.at
	if i >= len(v.es) || v.es[i].key != top.row {
		if i = slices.IndexFunc(v.es, func(e entry) bool { return e.key == top.row }); i < 0 {
			return
		}
	}
	// An open fold's line is just above its first row.
	j := v.pos(i)
	for _, k := range []int{j, j - 1} {
		if k >= 0 && v.key(k) == top.key && max(k-top.first, top.first-k) <= listReach {
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
	m := a.memoOf(t.id())
	open := m.open[f.key]
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
			m.toggle(f.key)
			a.listFor(t.id()).HoldPlace()
		}
	})
}

// foldable is whether a row of the kind can fold into a Zen line.
func foldable(k entryKind) bool { return k == entryWork || k == entryPlan || k == entryAssistant }

// zenView folds, in each turn, the work, plans and replies of each of its
// runs into a line, but for the run's final reply, the reply it's writing,
// and the reply that explains a request waiting. A run with no work or
// plan folds nothing. The folds open says are open list their rows under
// their lines.
func zenView(es []entry, open map[string]bool) timelineView {
	v, _ := zenViewKeeping(es, open, nil, 0, addAsks(nil, es, 0))
	return v
}

// A zenTurn is a turn's folds, which its rows, from start to end, decide,
// and whether they were fixed as it was folded: its rows stable, and no
// run of them waiting.
type zenTurn struct {
	start, end int
	fixed      bool
	folds      []*zenFold
}

// zenViewKeeping is zenView's, with asks the rows of the requests waiting,
// and the turns it folded. It keeps the folds of the turns that were fixed
// as they were folded and still are: they end by row stable, and before a
// run that waits, whose rows its requests fold otherwise.
func zenViewKeeping(es []entry, open map[string]bool, kept []zenTurn, stable int, asks []int) (timelineView, []zenTurn) {
	w := waitsOf(es, asks)
	stable = min(stable, w.from)
	v := timelineView{es: es, items: make([]listItem, 0, len(es)), at: make([]int, len(es))}
	n := 0
	for start, end := 0, 0; start < len(es); start = end {
		end = start + 1
		for end < len(es) && es[end].kind != entryUser {
			end++
		}
		t := zenTurn{start: start, end: end, fixed: end <= stable}
		if n < len(kept) && kept[n].start == start && kept[n].end == end && kept[n].fixed && t.fixed {
			t.folds = kept[n].folds
		} else {
			t.folds = turnFolds(es, start, end, &w)
		}
		if n < len(kept) {
			kept[n] = t
		} else {
			kept = append(kept, t)
		}
		n++
		v.addTurn(t, open)
	}
	return v, kept[:n]
}

// addTurn lists a turn's rows, or those before the first message, each
// fold's line over its first.
func (v *timelineView) addTurn(t zenTurn, open map[string]bool) {
	// Until a row is listed, at holds its fold's index plus one.
	for k, f := range t.folds {
		for _, i := range f.rows {
			v.at[i] = k + 1
		}
	}
	for i := t.start; i < t.end; i++ {
		if k := v.at[i]; k > 0 {
			f := t.folds[k-1]
			switch {
			case i == f.rows[0]:
				v.at[i] = len(v.items)
				v.items = append(v.items, listItem{row: -1, fold: f})
				if !open[f.key] {
					continue
				}
			case !open[f.key]:
				v.at[i] = v.at[f.rows[0]]
				continue
			}
		}
		v.at[i] = len(v.items)
		v.items = append(v.items, listItem{row: i})
	}
}

// zenWaits is what the requests waiting change of their runs' folds, in
// any turn of the run: the replies that explain them, each its run's last
// before it, which show, and what each run's lines say it waits on, the
// last request's. from is the first row of a run that waits, its message.
type zenWaits struct {
	kept   []int
	paused []zenPause
	from   int
}

type zenPause struct {
	run  api.ID
	says string
}

// waitsOf is what the requests of the rows asks change, looking back from
// each no further than its run's message.
func waitsOf(es []entry, asks []int) zenWaits {
	w := zenWaits{from: len(es)}
	for _, k := range asks {
		r := es[k].request
		w.paused = append(w.paused, zenPause{r.Run, pausedFor[r.Kind != api.KindApproval]})
		message := "m/" + r.Run.String()
		reply, i := -1, k-1
		for ; i >= 0 && (es[i].kind != entryUser || es[i].key != message); i-- {
			if reply < 0 && es[i].kind == entryAssistant && es[i].run == r.Run {
				reply = i
			}
		}
		if reply >= 0 {
			w.kept = append(w.kept, reply)
		}
		w.from = min(w.from, max(i, 0))
	}
	return w
}

// pausedOf is what the run's lines say it waits on, "" for nothing.
func (w *zenWaits) pausedOf(r api.ID) string {
	says := ""
	for _, p := range w.paused {
		if p.run == r {
			says = p.says
		}
	}
	return says
}

// turnFolds are the folds of the runs of the turn's rows, from start to
// end, as the requests waiting change them.
func turnFolds(es []entry, start, end int, w *zenWaits) []*zenFold {
	runs := make([]api.ID, 0, 4)
	for i := start; i < end; i++ {
		if r := es[i].run; foldable(es[i].kind) && !slices.Contains(runs, r) {
			runs = append(runs, r)
		}
	}
	folds := make([]*zenFold, 0, len(runs))
	for _, r := range runs {
		// The run's last reply shows if it's being written or asks, unless
		// the run has its final reply, which shows.
		last, n, final := -1, 0, false
		for i := start; i < end; i++ {
			if e := es[i]; e.run == r && foldable(e.kind) {
				last, n = i, n+1
				final = final || e.kind == entryAssistant && !e.turn.IsZero()
			}
		}
		keepLast := es[last].kind == entryAssistant && !final
		f := &zenFold{key: "z/" + r.String() + "/" + es[start].key, rows: make([]int, 0, n)}
		work := false
		for i := start; i < end; i++ {
			e := es[i]
			if e.run != r || !foldable(e.kind) || e.kind == entryAssistant && !e.turn.IsZero() || keepLast && i == last || slices.Contains(w.kept, i) {
				continue
			}
			f.rows = append(f.rows, i)
			work = work || e.kind != entryAssistant
		}
		if !work {
			continue
		}
		f.paused = w.pausedOf(r)
		spans := make([][2]time.Time, 0, 8)
		for _, i := range f.rows {
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
		folds = append(folds, f)
	}
	return folds
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
		if stepFailed(w) {
			n++
		}
	}
	return n
}
