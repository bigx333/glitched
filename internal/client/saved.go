package client

import (
	"errors"
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
	Label string `json:"label"`
	Addr  string `json:"addr"`
	Token string `json:"token"`
}

// saved is what gui.json holds, tokens included.
type saved struct {
	Servers []Server             `json:"servers,omitempty"`
	Seen    map[api.ID]time.Time `json:"seen,omitempty"` // each thread's last turn the GUI showed
}

func (m *Manager) savedPath() string { return filepath.Join(m.cfg.Dir, "gui.json") }

// edit changes gui.json for every GUI on this Dir: under gui.json.lock it
// reads what the others saved, applies change and replaces the file.
func (m *Manager) edit(change func(*saved)) (saved, error) {
	return jsonfile.Edit(m.savedPath(), func(s *saved) error {
		change(s)
		return nil
	})
}

// editServers saves a change to the servers, then makes the envs follow
// the servers gui.json lists, which another GUI may have changed too.
// renew names one to reconnect even if unchanged.
func (m *Manager) editServers(renew string, change func(*saved)) error {
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
// env whose entry changed, or named renew, reconnects and keeps its threads.
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
}

// Servers are the remotes gui.json listed when this manager last read it,
// in the order Envs shows them.
func (m *Manager) Servers() []Server { return slices.Clone(m.saved.Servers) }

// AddServer reaches a remote engine and lists it in gui.json. A label
// already listed is replaced and reconnected, which also retries a server
// that refused the token; its env keeps the threads and pending commands.
func (m *Manager) AddServer(s Server) error {
	if s.Label == "" || s.Addr == "" {
		return errors.New("client: a server needs a label and an address")
	}
	return m.editServers(s.Label, func(f *saved) {
		if i := slices.IndexFunc(f.Servers, func(o Server) bool { return o.Label == s.Label }); i >= 0 {
			f.Servers[i] = s
		} else {
			f.Servers = append(f.Servers, s)
		}
	})
}

func (m *Manager) RemoveServer(label string) error {
	return m.editServers("", func(f *saved) {
		f.Servers = slices.DeleteFunc(f.Servers, func(o Server) bool { return o.Label == label })
	})
}

func (m *Manager) server(label string) int {
	return slices.IndexFunc(m.envs, func(e *Env) bool { return e.Kind == Remote && e.Name == label })
}

// Unread is true when the thread's last turn ended after the one MarkRead
// last saw.
func (m *Manager) Unread(th *Thread) bool {
	last := th.View.LastTurn
	return last != nil && last.Ended.After(m.saved.Seen[th.View.ID])
}

// MarkRead records that the GUI showed the thread's last turn, at once,
// and saves the mark to gui.json from a goroutine, as the file's lock
// may wait on another GUI. It leaves the envs as they are, whatever
// servers another GUI saved.
func (m *Manager) MarkRead(th *Thread) {
	if !m.Unread(th) {
		return
	}
	m.saved.see(map[api.ID]time.Time{th.View.ID: th.View.LastTurn.Ended})
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
		s, err := m.edit(func(f *saved) { f.see(seen) })
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
