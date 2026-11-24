package runtime

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/skills"
)

// WithSkills gives each run's agent the skills package skills finds for
// its thread, the user's in dir, the engine's state folder, and its
// project's, copied to an overlay in dir.
func WithSkills(dir string) Option {
	return func(h *Host) {
		h.skills = &skillStage{dir: dir, store: skills.NewStore(dir), using: map[api.ID]string{}, opening: map[string]int{}, survivors: map[api.ID]bool{}, gone: map[api.ID]bool{}}
	}
}

// skillStage keeps the overlays of the skills the host's agents get, each
// while an agent may read it: a thread's agent's own till another opens in
// its place, one an open is handing an agent, and, while an agent the last
// engine started may still run, every overlay that engine left. A nil one
// gives none.
type skillStage struct {
	dir       string
	store     *skills.Store // the skills another machine's GUI synced here
	mu        sync.Mutex
	using     map[api.ID]string // by thread: the overlay its agent opened with
	opening   map[string]int    // overlays opens are handing their agents, by count
	survivors map[api.ID]bool   // threads whose agent the last engine started may run
	gone      map[api.ID]bool   // deleted threads whose agent's close isn't done
	inherited []string          // the overlays the last engine left
}

// stage is the skills the run of thread in workspace gets, held till the
// open that hands them over says how it went (opened). Skills it can't
// copy are logged and left out: the run goes on without them.
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
		return provider.Skills{}
	}
	s.opening[root]++
	out := provider.Skills{Root: root}
	for _, sk := range set.Skills {
		out.List = append(out.List, provider.Skill{Name: sk.Name, Description: sk.Description, Path: skills.SkillFile(root, sk.Name)})
	}
	return out
}

// opened lets go of root, the overlay staged for thread's open, as the
// open returns: one that opened an agent makes it the thread's, in place
// of its last agent's; one that didn't leaves the last agent its own.
func (s *skillStage) opened(thread api.ID, root string, ok bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if root != "" {
		if s.opening[root]--; s.opening[root] <= 0 {
			delete(s.opening, root)
		}
	}
	if !ok {
		return
	}
	delete(s.survivors, thread)
	if root == "" {
		delete(s.using, thread)
		return
	}
	s.using[thread] = root
}

// sweep deletes the overlays no agent may read.
func (s *skillStage) sweep() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	keep := slices.Concat(slices.Collect(maps.Values(s.using)), slices.Collect(maps.Keys(s.opening)))
	if len(s.survivors) > 0 {
		keep = append(keep, s.inherited...)
	}
	if err := skills.Sweep(s.dir, keep); err != nil {
		slog.Warn("skills: an overlay no agent uses stays", "err", err)
	}
}

// ended is that the run of thread ended, so an agent the last engine
// started for it no longer runs.
func (s *skillStage) ended(thread api.ID) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.survivors, thread)
}

// deleted is that thread was deleted: its agent's overlay stays till its
// provider has closed the agent (closed).
func (s *skillStage) deleted(thread api.ID) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gone[thread] = true
}

// closed is that thread's provider closed its agent, so an agent the last
// engine started for it no longer runs, and a deleted thread's overlay is
// no one's.
func (s *skillStage) closed(thread api.ID) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.survivors, thread)
	if s.gone[thread] {
		delete(s.using, thread)
		delete(s.gone, thread)
	}
}

// boot keeps every overlay the last engine left while an agent it started
// may still run, the survivors' (restored, uncertain, or with a close not
// done); with none, it deletes them.
func (s *skillStage) boot(survivors []api.ID) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(survivors) == 0 {
		if err := skills.Clear(s.dir); err != nil {
			slog.Warn("skills: the last engine's overlays stay", "err", err)
		}
		return
	}
	for _, id := range survivors {
		s.survivors[id] = true
	}
	entries, _ := os.ReadDir(skills.Runs(s.dir))
	for _, e := range entries {
		s.inherited = append(s.inherited, filepath.Join(skills.Runs(s.dir), e.Name()))
	}
}

// Skills is what Glitched gives the agent of q's thread now, as its next
// run would get it, and how its provider takes it.
func (h *Host) Skills(ctx context.Context, q api.SkillQuery) (api.SkillList, error) {
	t, err := h.store.Thread(ctx, q.Thread)
	if err != nil {
		return api.SkillList{}, err
	}
	if t.ID != q.Thread || t.Deleted {
		return api.SkillList{}, errors.New("no such thread")
	}
	list := api.SkillList{Skills: []api.Skill{}}
	if h.skills == nil {
		return list, nil
	}
	set := skills.Find(h.skills.dir, t.Workspace, t.Project)
	from := ""
	if o := set.Synced; o != nil {
		from = o.Name
		list.Synced = &api.SyncedFrom{Name: o.Name, Pulled: o.Pulled}
	}
	for _, sk := range set.Skills {
		s := api.Skill{Name: sk.Name, Description: sk.Description, Source: string(sk.Source), Shown: sk.Shown, Overrides: sk.Replaces != "", Replaces: string(sk.Replaces)}
		if sk.Source == skills.Synced {
			s.From = from
		}
		list.Skills = append(list.Skills, s)
	}
	for _, s := range set.Skipped {
		list.Skipped = append(list.Skipped, api.SkippedSkill{Source: string(s.Source), Name: s.Name, Why: s.Why})
	}
	if d, ok := h.providers[t.Model.Provider].(provider.Skilled); ok {
		list.Delivery = d.SkillDelivery()
	}
	return list, nil
}

var errNoSkills = errors.New("this engine gives its agents no skills")

// SyncSkills takes q's skills as the ones the next runs get from q's
// machine, once the engine holds every tree of them.
func (h *Host) SyncSkills(_ context.Context, q api.SkillSync) (api.SkillsSynced, error) {
	if h.skills == nil {
		return api.SkillsSynced{}, errNoSkills
	}
	return h.skills.store.Sync(q, time.Now())
}

// PutSkill keeps the files of a skill a sync is to take.
func (h *Host) PutSkill(_ context.Context, p api.SkillPut) error {
	if h.skills == nil {
		return errNoSkills
	}
	return h.skills.store.Put(p)
}

// DropSkills drops the skills d's machine synced here, for runs to come.
func (h *Host) DropSkills(_ context.Context, d api.SkillDrop) error {
	if h.skills == nil {
		return errNoSkills
	}
	return h.skills.store.Drop(d.Source)
}
