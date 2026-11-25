package client

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"reflect"
	"slices"
	"time"

	"glitched/internal/api"
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
		st, err := daemon.Inspect(m.ctx, m.cfg.Dir, m.cfg.Detect)
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

// Configure changes engine.json, and restarts the engine embedded here to
// apply the change, except a change to the child limits, idle
// compaction, keep awake, the provider log or children from other
// machines alone, which it applies at once: the engine reads the last
// from engine.json at each link's create. An engine another process runs applies it when it next starts.
func (m *Manager) Configure(change func(*daemon.Config)) error {
	var was daemon.Config
	cfg, err := daemon.Edit(m.cfg.Dir, func(cfg *daemon.Config) error {
		was = *cfg
		was.Listen = slices.Clone(cfg.Listen)
		was.Launch = maps.Clone(cfg.Launch)
		change(cfg)
		if reflect.DeepEqual(*cfg, was) {
			return daemon.ErrUnchanged
		}
		return nil
	})
	switch {
	case errors.Is(err, daemon.ErrUnchanged):
		return nil
	case err != nil:
		return err
	}
	m.changes++
	m.machine.Config = cfg
	m.machine.Listeners = nil
	for _, a := range cfg.Listen {
		m.machine.Listeners = append(m.machine.Listeners, daemon.Listener{Addr: a})
	}
	if was.SyncSkillsEnabled() != cfg.SyncSkillsEnabled() {
		m.resync()
	}
	was.Children = cfg.Children
	was.CompactBeforeExpire = cfg.CompactBeforeExpire
	was.KeepAwake = cfg.KeepAwake
	was.ProviderLog = cfg.ProviderLog
	was.ContinueAfterLimit = cfg.ContinueAfterLimit
	// The engine reads it as it starts, which is when it acts on it.
	was.ContinueAfterRestart = cfg.ContinueAfterRestart
	was.SyncSkills = cfg.SyncSkills
	was.ChildrenFromOtherMachines = cfg.ChildrenFromOtherMachines
	if reflect.DeepEqual(cfg, was) && m.embedded != nil {
		applyLive(m.embedded, cfg)
	} else {
		m.Restart()
	}
	m.Inspect()
	return nil
}

// Pair mints a token for a client labelled label, returned only this once.
func (m *Manager) Pair(label string) (string, error) {
	token, err := daemon.Pair(m.cfg.Dir, label)
	m.changes++
	m.Inspect()
	return token, err
}

// PairLink mints a link for the engine of the machine label names, scoped
// to l, its token returned only this once (daemon.PairLink).
func (m *Manager) PairLink(label string, l api.Link) (string, api.Link, error) {
	token, l, err := daemon.PairLink(m.cfg.Dir, label, l)
	m.changes++
	m.Inspect()
	return token, l, err
}

// AddMachine adds the engine at server, which minted this one a link with
// token, as label, where this engine's agents may then start children. It
// asks that engine which it is in the background and calls done with why
// it failed, or nil.
func (m *Manager) AddMachine(label, server, token string, done func(error)) {
	m.wg.Go(func() {
		ctx, cancel := context.WithTimeout(m.ctx, addTimeout)
		_, err := daemon.AddMachine(ctx, m.cfg.Dir, label, server, token)
		cancel()
		m.post(m.ctx, func() {
			m.changes++
			m.Inspect()
			done(err)
		})
	})
}

// RemoveMachine removes label's machine and its token here. Its link still
// works on that machine until its person removes this one there.
func (m *Manager) RemoveMachine(label string) error {
	err := daemon.RemoveMachine(m.cfg.Dir, label)
	m.changes++
	m.Inspect()
	return err
}

// RemoveToken removes label's token. The engine closes the sockets it
// admitted within a second.
func (m *Manager) RemoveToken(label string) error {
	err := daemon.RemoveToken(m.cfg.Dir, label)
	m.changes++
	m.Inspect()
	return err
}

// Restart stops the engine embedded here and starts it again from
// engine.json. The local env keeps its threads, and reconnects once the
// old engine has released the directory. Called again before the new
// engine starts, it restarts that one too, as it may have read
// engine.json before a change. It does nothing to an engine another
// process runs.
func (m *Manager) Restart() {
	switch {
	case m.closed:
		return
	case m.restarting:
		m.again = true
		return
	case !m.machine.Embedded:
		return
	}
	e, old := m.envs[0], m.local
	m.machine.Embedded, m.restarting, m.embedded = false, true, nil
	m.node.Store(nil)
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
	l := &local{dir: m.cfg.Dir, engine: m.cfg.Engine, done: make(chan struct{})}
	l.hosted = func(d *daemon.Daemon) {
		m.post(m.ctx, func() {
			if m.local != l {
				return
			}
			embedded := d != nil
			m.embedded = d
			if embedded && d.Node() != nil {
				m.node.Store(&node{d.Node()})
			} else {
				m.node.Store(nil)
			}
			m.machine.Embedded = embedded
			again := m.restarting && m.again && embedded
			m.restarting, m.again = false, false
			if again {
				m.Restart()
				return
			}
			// What the engine applies as it runs may have changed since it
			// read engine.json, as the user can change it while it starts.
			if cfg, err := daemon.Defaults(l.dir); embedded && err == nil {
				applyLive(d, cfg)
			}
		})
	}
	return l
}

// applyLive applies what of cfg d applies as it runs.
func applyLive(d *daemon.Daemon, cfg daemon.Config) {
	d.SetLimits(cfg.Children)
	d.SetIdleCompaction(cfg.CompactBeforeExpireEnabled())
	d.SetKeepAwake(cfg.KeepAwakeEnabled())
	d.SetProviderLog(cfg.ProviderLog)
	d.SetLimitResume(cfg.ContinueAfterLimit)
}
