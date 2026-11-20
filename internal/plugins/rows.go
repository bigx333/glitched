package plugins

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"

	"glitched/internal/plugins/widget"
)

// The bounds of a list's rows: how many one rows call draws, and how many
// a list keeps drawn, each at most widget.MaxRowBytes.
const (
	MaxRowBatch = 200
	MaxRowsKept = 1000
)

// RowAt names a list whose rows a session draws: the list's id in a page or
// tab, and for a list in a copy of a place, that copy.
type RowAt struct {
	List  string
	Place *PlaceRef
}

// RowRef names the row of a list a widget was in, as update gets it.
type RowRef struct {
	List string `json:"list"`
	Key  string `json:"key"`
}

type rowKey struct{ place, copy, list string }

func (at RowAt) key() rowKey {
	if at.Place == nil {
		return rowKey{list: at.List}
	}
	return rowKey{at.Place.ID, at.Place.Key, at.List}
}

// rowSet is what a session's lists show: the rows the plugin drew, by list
// and key, each with the view it was drawn for. A row drawn for an earlier
// view shows till it is drawn again, so rows never flicker or wait.
type rowSet struct {
	// The UI thread's: each list in a view shown, and the view the rows
	// asked of the worker were last checked against.
	lists map[rowKey]*listRows
	rev   int
	// stamp counts the rows asked of Row, which stamps each as its list's.
	stamp int
	// The worker's, under the session's mu: rows to draw, and the state
	// and context of the last view drawn, which rows are drawn with: a
	// view refused leaves its rows as they were.
	want    map[rowKey]*rowWant
	state   json.RawMessage
	context any
}

// listRows is one list's rows: those drawn, and, since the view changed
// as the list last saw it, those it asked of Row and the version it last
// asked for, which is the one a row drawn null must be for. Only the
// list's own next read in a new view drops a row it no longer shows, so
// other lists drawn meanwhile never take its rows away.
type listRows struct {
	drawn map[string]drawnRow
	asked map[string]string
	shown map[string]bool
	rev   int
	// seen stamps each row as the list last asked for it: past
	// MaxRowsKept drawn, a list keeps those it asked for last.
	seen map[string]int
}

// drewView records, on the worker, what the view it drew was drawn with.
func (rs *rowSet) drewView(mu *sync.Mutex, state json.RawMessage, context any) {
	mu.Lock()
	rs.state, rs.context = state, context
	mu.Unlock()
}

type drawnRow struct {
	node    *widget.Node
	rev     int
	version string
}

type rowWant struct {
	list     *listRows // the rows go to this list alone, not one made since
	place    *PlaceRef
	rev      int
	keys     []string
	versions map[string]string
}

// Row is the row of the list's item as the plugin drew it, nil before it
// did. A row not drawn for the session's view and the item's version is
// asked of the plugin, once, and its last drawing shows meanwhile. Only the
// rows asked for are drawn: a list asks for those in view.
func (s *Session) Row(at RowAt, it widget.ListItem) *widget.Node {
	rs := &s.rows
	if rs.lists == nil {
		rs.lists = map[rowKey]*listRows{}
	}
	if rs.rev != s.Rev {
		rs.rev = s.Rev
		s.dropWants()
	}
	k := at.key()
	l := rs.lists[k]
	if l == nil {
		l = &listRows{drawn: map[string]drawnRow{}, seen: map[string]int{}}
		rs.lists[k] = l
	}
	if l.shown == nil || l.rev != s.Rev {
		l.forget(s.Rev)
	}
	l.shown[it.Key] = true
	rs.stamp++
	l.seen[it.Key] = rs.stamp
	d, ok := l.drawn[it.Key]
	if ok && d.rev == s.Rev && d.version == it.Version {
		l.asked[it.Key] = it.Version
		return d.node
	}
	if v, asked := l.asked[it.Key]; !asked || v != it.Version {
		l.asked[it.Key] = it.Version
		s.wantRow(k, l, at.Place, it)
	}
	return d.node
}

// forget drops, as the list reads the view rev, the rows it didn't ask for
// since it last read another: it keeps the drawings of the rows it shows.
func (l *listRows) forget(rev int) {
	maps.DeleteFunc(l.drawn, func(key string, _ drawnRow) bool { return !l.shown[key] })
	maps.DeleteFunc(l.seen, func(key string, _ int) bool { return !l.shown[key] })
	l.asked, l.shown, l.rev = map[string]string{}, map[string]bool{}, rev
}

// dropLists drops, as a view is shown under the session's mu, the rows of
// the lists no view shown has: the session's, or a copy's of a place. A
// copy shown by a drawing of an older version than the UI's keeps its
// lists till the drawing of its own version shows.
func (s *Session) dropLists() {
	rs := &s.rows
	live := map[rowKey]bool{}
	lists := func(place, copy string, n *widget.Node) {
		n.Walk(func(n *widget.Node) {
			if n.Type == "list" {
				live[rowKey{place, copy, n.ID}] = true
			}
		})
	}
	if s.View != nil {
		lists("", "", s.View.Body)
	}
	if s.places != nil {
		for id, views := range s.places.views {
			for key, n := range views {
				lists(id, key, n)
			}
		}
	}
	maps.DeleteFunc(rs.lists, func(k rowKey, _ *listRows) bool {
		if live[k] || k.place == "" {
			return !live[k]
		}
		i := slices.IndexFunc(s.places.items[k.place], func(it PlaceItem) bool { return it.Key == k.copy })
		return i < 0 || s.places.answered[[2]string{k.place, k.copy}] == s.places.items[k.place][i].Version
	})
}

// dropWants drops the rows asked for an earlier view that the worker
// hasn't taken yet: the rows the new view shows are asked again.
func (s *Session) dropWants() {
	s.mu.Lock()
	defer s.mu.Unlock()
	maps.DeleteFunc(s.rows.want, func(_ rowKey, w *rowWant) bool { return w.rev < s.Rev })
}

func (s *Session) wantRow(k rowKey, l *listRows, place *PlaceRef, it widget.ListItem) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done || s.broken {
		return
	}
	if s.rows.want == nil {
		s.rows.want = map[rowKey]*rowWant{}
	}
	w := s.rows.want[k]
	if w == nil || w.list != l {
		w = &rowWant{list: l, versions: map[string]string{}}
		s.rows.want[k] = w
	}
	if _, ok := w.versions[it.Key]; !ok {
		w.keys = append(w.keys, it.Key)
	}
	w.place, w.rev, w.versions[it.Key] = place, s.Rev, it.Version
	if !s.running {
		s.running = true
		go s.run()
	}
}

// take is the rows to draw, nil for none, under the session's mu.
func (rs *rowSet) take() map[rowKey]*rowWant {
	w := rs.want
	rs.want = nil
	return w
}

// drawRows draws the rows wanted with the rows export, a batch at a time,
// and publishes them, or what failed.
func (s *Session) drawRows(gen int, state json.RawMessage, context any, want map[rowKey]*rowWant) {
	drawn := map[rowKey]map[string]drawnRow{}
	var failed error
	failedRev := 0 // the view the rows that failed were asked for
	for k, w := range want {
		rows := map[string]drawnRow{}
		drawn[k] = rows
		for keys := w.keys; len(keys) > 0; {
			batch := keys[:min(len(keys), MaxRowBatch)]
			keys = keys[len(batch):]
			nodes, err := s.callRows(k.list, w.place, batch, state, context)
			if err != nil && failed == nil {
				failedRev = w.rev
			}
			if err != nil && nodes == nil {
				// Its rows are asked again with the next view.
				failed = cmp.Or(failed, fmt.Errorf("rows of %s: %w", k.list, err))
				continue
			}
			failed = cmp.Or(failed, err)
			for i, key := range batch {
				rows[key] = drawnRow{nodes[i], w.rev, w.versions[key]}
			}
		}
	}
	s.h.opts.Post(func() {
		if s.closed || !s.current(gen) {
			return
		}
		// Rows that failed for an earlier view say nothing of this one's.
		if failed != nil && failedRev == s.Rev {
			s.Err = failed
		}
		for k, rows := range drawn {
			l := want[k].list
			if s.rows.lists[k] != l {
				continue // the list is gone
			}
			for key, d := range rows {
				// A row drawn for an earlier view or version than the list
				// last asked for never replaces a drawing: the last one
				// shows till the row's own is.
				v, asked := l.asked[key]
				current := d.rev == s.Rev && (!asked || v == d.version)
				if old := l.drawn[key]; !current && (d.node == nil || old.node != nil) {
					continue
				}
				l.drawn[key] = d
			}
			l.keep()
		}
	})
}

// keep drops the rows the list asked for longest ago, past MaxRowsKept,
// down to half of it: scrolling a long list keeps the rows it showed last,
// not every row it went by.
func (l *listRows) keep() {
	if len(l.drawn) <= MaxRowsKept {
		return
	}
	keys := slices.Collect(maps.Keys(l.drawn))
	slices.SortFunc(keys, func(a, b string) int { return l.seen[b] - l.seen[a] })
	for _, key := range keys[MaxRowsKept/2:] {
		// A row asked again since its drawing is still on its way.
		if d := l.drawn[key]; d.rev == l.rev && d.version == l.asked[key] {
			delete(l.asked, key)
		}
		delete(l.drawn, key)
		delete(l.seen, key)
	}
}

func (s *Session) callRows(list string, place *PlaceRef, keys []string, state json.RawMessage, context any) ([]*widget.Node, error) {
	if !s.runtime().Can("rows") {
		return nil, errors.New("the plugin has a list but no rows export")
	}
	args := map[string]any{"list": list, "keys": keys, "state": state}
	if place != nil {
		args["place"] = place
	}
	var raw json.RawMessage
	if err := s.call(ViewTimeout, "rows", s.args(args, context), &raw); err != nil {
		return nil, err
	}
	var out []json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil || len(out) != len(keys) {
		return nil, fmt.Errorf("rows answered %.40s…, not a list of %d widgets or nulls", raw, len(keys))
	}
	// A row that doesn't check draws nothing, and says why; the others
	// draw.
	nodes := make([]*widget.Node, len(keys))
	var failed error
	for i, w := range out {
		if string(w) == "null" {
			continue
		}
		n, err := widget.DecodeRow(w)
		if err != nil {
			failed = cmp.Or(failed, fmt.Errorf("rows of %s: %s: %w", list, keys[i], err))
			continue
		}
		nodes[i] = n
	}
	return nodes, failed
}
