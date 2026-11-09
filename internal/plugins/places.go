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
	h.startTimers(s, x.Timers)
	return s
}

// SetItems says which copies of the place id the UI shows now. It draws
// them again only when one came, went or changed its version.
func (s *Session) SetItems(place string, items []PlaceItem) {
	s.mu.Lock()
	old := s.places.items[place]
	same := len(old) == len(items) && !slices.ContainsFunc(items, func(it PlaceItem) bool {
		i := slices.IndexFunc(old, func(o PlaceItem) bool { return o.Key == it.Key })
		return i < 0 || old[i].Version != it.Version
	})
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
func (s *Session) renderPlaces(gen int, state json.RawMessage, failed error) {
	start := time.Now()
	drawn := map[string]map[string]*widget.Node{}
	for _, pl := range s.places.places {
		s.mu.Lock()
		items := slices.Clone(s.places.items[pl.ID])
		s.mu.Unlock()
		views := map[string]*widget.Node{}
		drawn[pl.ID] = views
		for len(items) > 0 && failed == nil {
			batch := items[:min(len(items), MaxPlaceBatch)]
			items = items[len(batch):]
			if err := s.drawBatch(pl, state, batch, views); err != nil {
				failed = fmt.Errorf("place %s: %w", pl.ID, err)
			}
		}
	}
	took := time.Since(start)
	s.h.opts.Post(func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.closed || gen != s.gen {
			return
		}
		s.Err = failed
		s.places.Slow = took
		for id, views := range drawn {
			if failed != nil {
				continue // keep the last good drawing
			}
			s.places.views[id] = views
		}
		s.Rev++
		s.Busy = s.running && (len(s.queue) > 0 || s.dirty)
	})
}

func (s *Session) drawBatch(pl Place, state json.RawMessage, batch []PlaceItem, views map[string]*widget.Node) error {
	if !s.p.Can("place") {
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
	for i, w := range out {
		if string(w) == "null" {
			continue
		}
		v, err := widget.DecodeWithin(append(append([]byte(`{"body":`), w...), '}'), MaxPlaceWidgets)
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
