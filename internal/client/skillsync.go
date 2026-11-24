package client

import (
	"context"
	"log/slog"
	"time"

	"glitched/internal/api"
	"glitched/internal/daemon"
	"glitched/internal/skills"
	"glitched/internal/transport"
)

const (
	// syncTimeout bounds a sync of this machine's skills to an engine as
	// the app reaches it, the files of each skill it lacks included.
	syncTimeout = 2 * time.Minute
	// syncFirst bounds the sync before a turn or a read of a thread's
	// skills, waiting for one under way included: past it, the turn goes
	// with what the engine holds.
	syncFirst = 10 * time.Second
)

// skillSource is this machine as it syncs the skills in its state folder
// to one engine: its own engine, by ID and name, and the gate that has
// one sync or drop at a time go to that engine, so a drop is never undone
// by a sync that began before it.
type skillSource struct {
	id   api.ID
	name string
	dir  string
	gate chan struct{}
}

// syncSource is this machine as e gets its skills, and false where e is
// this machine's own engine, or before that engine has answered. Only the
// Post thread calls it.
func (m *Manager) syncSource(e *Env) (skillSource, bool) {
	if e.Kind != Remote {
		return skillSource{}, false
	}
	l := m.envs[0]
	if l.ID.IsZero() || e.ID == l.ID {
		return skillSource{}, false
	}
	return skillSource{id: l.ID, name: l.Name, dir: m.cfg.Dir, gate: e.skillGate}, true
}

// push has the engine at ep hold this machine's skills, or drop them when
// Settings › This machine says not to sync them, within within. A push that
// fails leaves the engine its last set, and is logged: the call it
// precedes goes on.
func (src skillSource) push(ctx context.Context, ep api.Endpoint, within time.Duration) {
	k, ok := ep.(transport.Skills)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, within)
	defer cancel()
	select {
	case src.gate <- struct{}{}:
		defer func() { <-src.gate }()
	case <-ctx.Done():
		return
	}
	cfg, err := daemon.Defaults(src.dir)
	if err != nil {
		slog.Warn("skills: not synced, as engine.json couldn't be read", "err", err)
		return
	}
	if cfg.SyncSkillsEnabled() {
		_, err = skills.Push(ctx, src.dir, src.id, src.name, k)
	} else {
		err = k.DropSkills(ctx, api.SkillDrop{Source: src.id})
	}
	if err != nil {
		slog.Warn("skills: couldn't sync this machine's skills to a paired engine", "err", err)
	}
}

// syncSkills syncs this machine's skills to e as e connects, and, as this
// machine's own engine does, to each remote that connected before it. Only
// the Post thread calls it.
func (m *Manager) syncSkills(e *Env) {
	envs := []*Env{e}
	if e.Kind == Local {
		envs = m.envs
	}
	for _, r := range envs {
		src, ok := m.syncSource(r)
		if s := r.session; ok && s != nil {
			m.wg.Go(func() { src.push(s.ctx, s.ep, syncTimeout) })
		}
	}
}
