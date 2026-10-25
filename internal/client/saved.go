package client

import (
	"context"
	"errors"
	"fmt"
	"log"
	"maps"
	"path/filepath"
	"slices"
	"time"

	"glitched/internal/api"
	"glitched/internal/jsonfile"
)

// Server is a remote engine reached over the tailnet with a token it
// paired for this GUI.
type Server struct {
	Label  string `json:"label"`
	Addr   string `json:"addr"`
	Token  string `json:"token"`
	Engine api.ID `json:"engine,omitzero"` // the engine it reached when added
}

// saved is what gui.json holds, tokens included.
type saved struct {
	Servers []Server             `json:"servers,omitempty"`
	Seen    map[api.ID]time.Time `json:"seen,omitempty"` // each thread's last turn the GUI showed
}

func (m *Manager) savedPath() string { return filepath.Join(m.cfg.Dir, "gui.json") }

// edit changes gui.json for every GUI on this Dir: under gui.json.lock it
// reads what the others saved, applies change and replaces the file,
// unless change fails.
func (m *Manager) edit(change func(*saved) error) (saved, error) {
	return jsonfile.Edit(m.savedPath(), change)
}

// editServers saves a change to the servers, then makes the envs follow
// the servers gui.json lists, which another GUI may have changed too.
// renew names one to reconnect even if unchanged.
func (m *Manager) editServers(renew string, change func(*saved) error) error {
	s, err := m.edit(change)
	if err != nil {
		return err
	}
	seen := m.saved.Seen
	m.saved = s
	m.saved.see(seen)
	m.sync(renew)
	return nil
}

// sync makes the remote envs the servers gui.json lists, in its order. An
// env whose entry changed, or named renew, reconnects and keeps its threads,
// as does a duplicate no env listed before it reaches the engine of any
// more.
func (m *Manager) sync(renew string) {
	envs := []*Env{m.envs[0]}
	for _, s := range m.saved.Servers {
		i := m.server(s.Label)
		if i < 0 {
			envs = append(envs, m.remote(s))
			continue
		}
		e := m.envs[i]
		if e.server != s || s.Label == renew {
			m.redial(e, s)
		}
		envs = append(envs, e)
	}
	for _, e := range m.envs[1:] {
		if !slices.Contains(envs, e) {
			e.stop()
		}
	}
	m.envs = envs
	for _, e := range envs[1:] {
		if _, dup := e.Status.(Duplicate); dup && m.holder(e.ID) == e {
			m.redial(e, e.server)
		}
	}
}

// Servers are the remotes gui.json listed when this manager last read it,
// in the order Envs shows them.
func (m *Manager) Servers() []Server { return slices.Clone(m.saved.Servers) }

// AddServer reaches a remote engine from a goroutine, then on the Post
// thread lists it in gui.json and calls done with nil, or calls done with
// why it didn't: the server didn't answer or refused the handshake, or its
// engine is listed already, as this machine's or another server's. Adding
// a label listed already re-pairs it, which also retries a server that
// refused the handshake; its env keeps the threads and pending commands.
// A re-pair fails if the label is removed meanwhile.
func (m *Manager) AddServer(s Server, done func(error)) {
	if s.Label == "" || s.Addr == "" {
		done(errors.New("client: a server needs a label and an address"))
		return
	}
	repair := slices.ContainsFunc(m.saved.Servers, func(o Server) bool { return o.Label == s.Label })
	m.wg.Go(func() {
		r := m.newRemote(s)
		ctx, cancel := context.WithTimeout(m.ctx, addTimeout)
		ep, _ := r.endpoint(ctx)
		info, err := ep.Info(ctx)
		cancel()
		r.close()
		m.post(m.ctx, func() {
			if err != nil {
				done(fmt.Errorf("client: reaching %s: %w", s.Addr, err))
				return
			}
			s.Engine = info.ID
			done(m.list(s, repair))
		})
	})
}

// list saves s to gui.json, unless another env reaches its engine, or
// gui.json lists it under another label, as another GUI may have. A
// re-pair replaces the entry under s's label, and fails if there is none.
func (m *Manager) list(s Server, repair bool) error {
	return m.editServers(s.Label, func(f *saved) error {
		if h := m.holder(s.Engine); h != nil && h.Kind == Local {
			return fmt.Errorf("client: %s is this machine's engine", s.Label)
		} else if h != nil && h.Name != s.Label {
			return fmt.Errorf("client: %s is already listed as %s", s.Label, h.Name)
		}
		if i := slices.IndexFunc(f.Servers, func(o Server) bool { return !s.Engine.IsZero() && o.Engine == s.Engine && o.Label != s.Label }); i >= 0 {
			return fmt.Errorf("client: %s is already listed as %s", s.Label, f.Servers[i].Label)
		}
		switch i := slices.IndexFunc(f.Servers, func(o Server) bool { return o.Label == s.Label }); {
		case i >= 0:
			f.Servers[i] = s
		case repair:
			return fmt.Errorf("client: %s was removed while it was paired again", s.Label)
		default:
			f.Servers = append(f.Servers, s)
		}
		return nil
	})
}

func (m *Manager) RemoveServer(label string) error {
	return m.editServers("", func(f *saved) error {
		f.Servers = slices.DeleteFunc(f.Servers, func(o Server) bool { return o.Label == label })
		return nil
	})
}

func (m *Manager) server(label string) int {
	return slices.IndexFunc(m.envs, func(e *Env) bool { return e.Kind == Remote && e.Name == label })
}

// news is when the thread last had something to read: its last turn's end,
// or a child's report after it.
func news(v api.Thread) time.Time {
	var at time.Time
	if v.LastTurn != nil {
		at = v.LastTurn.Ended
	}
	if v.Reported.After(at) {
		at = v.Reported
	}
	return at
}

// Unread is true when the thread's last turn ended, or a child reported to
// it, after what MarkRead last saw.
func (m *Manager) Unread(th *Thread) bool {
	at := news(th.View)
	return !at.IsZero() && at.After(m.saved.Seen[th.View.ID])
}

// MarkRead records that the GUI showed the thread's last turn and reports,
// at once, and saves the mark to gui.json from a goroutine, as the file's
// lock may wait on another GUI. It leaves the envs as they are, whatever
// servers another GUI saved.
func (m *Manager) MarkRead(th *Thread) {
	if m.Unread(th) {
		m.read(th.View.ID, news(th.View))
	}
}

// MarkShown records that the GUI showed the thread's transcript, read up
// to the last turn or report it has, which may be behind the view's: one
// cached from an earlier visit, or still catching up.
func (m *Manager) MarkShown(th *Thread) {
	if tr := th.Transcript; tr != nil && m.Unread(th) {
		at := news(th.View)
		shown := tr.Ended
		if tr.Reported.After(shown) {
			shown = tr.Reported
		}
		if shown.Before(at) {
			at = shown
		}
		m.read(th.View.ID, at)
	}
}

func (m *Manager) read(thread api.ID, at time.Time) {
	if !at.After(m.saved.Seen[thread]) {
		return
	}
	m.saved.see(map[api.ID]time.Time{thread: at})
	m.unsaved = true
	m.saveSeen()
}

// saveSeen saves the read marks, unless a save is under way, which saves
// the marks made meanwhile once it is done. A save that failed is tried
// again with the next mark, or by Close.
func (m *Manager) saveSeen() {
	if m.saving || !m.unsaved {
		return
	}
	m.saving, m.unsaved = true, false
	seen := maps.Clone(m.saved.Seen)
	m.wg.Go(func() {
		s, err := m.edit(func(f *saved) error { f.see(seen); return nil })
		m.post(m.ctx, func() {
			m.saving = false
			if err != nil {
				log.Printf("client: saving read marks: %v", err)
				m.unsaved = true
				return
			}
			m.saved.see(s.Seen)
			m.saveSeen()
		})
	})
}

// see takes the marks in seen that are later than s's.
func (s *saved) see(seen map[api.ID]time.Time) {
	if s.Seen == nil {
		s.Seen = map[api.ID]time.Time{}
	}
	for id, at := range seen {
		if at.After(s.Seen[id]) {
			s.Seen[id] = at
		}
	}
}
