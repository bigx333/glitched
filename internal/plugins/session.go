package plugins

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
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

// pageKey names a session: a plugin's page, or its tab in one thread's
// panel (scope being the thread), or its app session (page "").
type pageKey struct{ plugin, page, scope string }

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
	// tab is set for a tab in a thread's panel, whose page is the tab's id.
	tab bool
	// Builtin is set for a plugin built into the app.
	Builtin bool
	// places is set for the app session, which draws the plugin's places
	// in its view's stead, and hears app events and timers.
	places *placeSet

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
	context   any // what the session is about, as plugins see it: a tab's thread
	// rows are its lists' rows, which the rows export draws.
	rows rowSet
}

type queued struct {
	gen     int
	init    bool
	refresh bool // only draw again: the places' contexts changed
	// items is set on a refresh for the copies shown changing alone: only
	// the copies that came or changed are drawn.
	items bool
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
	// Place is set for a widget in one of the plugin's places.
	Place *PlaceRef `json:"place,omitempty"`
	// Row is set for a widget in a list's row. Modifiers are the keys held
	// as a list was submitted: cmd, shift, alt and ctrl.
	Row       *RowRef  `json:"row,omitempty"`
	Modifiers []string `json:"modifiers,omitempty"`
}

// PlaceRef names the copy of a place a widget was in: the place, the item
// it was drawn for, and that item's context.
type PlaceRef struct {
	ID      string `json:"id"`
	At      string `json:"at"`
	Key     string `json:"key"`
	Context any    `json:"context,omitempty"`
}

// AppEvent is something that happened in the app, or a timer, as update
// gets it.
type AppEvent struct {
	Kind string `json:"kind"` // "app" or "timer"
	Type string `json:"type,omitempty"`
	ID   string `json:"id,omitempty"` // a timer's
	Data any    `json:"data,omitempty"`
}

// Session is the plugin id's view page, started if it isn't.
func (h *Host) Session(id, page string) (*Session, error) {
	k := pageKey{plugin: id, page: page}
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

// TabSession is the plugin id's tab in the panel of a thread, started if
// it isn't, about context: the thread's record.
func (h *Host) TabSession(id, tab, thread string, context any) (*Session, error) {
	k := pageKey{id, tab, thread}
	if s := h.sessions[k]; s != nil {
		s.SetContext(context)
		return s, nil
	}
	p, err := h.plugin(id)
	if err != nil {
		return nil, err
	}
	if !p.Can("view") {
		return nil, fmt.Errorf("plugin %s has no view export", id)
	}
	s := &Session{h: h, p: p, plugin: id, page: tab, tab: true, context: context}
	h.sessions[k] = s
	s.push(queued{init: true})
	return s, nil
}

// SetContext changes what the session is about; the plugin sees it in its
// next call.
func (s *Session) SetContext(c any) {
	next, _ := json.Marshal(c)
	s.mu.Lock()
	old, _ := json.Marshal(s.context)
	s.context = c
	s.mu.Unlock()
	if !bytes.Equal(old, next) {
		s.refresh() // a tab draws its thread as it changes
	}
}

// Notify hands the plugin an app event or a timer.
func (s *Session) Notify(ev AppEvent) {
	s.mu.Lock()
	gen := s.gen
	s.mu.Unlock()
	s.push(queued{gen: gen, event: ev})
}

// refresh draws again, with no event.
func (s *Session) refresh() {
	s.mu.Lock()
	gen := s.gen
	s.mu.Unlock()
	s.push(queued{gen: gen, refresh: true})
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
		if ev.Place != nil {
			q.change = ev.Place.ID + "\x00" + ev.Place.Key + "\x00" + ev.Node // one copy's widget, not another's
		}
		if ev.Row != nil {
			q.change += "\x00" + ev.Row.List + "\x00" + ev.Row.Key // one row's, not another's
		}
	}
	if !s.push(q) {
		return 0
	}
	return q.seq
}

// Retry starts the page over: the plugin's init, then its view.
func (s *Session) Retry() {
	s.mu.Lock()
	s.gen++
	s.queue = nil
	s.state = nil
	s.broken = false
	// What an earlier start asked for goes; what it drew shows till the
	// new start's view draws its rows again.
	s.rows = rowSet{lists: s.rows.lists, rev: s.rows.rev, stamp: s.rows.stamp}
	gen := s.gen
	s.mu.Unlock()
	s.Err = nil
	s.push(queued{gen: gen, init: true})
}

// push queues q, and reports whether it did.
func (s *Session) push(q queued) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done || s.broken || q.gen != s.gen {
		return false
	}
	if i := slices.IndexFunc(s.queue, func(w queued) bool { return w.refresh }); q.refresh && i >= 0 {
		s.queue[i].items = s.queue[i].items && q.items
		return true
	}
	// A change replaces the one of the same widget waiting just before it,
	// never one before a click or a submit, which saw the earlier value.
	if n := len(s.queue); q.change != "" && n > 0 && s.queue[n-1].change == q.change {
		s.queue[n-1] = q
		return true
	}
	if len(s.queue) >= MaxQueued {
		s.h.opts.Post(func() { s.fail(fmt.Errorf("the plugin is %d events behind; dropped one", MaxQueued)) })
		return false
	}
	s.queue = append(s.queue, q)
	s.Busy = true
	if !s.running {
		s.running = true
		go s.run()
	}
	return true
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
// Idle reports whether no command runs and no plugin's app session, which
// draws its places and components, has an event waiting or a call
// running: what they last did is posted to the UI thread by then. Pages
// and tabs aren't asked.
func (h *Host) Idle() bool {
	if h.commands.Load() > 0 {
		return false
	}
	for k, s := range h.sessions {
		if k != (pageKey{plugin: k.plugin}) {
			continue
		}
		s.mu.Lock()
		busy := s.running || len(s.queue) > 0 || s.dirty
		s.mu.Unlock()
		if busy {
			return false
		}
	}
	return true
}

func (s *Session) run() {
	var failed error
	defer func() {
		if r := recover(); r != nil {
			err := fmt.Errorf("plugin page crashed: %v", r)
			s.mu.Lock()
			s.running = false
			s.mu.Unlock()
			s.h.opts.Post(func() { s.fail(err); s.Busy = false })
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
				// The rows in view are drawn once the view they are in is.
				want := s.rows.take()
				if want == nil {
					s.running = false
					s.mu.Unlock()
					return
				}
				state, gen, context := s.rows.state, s.gen, s.rows.context
				s.mu.Unlock()
				s.drawRows(gen, state, context, want)
				continue
			}
			s.dirty = false
			state, values, gen, processed, context := s.state, s.values, s.gen, s.processed, s.context
			s.mu.Unlock()
			if s.places != nil {
				s.renderPlaces(gen, processed, state, failed)
			} else {
				s.render(gen, processed, state, values, context, failed)
			}
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
		state, values, context := s.state, s.values, s.context
		s.dirty = true
		if s.places != nil && !q.items {
			s.places.redrawAll = true
		}
		s.mu.Unlock()
		if q.refresh {
			continue
		}
		ran := s.runtime()
		failed = s.apply(q, state, values, context)
		s.mu.Lock()
		if q.seq > 0 && q.gen == s.gen {
			s.processed = q.seq
		}
		s.mu.Unlock()
		if q.init && failed != nil {
			err := fmt.Errorf("init: %w", failed)
			s.mu.Lock()
			if q.gen != s.gen {
				s.mu.Unlock()
				continue // a retry started the page over meanwhile
			}
			if s.p != ran {
				// Its code was swapped as it failed: the new code starts it.
				s.queue = append([]queued{{gen: s.gen, init: true}}, s.queue...)
				s.mu.Unlock()
				continue
			}
			s.broken, s.queue, s.dirty, s.running = true, nil, false, false
			s.mu.Unlock()
			s.h.opts.Post(func() {
				if s.current(q.gen) {
					s.fail(err)
					s.Busy = false
				}
			})
			return
		}
	}
}

// apply hands the plugin one event, or starts it with init, and keeps the
// state it answers.
func (s *Session) apply(q queued, state json.RawMessage, values map[string]any, context any) error {
	var raw json.RawMessage
	var err error
	switch {
	case q.init && !s.runtime().Can("init"):
		s.setState(q.gen, nil)
		return nil
	case q.init:
		err = s.call(0, "init", s.args(map[string]any{}, context), &raw)
	default:
		if _, app := q.event.(AppEvent); app && !s.runtime().Can("update") {
			return nil // nothing hears it: a grant changing doesn't break a plugin that only draws
		}
		if !s.runtime().Can("update") {
			return errors.New("the plugin has no update export")
		}
		err = s.call(0, "update", s.args(map[string]any{"state": state, "event": q.event, "values": values}, context), &raw)
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
	return nil
}

// args are a call's arguments: what the call gives, and what every call
// says of the session.
func (s *Session) args(a map[string]any, context any) map[string]any {
	a["page"] = s.page
	if s.tab {
		a["tab"] = s.page
	}
	if context != nil {
		a["context"] = context
	}
	return a
}

// current reports whether the page is open, in its start gen.
func (s *Session) current(gen int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.done && gen == s.gen
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
}

// decodeUpdate reads what init or update answered: nothing, which
// changes nothing, or {state}. What a plugin does in the app, it does
// with glitched's calls as it runs.
func decodeUpdate(raw json.RawMessage) (update, error) {
	var u update
	if len(raw) == 0 || string(raw) == "null" {
		return u, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return u, fmt.Errorf("update answered %.40s…, not {state}", raw)
	}
	for k := range fields {
		switch k {
		case "state":
		case "effects":
			return u, errors.New("update answered effects: call glitched's functions instead, as glitched.toast(...)")
		default:
			return u, fmt.Errorf("update answered an unknown field %q", k)
		}
	}
	if st, ok := fields["state"]; ok {
		if len(st) > MaxState {
			return u, fmt.Errorf("state is %d bytes, over the %d allowed", len(st), MaxState)
		}
		u.hasState, u.state = true, st
	}
	return u, nil
}

// render asks the plugin for its view of the state, checks it, and
// publishes it with what failed.
func (s *Session) render(gen, processed int, state json.RawMessage, values map[string]any, context any, failed error) {
	var raw json.RawMessage
	err := s.call(ViewTimeout, "view", s.args(map[string]any{"state": state, "values": values}, context), &raw)
	var v *widget.View
	if err == nil {
		v, err = widget.Decode(raw)
	}
	if err != nil {
		failed = fmt.Errorf("view: %w", err)
	} else {
		s.rows.drewView(&s.mu, state, context)
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
			s.dropLists()
			s.Processed = processed
		}
		s.fail(failed)
		s.Busy = s.running && (len(s.queue) > 0 || s.dirty)
	})
}

// call calls the plugin. A runtime the host replaced is shut down for
// good, so a call that started as it was fails, and starts none.
func (s *Session) call(timeout time.Duration, method string, args map[string]any, out *json.RawMessage) error {
	return s.runtime().CallWithin(timeout, method, args, out)
}

// runtime is the plugin the session calls, which a hot reload swaps.
func (s *Session) runtime() *plugin.Plugin {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.p
}

// swap moves the session to a reloaded plugin, keeping its state, and
// draws it again.
func (s *Session) swap(p *plugin.Plugin) {
	s.mu.Lock()
	s.p = p
	broken := s.broken
	s.broken = false
	gen := s.gen
	s.mu.Unlock()
	s.Err = nil
	if broken {
		s.push(queued{gen: gen, init: true}) // its init failed: the new code starts it
		return
	}
	s.refresh()
}

// fail records what went wrong last, or that nothing did, and tells
// OnFailure of a failure.
func (s *Session) fail(err error) {
	s.Err = err
	// A session closed may yet hear of what it posted: its plugin may be
	// another by now, as a built-in back in a copy's place.
	if fn := s.h.failed.Load(); err != nil && fn != nil && !s.closed {
		s.h.mu.RLock()
		_, builtin := s.h.builtins[s.plugin]
		s.h.mu.RUnlock()
		(*fn)(s.plugin, builtin, err)
	}
}

// OnFailure has fn hear each failure a plugin's session records, on the
// UI thread, with whether the plugin is one built into the app, as tests
// fail on a built-in's failure they don't expect.
func (h *Host) OnFailure(fn func(plugin string, builtin bool, err error)) {
	h.failed.Store(&fn)
}
