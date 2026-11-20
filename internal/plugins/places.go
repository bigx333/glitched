package plugins

import (
	"encoding/json"
	"errors"
	"fmt"
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
	// cache is the worker's: each copy's last drawing, by place and key,
	// with the version and the state it was drawn at. A copy whose
	// version and state are the same isn't drawn again.
	cache      map[string]map[string]placed
	cacheState string
	cacheGen   int
}

type placed struct {
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
	}
	s.mu.Unlock()
	if !same {
		s.refresh()
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
	asked := map[[2]string]bool{}
	ps := s.places
	if ps.cache == nil || ps.cacheState != string(state) || ps.cacheGen != gen {
		ps.cache, ps.cacheState, ps.cacheGen = map[string]map[string]placed{}, string(state), gen
	}
	for _, pl := range ps.places {
		s.mu.Lock()
		items := slices.Clone(ps.items[pl.ID])
		s.mu.Unlock()
		views := map[string]*widget.Node{}
		drawn[pl.ID] = views
		cache := map[string]placed{}
		var todo []PlaceItem
		for _, it := range items {
			asked[[2]string{pl.ID, it.Key}] = true
			if c, ok := ps.cache[pl.ID][it.Key]; ok && c.version == it.Version {
				cache[it.Key] = c
				if c.node != nil {
					views[it.Key] = c.node
				}
				continue
			}
			todo = append(todo, it)
		}
		for len(todo) > 0 && failed == nil {
			batch := todo[:min(len(todo), MaxPlaceBatch)]
			todo = todo[len(batch):]
			if err := s.drawBatch(pl, state, batch, views); err != nil {
				failed = fmt.Errorf("place %s: %w", pl.ID, err)
				break
			}
			for _, it := range batch {
				cache[it.Key] = placed{it.Version, views[it.Key]}
			}
		}
		ps.cache[pl.ID] = cache
	}
	d := placeDraw{gen, processed, drawn, asked, failed, time.Since(start)}
	s.mu.Lock()
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
	gen, processed int
	drawn          map[string]map[string]*widget.Node
	// asked are the copies it drew, by place and key, nulls included.
	asked  map[[2]string]bool
	failed error
	took   time.Duration
}

// publish shows a drawing, on the UI thread.
func (s *Session) publish(d placeDraw) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || d.gen != s.gen {
		return
	}
	s.Err = d.failed
	s.places.Slow = d.took
	if d.failed != nil {
		// Keep the last good drawing, and what it says of the events
		// it took: its controls keep what the user typed.
		s.Busy = s.running && (len(s.queue) > 0 || s.dirty)
		return
	}
	for id, views := range d.drawn {
		// A copy shown since the drawing began keeps its last drawing
		// till the next one has it.
		for _, it := range s.places.items[id] {
			if _, ok := views[it.Key]; !ok && s.places.views[id][it.Key] != nil {
				views[it.Key] = s.places.views[id][it.Key]
			}
		}
		s.places.views[id] = views
	}
	s.Rev++
	s.Processed = d.processed
	s.Busy = s.running && (len(s.queue) > 0 || s.dirty)
}

// DrawFirst draws the copy key of the place before the UI shows it, for a
// copy it has no drawing of yet: it waits, on the UI thread, until the
// plugin drew it (or answered null for it), it failed, or the deadline
// passed. A component drawn so never shows empty as it opens. It waits
// once per version of the copy, as items has it: a copy the plugin leaves
// undrawn doesn't stall every frame. Nil when there is no drawing by then.
func (s *Session) DrawFirst(place string, items []PlaceItem, key string, deadline time.Time) *widget.Node {
	i := slices.IndexFunc(items, func(it PlaceItem) bool { return it.Key == key })
	if i < 0 {
		return nil
	}
	k := [2]string{place, key}
	first := make(chan placeDraw, 1)
	s.mu.Lock()
	if s.broken || s.done || s.places.tried[k] == items[i].Version {
		s.mu.Unlock()
		return nil
	}
	if s.places.tried == nil {
		s.places.tried = map[[2]string]string{}
	}
	s.places.tried[k] = items[i].Version
	s.places.first = first
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.places.first = nil
		s.mu.Unlock()
	}()
	// items are the copies the frame has drawn so far: the others shown
	// keep theirs, lest drawing this one take their drawings away.
	s.mu.Lock()
	shown := slices.Clone(s.places.items[place])
	s.mu.Unlock()
	for _, it := range items {
		if j := slices.IndexFunc(shown, func(o PlaceItem) bool { return o.Key == it.Key }); j >= 0 {
			shown[j] = it
		} else {
			shown = append(shown, it)
		}
	}
	s.SetItems(place, shown)
	wait := time.NewTimer(time.Until(deadline))
	defer wait.Stop()
	poll := time.NewTicker(time.Millisecond)
	defer poll.Stop()
	for {
		select {
		case <-poll.C:
			// A call of the plugin's into the app waits on this thread,
			// which waits on the plugin: the copy isn't drawn in time, so
			// the next plugin draws it.
			if s.h.awaitsApp(s.plugin) {
				return nil
			}
		case d := <-first:
			s.publish(d)
			if d.asked[k] || d.failed != nil {
				return s.PlaceView(place, key)
			}
		case <-wait.C:
			return nil
		}
	}
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
