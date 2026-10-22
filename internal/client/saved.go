package client

import (
	"errors"
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
// reads what the others saved, applies change and replaces the file. The
// envs then follow the servers the file lists; renew names one to
// reconnect even if unchanged.
func (m *Manager) edit(renew string, change func(*saved)) error {
	s, err := jsonfile.Edit(m.savedPath(), func(s *saved) error {
		change(s)
		return nil
	})
	if err != nil {
		return err
	}
	m.saved = s
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
	return m.edit(s.Label, func(f *saved) {
		if i := slices.IndexFunc(f.Servers, func(o Server) bool { return o.Label == s.Label }); i >= 0 {
			f.Servers[i] = s
		} else {
			f.Servers = append(f.Servers, s)
		}
	})
}

func (m *Manager) RemoveServer(label string) error {
	return m.edit("", func(f *saved) {
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

// MarkRead records in gui.json that the GUI showed the thread's last turn.
func (m *Manager) MarkRead(th *Thread) error {
	if !m.Unread(th) {
		return nil
	}
	id, ended := th.View.ID, th.View.LastTurn.Ended
	return m.edit("", func(f *saved) {
		if f.Seen == nil {
			f.Seen = map[api.ID]time.Time{}
		}
		if ended.After(f.Seen[id]) {
			f.Seen[id] = ended
		}
	})
}
