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
	// The UI thread's: drawn rows, the view each was asked for, and the
	// rows asked of Row since the view changed, which keep their drawing.
	drawn map[rowKey]map[string]drawnRow
	asked map[rowKey]map[string]string
	shown map[rowKey]map[string]bool
	// wasShown are those asked before the last change: a view that
	// changes partway through a frame leaves the rows the frame asked
	// for before it there.
	wasShown map[rowKey]map[string]bool
	rev      int
	// seen stamps each row as a list last asked for it: past MaxRowsKept
	// drawn, a list keeps those it asked for last.
	seen  map[rowKey]map[string]int
	stamp int
	// The worker's, under the session's mu: rows to draw, and the state
	// and context of the last view drawn, which rows are drawn with: a
	// view refused leaves its rows as they were.
	want    map[rowKey]*rowWant
	state   json.RawMessage
	context any
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
	if rs.drawn == nil || rs.rev != s.Rev {
		rs.forget(s.Rev)
		s.dropWants()
	}
	k := at.key()
	if rs.shown[k] == nil {
		rs.shown[k] = map[string]bool{}
		rs.asked[k] = map[string]string{}
	}
	rs.shown[k][it.Key] = true
	rs.stamp++
	if rs.seen[k] == nil {
		rs.seen[k] = map[string]int{}
	}
	rs.seen[k][it.Key] = rs.stamp
	d, ok := rs.drawn[k][it.Key]
	if ok && d.rev == s.Rev && d.version == it.Version {
		return d.node
	}
	if v, asked := rs.asked[k][it.Key]; !asked || v != it.Version {
		rs.asked[k][it.Key] = it.Version
		s.wantRow(k, at.Place, it)
	}
	return d.node
}

// forget drops, as the view changes to rev, the rows no list asked for
// since the last change: a list keeps the drawings of the rows it shows.
func (rs *rowSet) forget(rev int) {
	seen := map[rowKey]map[string]int{}
	for k, rows := range rs.drawn {
		for key := range rows {
			if !rs.shown[k][key] && !rs.wasShown[k][key] {
				delete(rows, key)
				continue
			}
			if seen[k] == nil {
				seen[k] = map[string]int{}
			}
			seen[k][key] = rs.seen[k][key]
		}
		if len(rows) == 0 {
			delete(rs.drawn, k)
		}
	}
	if rs.drawn == nil {
		rs.drawn = map[rowKey]map[string]drawnRow{}
	}
	rs.seen = seen
	rs.wasShown = rs.shown
	rs.asked, rs.shown, rs.rev = map[rowKey]map[string]string{}, map[rowKey]map[string]bool{}, rev
}

// dropWants drops the rows asked for an earlier view that the worker
// hasn't taken yet: the rows the new view shows are asked again.
func (s *Session) dropWants() {
	s.mu.Lock()
	defer s.mu.Unlock()
	maps.DeleteFunc(s.rows.want, func(_ rowKey, w *rowWant) bool { return w.rev < s.Rev })
}

func (s *Session) wantRow(k rowKey, place *PlaceRef, it widget.ListItem) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done || s.broken {
		return
	}
	if s.rows.want == nil {
		s.rows.want = map[rowKey]*rowWant{}
	}
	w := s.rows.want[k]
	if w == nil {
		w = &rowWant{versions: map[string]string{}}
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
		rs := &s.rows
		if rs.drawn == nil {
			rs.forget(s.Rev)
		}
		for k, rows := range drawn {
			if rs.drawn[k] == nil {
				rs.drawn[k] = map[string]drawnRow{}
			}
			for key, d := range rows {
				rs.drawn[k][key] = d
			}
			rs.keep(k)
		}
	})
}

// keep drops the rows of list k a list asked for longest ago, past
// MaxRowsKept, down to half of it: scrolling a long list keeps the rows it
// showed last, not every row it went by.
func (rs *rowSet) keep(k rowKey) {
	rows := rs.drawn[k]
	if len(rows) <= MaxRowsKept {
		return
	}
	keys := slices.Collect(maps.Keys(rows))
	slices.SortFunc(keys, func(a, b string) int { return rs.seen[k][b] - rs.seen[k][a] })
	for _, key := range keys[MaxRowsKept/2:] {
		delete(rows, key)
		delete(rs.asked[k], key)
		delete(rs.seen[k], key)
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
