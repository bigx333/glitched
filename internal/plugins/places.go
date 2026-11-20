package plugins

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"glitched/internal/plugins/widget"
)

// The bounds of places.
const (
	// MaxPlaceBatch is how many copies of a place one place call draws.
	MaxPlaceBatch = 200
	// MaxPlaceWidgets bounds one copy's tree: a row's badge, not a page.
	// A copy of a component, which is a part of the window, has
	// widget.MaxNodes.
	MaxPlaceWidgets = 200
)

// PlaceItem is one copy of a place to draw: its key (a thread's id for a
// row), a version that changes when its context does, and the context.
type PlaceItem struct {
	Key     string
	Version string
	Context any
}

// placeSet is what the app session draws in the plugin's places. The UI
// thread says which copies it shows (SetItems), and reads what was drawn
// (View); the worker draws them in batches. A copy keeps its last drawing
// while the next is drawn, so rows never flicker or wait.
type placeSet struct {
	places []Place
	// items, by place id, are the copies the UI shows; the worker reads
	// them under the session's mu.
	items map[string][]PlaceItem
	// views, by place id and key, are the UI thread's.
	views map[string]map[string]*widget.Node
	// Slow is the last draw's time, which Settings shows when it's long.
	Slow time.Duration
	// first is set while DrawFirst waits on the UI thread: each drawing
	// goes to it as well as being posted.
	first chan placeDraw
	// tried are the copies DrawFirst waited on, by place and key, with
	// their version: it waits once per version.
	tried map[[2]string]string
	// drawn is the worker's: each copy's last drawing, by place and key,
	// with the version it drew. A copy whose version is the same isn't
	// drawn again until something but the copies shown changes:
	// redrawAll, set under mu, says so.
	drawn     map[string]map[string]drawnCopy
	drawnGen  int
	redrawAll bool
	// drawSeq numbers the worker's drawings; shownSeq is the last one
	// published, so an older one never takes a newer one's place.
	drawSeq, shownSeq int
	// answered are the copies shown that a published drawing answered,
	// null included, by place and key, with the version it answered.
	answered map[[2]string]string
	// posted is the worker's last drawing, posted to the UI thread: one
	// DrawFirst waits on may be in it, not yet published.
	posted *placeDraw
}

// drawnCopy is a copy's drawing, nil when the plugin answered null.
type drawnCopy struct {
	version string
	node    *widget.Node
}

// App is the plugin id's app session, started if it isn't: one per
// plugin, which draws its places, and hears app events, timers and what
// the user does in its places. Nil for a plugin with none of those.
func (h *Host) App(id string) *Session {
	k := pageKey{plugin: id}
	if s := h.sessions[k]; s != nil {
		return s
	}
	in := h.running1(id)
	if in == nil {
		return nil
	}
	x := in.ext
	if len(x.Places) == 0 && len(x.Events) == 0 && len(x.Timers) == 0 {
		return nil
	}
	s := &Session{h: h, p: in.p, plugin: id, places: &placeSet{places: x.Places, items: map[string][]PlaceItem{}, views: map[string]map[string]*widget.Node{}}}
	h.sessions[k] = s
	s.push(queued{init: true})
	if x.Service == "" {
		h.startTimers(s, x.Timers) // a plugin with a service has its timers there
	}
	return s
}

// SetItems says which copies of the place id the UI shows now. It draws
// them again only when one came, went or changed its version.
func (s *Session) SetItems(place string, items []PlaceItem) {
	s.mu.Lock()
	old := s.places.items[place]
	// In the order drawn, frame after frame: compared side by side.
	same := slices.EqualFunc(old, items, func(o, it PlaceItem) bool { return o.Key == it.Key && o.Version == it.Version })
	if !same {
		s.places.items[place] = slices.Clone(items)
		// What was waited on or answered of a copy no longer shown is
		// forgotten with it.
		for _, m := range []map[[2]string]string{s.places.tried, s.places.answered} {
			maps.DeleteFunc(m, func(k [2]string, _ string) bool {
				return k[0] == place && !slices.ContainsFunc(items, func(it PlaceItem) bool { return it.Key == k[1] })
			})
		}
	}
	s.mu.Unlock()
	if !same {
		s.mu.Lock()
		gen := s.gen
		s.mu.Unlock()
		s.push(queued{gen: gen, refresh: true, items: true})
	}
}

// PlaceView is what the plugin drew for one copy of its place, or nil.
func (s *Session) PlaceView(place, key string) *widget.Node {
	return s.places.views[place][key]
}

// PlaceContext is the context a copy of the place was drawn with.
func (s *Session) PlaceContext(place, key string) any {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, it := range s.places.items[place] {
		if it.Key == key {
			return it.Context
		}
	}
	return nil
}

// Places are the plugin's places.
func (s *Session) Places() []Place { return s.places.places }

// renderPlaces draws every copy of every place, a batch at a time, with
// the plugin's place export, and publishes them with what failed.
func (s *Session) renderPlaces(gen, processed int, state json.RawMessage, failed error) {
	start := time.Now()
	drawn := map[string]map[string]*widget.Node{}
	asked := map[[2]string]string{}
	ps := s.places
	s.mu.Lock()
	if ps.redrawAll || ps.drawnGen != gen || ps.drawn == nil {
		ps.drawn, ps.drawnGen = map[string]map[string]drawnCopy{}, gen
	}
	ps.redrawAll = false
	s.mu.Unlock()
	for _, pl := range ps.places {
		s.mu.Lock()
		items := slices.Clone(ps.items[pl.ID])
		s.mu.Unlock()
		views := map[string]*widget.Node{}
		drawn[pl.ID] = views
		last := ps.drawn[pl.ID]
		kept := map[string]drawnCopy{}
		var todo []PlaceItem
		for _, it := range items {
			if p, ok := last[it.Key]; ok && p.version == it.Version {
				kept[it.Key] = p
				if p.node != nil {
					views[it.Key] = p.node
				}
			} else {
				todo = append(todo, it)
			}
			asked[[2]string{pl.ID, it.Key}] = it.Version
		}
		for len(todo) > 0 && failed == nil {
			batch := todo[:min(len(todo), MaxPlaceBatch)]
			todo = todo[len(batch):]
			if err := s.drawBatch(pl, state, batch, views); err != nil {
				failed = fmt.Errorf("place %s: %w", pl.ID, err)
				break
			}
			for _, it := range batch {
				kept[it.Key] = drawnCopy{it.Version, views[it.Key]}
			}
		}
		ps.drawn[pl.ID] = kept
	}
	if failed == nil {
		s.rows.drewView(&s.mu, state, nil)
	}
	ps.drawSeq++
	d := placeDraw{ps.drawSeq, gen, processed, drawn, asked, failed, time.Since(start)}
	s.mu.Lock()
	s.places.posted = &d
	if s.places.first != nil {
		select {
		case s.places.first <- d:
		default:
		}
	}
	s.mu.Unlock()
	s.h.opts.Post(func() { s.publish(d) })
}

// awaitsApp reports whether the plugin id's runtime is in a call into the
// app, which the UI thread answers.
func (h *Host) awaitsApp(id string) bool {
	in := h.running1(id)
	if in == nil {
		return false
	}
	h.mu.RLock()
	awaiting := in.awaiting
	h.mu.RUnlock()
	return awaiting != nil && awaiting.Load() > 0
}

// placeDraw is one drawing of every copy of the places, or what failed.
type placeDraw struct {
	seq, gen, processed int
	drawn               map[string]map[string]*widget.Node
	// asked are the copies it drew, by place and key, nulls included,
	// with the version drawn.
	asked  map[[2]string]string
	failed error
	took   time.Duration
}

// publish shows a drawing, on the UI thread.
func (s *Session) publish(d placeDraw) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || d.gen != s.gen || d.seq <= s.places.shownSeq {
		return // DrawFirst published it already, or a newer one is out
	}
	s.places.shownSeq = d.seq
	s.Err = d.failed
	s.places.Slow = d.took
	if d.failed != nil {
		// Keep the last good drawing, and what it says of the events
		// it took: its controls keep what the user typed.
		s.Busy = s.running && (len(s.queue) > 0 || s.dirty)
		return
	}
	for id, views := range d.drawn {
		// A copy drawn for the version shown has that drawing, none if
		// null; one shown or changed since the drawing began keeps its
		// last drawing till one is for its version.
		for _, it := range s.places.items[id] {
			k := [2]string{id, it.Key}
			if d.asked[k] == it.Version {
				if s.places.answered == nil {
					s.places.answered = map[[2]string]string{}
				}
				s.places.answered[k] = it.Version
			} else if n := s.places.views[id][it.Key]; n != nil && views[it.Key] == nil {
				views[it.Key] = n
			}
		}
		s.places.views[id] = views
	}
	s.Rev++
	s.dropLists()
	s.Processed = d.processed
	s.Busy = s.running && (len(s.queue) > 0 || s.dirty)
}

// DrawFirst draws the copies keys of the place before the UI shows them,
// those it has no drawing of yet, in one wait: on the UI thread, until the
// plugin drew them all (or answered null for them), it failed, or the
// deadline passed. A component drawn so never shows empty as it opens. It
// waits once per version of a copy, as items has it: a copy the plugin
// leaves undrawn doesn't stall every frame. items are the copies the frame
// has drawn so far; the others shown keep theirs, so drawing these never
// takes their drawings away.
func (s *Session) DrawFirst(place string, items []PlaceItem, keys []string, deadline time.Time) {
	// Room for the drawings the worker finishes while this one is read:
	// the one with these copies may come after another.
	first := make(chan placeDraw, 8)
	// A drawing the worker posted is newer than what is shown: what it
	// answered isn't waited on.
	s.mu.Lock()
	posted := s.places.posted
	newer := posted != nil && posted.seq > s.places.shownSeq
	s.mu.Unlock()
	if newer {
		s.publish(*posted)
	}
	s.mu.Lock()
	if s.broken || s.done {
		s.mu.Unlock()
		return
	}
	if s.places.tried == nil {
		s.places.tried = map[[2]string]string{}
	}
	// Each copy as items has it: one already answered at that version,
	// null included, isn't waited on.
	want := map[[2]string]string{}
	for _, key := range keys {
		i := slices.IndexFunc(items, func(it PlaceItem) bool { return it.Key == key })
		k := [2]string{place, key}
		if i < 0 || s.places.tried[k] == items[i].Version || s.places.answered[k] == items[i].Version {
			continue
		}
		s.places.tried[k] = items[i].Version
		want[k] = items[i].Version
	}
	if len(want) == 0 {
		s.mu.Unlock()
		return
	}
	shown := slices.Clone(s.places.items[place])
	for _, it := range items {
		if j := slices.IndexFunc(shown, func(o PlaceItem) bool { return o.Key == it.Key }); j >= 0 {
			shown[j] = it
		} else {
			shown = append(shown, it)
		}
	}
	s.places.first = first
	posted = nil
	if p := s.places.posted; p != nil && p.seq > s.places.shownSeq {
		posted = p
	}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.places.first = nil
		s.mu.Unlock()
	}()
	s.SetItems(place, shown)
	// The wait is over with a drawing of this generation that drew, or
	// failed to draw, every copy waited on at its version.
	drew := func(d placeDraw) bool {
		s.publish(d)
		if !s.current(d.gen) {
			return false
		}
		for k, v := range want {
			if d.asked[k] != v {
				return false
			}
		}
		return true
	}
	if posted != nil && drew(*posted) {
		return
	}
	wait := time.NewTimer(time.Until(deadline))
	defer wait.Stop()
	poll := time.NewTicker(time.Millisecond)
	defer poll.Stop()
	for {
		select {
		case <-poll.C:
			// A call of the plugin's into the app waits on this thread,
			// which waits on the plugin: the copies aren't drawn in time,
			// so the next plugin draws them.
			if s.h.awaitsApp(s.plugin) {
				return
			}
		case d := <-first:
			if drew(d) {
				return
			}
		case <-wait.C:
			return
		}
	}
}

// placed counts the native widgets name the tree n places, anywhere, and
// those every frame draws: in rows, columns and cards alone, never in a
// dialog, a popover, or an item's trailing or actions, which may not show.
// shown says n is drawn every frame.
func placed(n *widget.Node, name string, shown bool) (all, drawn int) {
	if n.Type == "native" && n.Name == name {
		all++
		if shown {
			drawn++
		}
	}
	in := shown && (n.Type == "row" || n.Type == "column" || n.Type == "card")
	for _, ch := range n.Children {
		a, d := placed(ch, name, in)
		all, drawn = all+a, drawn+d
	}
	for _, ch := range slices.Concat(n.Trailing, n.Actions) {
		a, _ := placed(ch, name, false)
		all += a
	}
	if n.Popover != nil {
		a, _ := placed(n.Popover, name, false)
		all += a
	}
	return all, drawn
}

func (s *Session) drawBatch(pl Place, state json.RawMessage, batch []PlaceItem, views map[string]*widget.Node) error {
	if !s.runtime().Can("place") {
		return errors.New("the plugin has places but no place export")
	}
	type item struct {
		Key     string `json:"key"`
		Context any    `json:"context,omitempty"`
	}
	in := make([]item, len(batch))
	for i, it := range batch {
		in[i] = item{it.Key, it.Context}
	}
	var raw json.RawMessage
	if err := s.call(ViewTimeout, "place", map[string]any{"id": pl.ID, "at": pl.At, "state": state, "items": in}, &raw); err != nil {
		return err
	}
	var out []json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil || len(out) != len(batch) {
		return fmt.Errorf("place answered %.40s…, not a list of %d widgets or nulls", raw, len(batch))
	}
	bound := MaxPlaceWidgets
	if pl.Replaces {
		bound = widget.MaxNodes
	}
	for i, w := range out {
		if string(w) == "null" {
			continue
		}
		v, err := widget.DecodeWithin(append(append([]byte(`{"body":`), w...), '}'), bound)
		if err != nil {
			return fmt.Errorf("%s: %w", batch[i].Key, err)
		}
		// A component's layout around a native it requires, as the
		// composer's around composer.input, places it once, where it
		// always shows.
		if need := ComponentNamed(pl.At).Requires; pl.Replaces && need != "" {
			if all, drawn := placed(v.Body, need, true); all != 1 || drawn != 1 {
				return fmt.Errorf("%s: %s's layout places %s once, in rows, columns and cards alone, not %d times, %d of them always shown", batch[i].Key, pl.At, need, all, drawn)
			}
		}
		views[batch[i].Key] = v.Body
	}
	return nil
}

// startTimers wakes the app session on each of its timers, until it
// closes.
func (h *Host) startTimers(s *Session, timers []Timer) {
	for _, t := range timers {
		every := time.Duration(t.Every) * time.Second
		if h.opts.TimerScale > 0 {
			every = time.Duration(float64(every) * h.opts.TimerScale)
		}
		go func() {
			tick := time.NewTicker(every)
			defer tick.Stop()
			for range tick.C {
				s.mu.Lock()
				done := s.done
				s.mu.Unlock()
				if done {
					return
				}
				h.opts.Post(func() { s.Notify(AppEvent{Kind: "timer", ID: t.ID}) })
			}
		}()
	}
}
