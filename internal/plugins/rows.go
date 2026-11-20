package plugins

import (
	"encoding/json"
	"errors"
	"fmt"

	"glitched/internal/plugins/widget"
)

// MaxRowBatch is how many rows of a list one rows call draws.
const MaxRowBatch = 200

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
	rev   int
	// want is the worker's, under the session's mu: rows to draw.
	want map[rowKey]*rowWant
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
	}
	k := at.key()
	if rs.shown[k] == nil {
		rs.shown[k] = map[string]bool{}
		rs.asked[k] = map[string]string{}
	}
	rs.shown[k][it.Key] = true
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
	for k, rows := range rs.drawn {
		for key := range rows {
			if !rs.shown[k][key] {
				delete(rows, key)
			}
		}
	}
	if rs.drawn == nil {
		rs.drawn = map[rowKey]map[string]drawnRow{}
	}
	rs.asked, rs.shown, rs.rev = map[rowKey]map[string]string{}, map[rowKey]map[string]bool{}, rev
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
	for k, w := range want {
		rows := map[string]drawnRow{}
		drawn[k] = rows
		for keys := w.keys; len(keys) > 0 && failed == nil; {
			batch := keys[:min(len(keys), MaxRowBatch)]
			keys = keys[len(batch):]
			nodes, err := s.callRows(k.list, w.place, batch, state, context)
			if err != nil {
				failed = fmt.Errorf("rows of %s: %w", k.list, err)
				break
			}
			for i, key := range batch {
				rows[key] = drawnRow{nodes[i], w.rev, w.versions[key]}
			}
		}
	}
	s.h.opts.Post(func() {
		if s.closed || !s.current(gen) {
			return
		}
		if failed != nil {
			s.Err = failed
			return
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
		}
	})
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
	nodes := make([]*widget.Node, len(keys))
	for i, w := range out {
		if string(w) == "null" {
			continue
		}
		n, err := widget.DecodeRow(w)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", keys[i], err)
		}
		nodes[i] = n
	}
	return nodes, nil
}
