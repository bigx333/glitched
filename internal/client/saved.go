package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"time"

	"glitched/internal/api"
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

const savedFile = "gui.json"

func load(dir string) (saved, error) {
	b, err := os.ReadFile(filepath.Join(dir, savedFile))
	if errors.Is(err, os.ErrNotExist) {
		return saved{}, nil
	}
	if err != nil {
		return saved{}, err
	}
	var s saved
	if err := json.Unmarshal(b, &s); err != nil {
		return saved{}, fmt.Errorf("%s: %w", savedFile, err)
	}
	return s, nil
}

// save replaces gui.json whole, so a crash leaves the old file or the new
// one. CreateTemp makes it 0600, as the tokens in it need.
func (m *Manager) save(s saved) error {
	if err := os.MkdirAll(m.cfg.Dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(m.cfg.Dir, savedFile+".*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(append(b, '\n'))
	if err == nil {
		err = f.Sync()
	}
	if err := errors.Join(err, f.Close()); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), filepath.Join(m.cfg.Dir, savedFile)); err != nil {
		return err
	}
	m.saved = s
	return nil
}

// Servers are the remotes gui.json lists, in the order Envs shows them.
func (m *Manager) Servers() []Server { return slices.Clone(m.saved.Servers) }

// AddServer reaches a remote engine and lists it in gui.json. A label
// already listed is replaced and reconnected, which also retries a server
// that refused the token; its env keeps the threads and pending commands.
func (m *Manager) AddServer(s Server) error {
	if s.Label == "" || s.Addr == "" {
		return errors.New("client: a server needs a label and an address")
	}
	next := m.saved
	next.Servers = slices.Clone(next.Servers)
	if i := slices.IndexFunc(next.Servers, func(o Server) bool { return o.Label == s.Label }); i >= 0 {
		next.Servers[i] = s
	} else {
		next.Servers = append(next.Servers, s)
	}
	if err := m.save(next); err != nil {
		return err
	}
	if i := m.server(s.Label); i >= 0 {
		m.relink(m.envs[i], dial(s))
	} else {
		m.envs = append(m.envs, m.remote(s))
	}
	return nil
}

func (m *Manager) RemoveServer(label string) error {
	next := m.saved
	next.Servers = slices.DeleteFunc(slices.Clone(next.Servers), func(o Server) bool { return o.Label == label })
	if err := m.save(next); err != nil {
		return err
	}
	if i := m.server(label); i >= 0 {
		m.envs[i].retire()
		m.envs = slices.Delete(m.envs, i, i+1)
	}
	return nil
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
	next := m.saved
	next.Seen = maps.Clone(next.Seen)
	if next.Seen == nil {
		next.Seen = map[api.ID]time.Time{}
	}
	next.Seen[th.View.ID] = th.View.LastTurn.Ended
	return m.save(next)
}
