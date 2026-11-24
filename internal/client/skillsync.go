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

// syncTimeout bounds one sync of this machine's skills to an engine, the
// files of each skill it lacks included.
const syncTimeout = 2 * time.Minute

// skillSource is this machine as it syncs the skills in its state folder
// to the engines it pairs with: its engine, by ID and name.
type skillSource struct {
	id   api.ID
	name string
	dir  string
}

// skillSource is this machine as e gets its skills, and false where e is
// this machine's own engine, or before that engine has answered. Only the
// Post thread calls it.
func (m *Manager) skillSource(e *Env) (skillSource, bool) {
	if e.Kind != Remote {
		return skillSource{}, false
	}
	l := m.envs[0]
	if l.ID.IsZero() || e.ID == l.ID {
		return skillSource{}, false
	}
	return skillSource{id: l.ID, name: l.Name, dir: m.cfg.Dir}, true
}

// push has the engine at ep hold this machine's skills, or drop them when
// Settings › This machine says not to sync them. A push that fails leaves
// the engine its last set, and is logged: the call it precedes goes on.
func (src skillSource) push(ctx context.Context, ep api.Endpoint) {
	k, ok := ep.(transport.Skills)
	if !ok {
		return
	}
	cfg, err := daemon.Defaults(src.dir)
	if err != nil {
		slog.Warn("skills: not synced, as engine.json couldn't be read", "err", err)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, syncTimeout)
	defer cancel()
	if cfg.SyncSkillsEnabled() {
		_, err = skills.Push(ctx, src.dir, src.id, src.name, k)
	} else {
		err = k.DropSkills(ctx, api.SkillDrop{Source: src.id})
	}
	if err != nil {
		slog.Warn("skills: couldn't sync this machine's skills to a paired engine", "err", err)
	}
}
