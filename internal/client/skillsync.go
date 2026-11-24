package client

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"glitched/internal/api"
	"glitched/internal/daemon"
	"glitched/internal/skills"
	"glitched/internal/transport"
)

// syncTimeout bounds a sync of this machine's skills to an engine, the
// files of each skill it lacks included.
const syncTimeout = 2 * time.Minute

// syncFirst is how long a turn, or a read of a thread's skills, waits for
// the sync asked for before it: past it, it goes with what the engine
// holds, and the sync goes on.
var syncFirst = 10 * time.Second

// skillSource is this machine as it syncs the skills in its state folder:
// its own engine, by ID and name.
type skillSource struct {
	id   api.ID
	name string
	dir  string
}

// skillSync runs the syncs of this machine's skills to one engine, one at
// a time, so a drop is never undone by a sync that began before it. Each
// reads the setting and the skills as it begins, never on its caller's
// goroutine: a caller waits for it only as long as it chooses.
type skillSync struct {
	mu      sync.Mutex
	run     func()          // the sync last asked for, nil for none
	asked   []chan struct{} // closed once the sync begun after each ask ends
	running bool
}

// ask has run go once the sync under way, if any, ends, in place of one
// asked for before it that hasn't begun, and returns a channel closed once
// it has.
func (w *skillSync) ask(wg *sync.WaitGroup, run func()) <-chan struct{} {
	w.mu.Lock()
	defer w.mu.Unlock()
	done := make(chan struct{})
	w.run, w.asked = run, append(w.asked, done)
	if !w.running {
		w.running = true
		wg.Go(w.loop)
	}
	return done
}

func (w *skillSync) loop() {
	for {
		w.mu.Lock()
		run, asked := w.run, w.asked
		w.run, w.asked = nil, nil
		if run == nil {
			w.running = false
			w.mu.Unlock()
			return
		}
		w.mu.Unlock()
		run()
		for _, c := range asked {
			close(c)
		}
	}
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
	return skillSource{id: l.ID, name: l.Name, dir: m.cfg.Dir}, true
}

// syncTo asks for a sync of this machine's skills to e's engine over s,
// and returns a channel closed once one begun after the ask has ended; nil
// where e gets none. Each its engine takes counts in e.SkillsChanged. Only
// the Post thread calls it.
func (m *Manager) syncTo(e *Env, s *session) <-chan struct{} {
	src, ok := m.syncSource(e)
	if !ok || s == nil {
		return nil
	}
	return e.skills.ask(&m.wg, func() {
		if src.push(s.ctx, s.ep) == nil {
			m.post(s.ctx, func() { e.SkillsChanged++ })
		}
	})
}

// push has the engine at ep hold this machine's skills, or drop them when
// Settings › This machine says not to sync them. A push that fails leaves
// the engine its last set, and is logged.
func (src skillSource) push(ctx context.Context, ep api.Endpoint) error {
	k, ok := ep.(transport.Skills)
	if !ok {
		return ErrTooOld
	}
	ctx, cancel := context.WithTimeout(ctx, syncTimeout)
	defer cancel()
	cfg, err := daemon.Defaults(src.dir)
	switch {
	case err != nil:
	case cfg.SyncSkillsEnabled():
		_, err = skills.Push(ctx, src.dir, src.id, src.name, k)
	default:
		err = k.DropSkills(ctx, api.SkillDrop{Source: src.id})
	}
	if err != nil {
		slog.Warn("skills: couldn't sync this machine's skills to a paired engine", "err", err)
	}
	return err
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
		m.syncTo(r, r.session)
	}
}
