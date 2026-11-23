package runtime

import (
	"log/slog"
	"maps"
	"slices"
	"sync"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/skills"
)

// WithSkills gives each run's agent the skills package skills finds for
// its thread, the user's in dir, the engine's state folder, and its
// project's, copied to an overlay in dir.
func WithSkills(dir string) Option {
	return func(h *Host) { h.skills = &skillStage{dir: dir, using: map[api.ID]string{}} }
}

// skillStage keeps the overlays of the skills the host's agents get,
// each until no thread's agent last opened with it. A nil one gives
// none.
type skillStage struct {
	dir   string
	mu    sync.Mutex
	using map[api.ID]string // by thread: the overlay its last agent opened with
}

// stage is the skills the run of thread in workspace gets. Skills it
// can't copy are logged and left out: the run goes on without them.
func (s *skillStage) stage(thread api.ID, workspace, project string) provider.Skills {
	if s == nil {
		return provider.Skills{}
	}
	set := skills.Find(s.dir, workspace, project)
	s.mu.Lock()
	defer s.mu.Unlock()
	root, err := skills.Build(s.dir, set)
	if err != nil {
		slog.Warn("skills: the run's agent gets none, as they couldn't be copied", "thread", thread, "err", err)
	}
	if root == "" {
		delete(s.using, thread)
		return provider.Skills{}
	}
	s.using[thread] = root
	out := provider.Skills{Root: root}
	for _, sk := range set.Skills {
		out.List = append(out.List, provider.Skill{Name: sk.Name, Description: sk.Description, Path: skills.SkillFile(root, sk.Name)})
	}
	return out
}

// sweep deletes the overlays no thread's agent last opened with.
func (s *skillStage) sweep() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := skills.Sweep(s.dir, slices.Collect(maps.Values(s.using))); err != nil {
		slog.Warn("skills: an overlay no agent uses stays", "err", err)
	}
}

func (s *skillStage) forget(thread api.ID) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.using, thread)
}

// clear deletes every overlay, as no agent of an engine before this one
// runs once it booted.
func (s *skillStage) clear() {
	if s == nil {
		return
	}
	if err := skills.Clear(s.dir); err != nil {
		slog.Warn("skills: the last engine's overlays stay", "err", err)
	}
}
