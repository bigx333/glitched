package client

import (
	"encoding/json"
	"errors"
	"fmt"
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

// edit changes gui.json for every GUI on this Dir: under gui.json.lock it
// reads what the others saved, applies change and replaces the file, so a
// crash leaves the old file or the new one. The envs then follow the
// servers the file lists; renew names one to reconnect even if unchanged.
func (m *Manager) edit(renew string, change func(*saved)) error {
	if err := os.MkdirAll(m.cfg.Dir, 0o700); err != nil {
		return err
	}
	lk, err := lock(filepath.Join(m.cfg.Dir, savedFile+".lock"))
	if err != nil {
		return err
	}
	defer lk.Close()
	s, err := load(m.cfg.Dir)
	if err != nil {
		return err
	}
	change(&s)
	if err := replace(m.cfg.Dir, s); err != nil {
		return err
	}
	m.saved = s
	m.sync(renew)
	return nil
}

// replace writes gui.json through a temp file, which CreateTemp makes 0600
// as the tokens in it need.
func replace(dir string, s saved) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, savedFile+".*")
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
	if err := os.Rename(f.Name(), filepath.Join(dir, savedFile)); err != nil {
		return err
	}
	return syncDir(dir)
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
