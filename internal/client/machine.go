package client

import (
	"log/slog"
	"slices"
	"time"

	"glitched/internal/daemon"
)

// Machine is this machine's engine as the GUI manages it.
type Machine struct {
	daemon.Status
	// Embedded: the engine runs in this app, which restarts it to apply
	// engine.json. Otherwise another process, such as glitched serve, runs
	// it, or none does yet.
	Embedded bool
	Read     time.Time // when Status was read; zero before the first read
	Err      error     // why the last read failed
}

// Machine is the engine's status as the last Inspect read it.
func (m *Manager) Machine() Machine { return m.machine }

// Inspect reads the engine's status in the background, unless a read is
// under way.
func (m *Manager) Inspect() {
	if m.inspecting || m.closed {
		return
	}
	m.inspecting = true
	changes := m.changes
	m.wg.Go(func() {
		st, err := daemon.Inspect(m.ctx, m.cfg.Dir)
		m.post(m.ctx, func() {
			m.inspecting = false
			if changes != m.changes {
				m.Inspect()
				return
			}
			m.machine.Read, m.machine.Err = time.Now(), err
			if err == nil {
				m.machine.Status = st
			}
		})
	})
}

// SetListen records where the engine serves the tailnet in engine.json,
// and restarts the engine embedded here to apply it. An engine another
// process runs applies it when it next starts.
func (m *Manager) SetListen(addrs []string) error {
	cfg, err := daemon.Defaults(m.cfg.Dir)
	if err != nil {
		return err
	}
	if slices.Equal(cfg.Listen, addrs) {
		return nil
	}
	cfg.Listen = addrs
	if err := cfg.Save(); err != nil {
		return err
	}
	m.changes++
	m.machine.Config.Listen = addrs
	m.machine.Listeners = nil
	for _, a := range addrs {
		m.machine.Listeners = append(m.machine.Listeners, daemon.Listener{Addr: a})
	}
	m.Restart()
	m.Inspect()
	return nil
}

// Pair mints a token for a client labelled label, returned only this once.
func (m *Manager) Pair(label string) (string, error) {
	token, err := daemon.Pair(m.cfg.Dir, label)
	m.Inspect()
	return token, err
}

// Revoke removes label's token. The engine closes the sockets it admitted
// within a second.
func (m *Manager) Revoke(label string) error {
	err := daemon.Revoke(m.cfg.Dir, label)
	m.Inspect()
	return err
}

// Restart stops the engine embedded here and starts it again from
// engine.json. The local env keeps its threads, and reconnects once the
// old engine has released the directory. It does nothing to an engine
// another process runs.
func (m *Manager) Restart() {
	if !m.machine.Embedded || m.closed {
		return
	}
	e, old := m.envs[0], m.local
	m.machine.Embedded = false
	e.unlink()
	m.down(e, Connecting{})
	m.local = m.newLocal()
	m.wg.Go(func() {
		<-old.done
		if old.err != nil {
			slog.Warn("the embedded engine stopped with an error", "err", old.err)
		}
		m.post(e.ctx, func() { m.connect(e, m.local) })
	})
}

func (m *Manager) newLocal() *local {
	l := &local{dir: m.cfg.Dir, done: make(chan struct{})}
	l.hosted = func(embedded bool) {
		m.post(m.ctx, func() {
			if m.local == l {
				m.machine.Embedded = embedded
			}
		})
	}
	return l
}
