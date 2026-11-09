package plugins

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	plugin "github.com/yxxbc/mygo-plugin"

	"glitched/internal/plugins/widget"
)

// The bounds of a session.
const (
	MaxState  = 1 << 20 // the state's JSON
	MaxQueued = 64      // events waiting for the plugin
	// ViewTimeout bounds one view call: drawing is no place for waiting.
	ViewTimeout = 5 * time.Second
)

type pageKey struct{ plugin, page string }

// Session is one native view page: the state the plugin keeps in it, the
// events waiting for its update, and what it shows. One worker goroutine
// at a time calls the plugin, so its calls see the events in order, and
// the UI thread never waits on it.
//
// The fields but mu's are the UI thread's: the worker sets them in what
// it posts there.
type Session struct {
	h            *Host
	p            *plugin.Plugin
	plugin, page string

	// View is the last view the plugin answered that checked; nil before
	// one. Rev counts them.
	View *widget.View
	Rev  int
	// Processed is the last UI event, as Dispatch numbered it, that the
	// plugin took before it answered View.
	Processed int
	// Err is what went wrong last: an update or a view that failed, or a
	// view that didn't check, while View stays the last good one.
	Err error
	// Busy is set while the plugin has events to take.
	Busy   bool
	closed bool

	mu      sync.Mutex
	queue   []queued
	running bool
	done    bool
	// broken is set when init failed: the page waits for a retry.
	broken bool
	// gen counts the page's starts: what an earlier start asked for is
	// dropped.
	gen       int
	state     json.RawMessage
	values    map[string]any
	dirty     bool
	seq       int // the last UI event's number
	processed int // the last UI event the plugin took
}

type queued struct {
	gen   int
	init  bool
	event any
	// values are the controls' values as the event happened; seq numbers a
	// UI event.
	values map[string]any
	seq    int
	// change is the widget of a change event, which a later change of the
	// same widget replaces while it waits.
	change string
}

// UIEvent is what the user did to a widget, as update gets it.
type UIEvent struct {
	Kind    string          `json:"kind"` // "ui"
	Type    string          `json:"type"` // click, change or submit
	Handler string          `json:"handler"`
	Node    string          `json:"node"`
	Value   any             `json:"value,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// EffectEvent is an effect's answer, as update gets it.
type EffectEvent struct {
	Kind   string `json:"kind"` // "effect"
	ID     string `json:"id,omitempty"`
	Effect string `json:"effect"`
	OK     bool   `json:"ok"`
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
	Denied bool   `json:"denied,omitempty"`
}

// Session is the plugin id's view page, started if it isn't.
func (h *Host) Session(id, page string) (*Session, error) {
	k := pageKey{id, page}
	if s := h.sessions[k]; s != nil {
		return s, nil
	}
	p, err := h.plugin(id)
	if err != nil {
		return nil, err
	}
	if !p.Can("view") {
		return nil, fmt.Errorf("plugin %s has no view export", id)
	}
	s := &Session{h: h, p: p, plugin: id, page: page}
	h.sessions[k] = s
	s.push(queued{init: true})
	return s, nil
}

// Dispatch hands the plugin what the user did, with the values of the
// view's inputs, toggles and selects as it happened, and returns the
// event's number.
func (s *Session) Dispatch(ev UIEvent, values map[string]any) int {
	ev.Kind = "ui"
	s.mu.Lock()
	s.seq++
	q := queued{gen: s.gen, event: ev, values: values, seq: s.seq}
	s.mu.Unlock()
	if ev.Type == "change" {
		q.change = ev.Node
	}
	s.push(q)
	return q.seq
}

// Retry starts the page over: the plugin's init, then its view.
func (s *Session) Retry() {
	s.mu.Lock()
	s.gen++
	s.queue = nil
	s.state = nil
	s.broken = false
	gen := s.gen
	s.mu.Unlock()
	s.Err = nil
	s.push(queued{gen: gen, init: true})
}

func (s *Session) push(q queued) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done || s.broken || q.gen != s.gen {
		return
	}
	// A change replaces the one of the same widget waiting just before it,
	// never one before a click or a submit, which saw the earlier value.
	if n := len(s.queue); q.change != "" && n > 0 && s.queue[n-1].change == q.change {
		s.queue[n-1] = q
		return
	}
	if len(s.queue) >= MaxQueued {
		s.h.opts.Post(func() { s.Err = fmt.Errorf("the plugin is %d events behind; dropped one", MaxQueued) })
		return
	}
	s.queue = append(s.queue, q)
	s.Busy = true
	if !s.running {
		s.running = true
		go s.run()
	}
}

func (s *Session) close() {
	s.closed = true
	s.mu.Lock()
	s.done = true
	s.queue = nil
	s.mu.Unlock()
}

// run takes the events in order, then asks for the view once they're all
// taken.
func (s *Session) run() {
	var failed error
	defer func() {
		if r := recover(); r != nil {
			err := fmt.Errorf("plugin page crashed: %v", r)
			s.mu.Lock()
			s.running = false
			s.mu.Unlock()
			s.h.opts.Post(func() { s.Err, s.Busy = err, false })
		}
	}()
	for {
		s.mu.Lock()
		if s.done {
			s.running = false
			s.mu.Unlock()
			return
		}
		if len(s.queue) == 0 {
			if !s.dirty {
				s.running = false
				s.mu.Unlock()
				return
			}
			s.dirty = false
			state, values, gen, processed := s.state, s.values, s.gen, s.processed
			s.mu.Unlock()
			s.render(gen, processed, state, values, failed)
			continue
		}
		q := s.queue[0]
		s.queue = s.queue[1:]
		if q.gen != s.gen {
			s.mu.Unlock()
			continue
		}
		if q.values != nil {
			s.values = q.values
		}
		state, values := s.state, s.values
		s.dirty = true
		s.mu.Unlock()
		failed = s.apply(q, state, values)
		s.mu.Lock()
		if q.seq > 0 && q.gen == s.gen {
			s.processed = q.seq
		}
		s.mu.Unlock()
		if q.init && failed != nil {
			err := fmt.Errorf("init: %w", failed)
			s.mu.Lock()
			s.broken, s.queue, s.dirty, s.running = true, nil, false, false
			s.mu.Unlock()
			s.h.opts.Post(func() {
				if !s.closed {
					s.Err, s.Busy = err, false
				}
			})
			return
		}
	}
}

// apply hands the plugin one event, or starts it with init, and keeps the
// state it answers. It runs the effects it asks for.
func (s *Session) apply(q queued, state json.RawMessage, values map[string]any) error {
	var raw json.RawMessage
	var err error
	switch {
	case q.init && !s.p.Can("init"):
		s.setState(q.gen, nil)
		return nil
	case q.init:
		err = s.call(0, "init", map[string]any{"page": s.page}, &raw)
	default:
		if !s.p.Can("update") {
			return errors.New("the plugin has no update export")
		}
		err = s.call(0, "update", map[string]any{"page": s.page, "state": state, "event": q.event, "values": values}, &raw)
	}
	if err != nil {
		return err
	}
	u, err := decodeUpdate(raw)
	if err != nil {
		return err
	}
	if q.init || u.hasState {
		s.setState(q.gen, u.state)
	}
	for _, e := range u.effects {
		s.h.effect(s.plugin, s.p, e, func(result any, err error) {
			ev := EffectEvent{Kind: "effect", ID: e.ID, Effect: e.Type, OK: err == nil, Result: result}
			if err != nil {
				ev.Error, ev.Denied = err.Error(), errors.Is(err, ErrDenied)
			} else if e.ID == "" {
				return // no one waits for it
			}
			s.push(queued{gen: q.gen, event: ev})
		})
	}
	return nil
}

func (s *Session) setState(gen int, st json.RawMessage) {
	s.mu.Lock()
	if gen == s.gen {
		s.state = st
	}
	s.mu.Unlock()
}

type update struct {
	hasState bool
	state    json.RawMessage
	effects  []Effect
}

// decodeUpdate reads what init or update answered: nothing, which
// changes nothing, or {state?, effects?}.
func decodeUpdate(raw json.RawMessage) (update, error) {
	var u update
	if len(raw) == 0 || string(raw) == "null" {
		return u, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return u, fmt.Errorf("update answered %.40s…, not {state, effects}", raw)
	}
	for k := range fields {
		if k != "state" && k != "effects" {
			return u, fmt.Errorf("update answered an unknown field %q", k)
		}
	}
	if st, ok := fields["state"]; ok {
		if len(st) > MaxState {
			return u, fmt.Errorf("state is %d bytes, over the %d allowed", len(st), MaxState)
		}
		u.hasState, u.state = true, st
	}
	if e, ok := fields["effects"]; ok && !bytes.Equal(e, []byte("null")) {
		if err := json.Unmarshal(e, &u.effects); err != nil {
			return u, fmt.Errorf("effects: %w", err)
		}
		if len(u.effects) > MaxEffects {
			return u, fmt.Errorf("%d effects, over the %d allowed", len(u.effects), MaxEffects)
		}
	}
	return u, nil
}

// render asks the plugin for its view of the state, checks it, and
// publishes it with what failed.
func (s *Session) render(gen, processed int, state json.RawMessage, values map[string]any, failed error) {
	var raw json.RawMessage
	err := s.call(ViewTimeout, "view", map[string]any{"page": s.page, "state": state, "values": values}, &raw)
	var v *widget.View
	if err == nil {
		v, err = widget.Decode(raw)
	}
	if err != nil {
		failed = fmt.Errorf("view: %w", err)
	}
	s.h.opts.Post(func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.closed || gen != s.gen {
			return
		}
		if v != nil {
			s.View = v
			s.Rev++
			s.Processed = processed
		}
		s.Err = failed
		s.Busy = s.running && (len(s.queue) > 0 || s.dirty)
	})
}

// call calls the plugin. A call that started as the page closed, with a
// reload, may have started the plugin's runtime again: closing it again
// leaves none behind.
func (s *Session) call(timeout time.Duration, method string, args map[string]any, out *json.RawMessage) error {
	err := s.p.CallWithin(timeout, method, args, out)
	s.mu.Lock()
	done := s.done
	s.mu.Unlock()
	if done {
		s.p.Close()
		return errors.New("the page closed")
	}
	return err
}
