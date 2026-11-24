package main

import (
	"glitched/internal/api"
)

// skillsRead is what a thread's engine last said of the skills its agent
// gets, for the thread's header: list is nil till it answers, and stale
// says to sync this machine's skills there and ask again, as once the
// window comes back to the front. changed is the engine's SkillsChanged as
// the last ask went: a sync it took since says to ask again, with no sync.
type skillsRead struct {
	list    *api.SkillList
	asking  bool
	stale   bool
	changed uint64
}

// headerSkills are the skills of t its header shows, asking its engine
// for them when it hasn't yet, or when what it said is stale: the answer
// comes as the manager's changes do, and the header draws it then. An
// engine that can't say, as one too old, leaves what it last said.
func (a *app) headerSkills(t *thread) *api.SkillList {
	if t.env == nil {
		return nil
	}
	if a.skills == nil {
		a.skills = map[api.ID]*skillsRead{}
	}
	r := a.skills[t.id()]
	if r == nil {
		r = &skillsRead{stale: true}
		a.skills[t.id()] = r
	}
	if (r.stale || r.changed != t.env.SkillsChanged) && !r.asking {
		sync := r.stale
		r.asking, r.stale, r.changed = true, false, t.env.SkillsChanged
		a.backend.Skills(t.env, t.id(), sync, func(l api.SkillList, err error) {
			r.asking = false
			if err != nil {
				return
			}
			if l.Skills == nil {
				l.Skills = []api.Skill{}
			}
			r.list = &l
		})
	}
	return r.list
}

// skillsStale has every thread's header ask its engine for its skills
// again as it next shows: one added or fixed while the window was away
// shows once it is back.
func (a *app) skillsStale() {
	for _, r := range a.skills {
		r.stale = true
	}
}
