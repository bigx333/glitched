package main

import (
	"glitched/internal/api"
)

// skillsRead is what a thread's engine last said of the skills its agent
// gets, for the thread's header: list is nil till it answers, and stale
// says to ask again, as once the window comes back to the front.
type skillsRead struct {
	list   *api.SkillList
	asking bool
	stale  bool
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
	if r.stale && !r.asking {
		r.asking, r.stale = true, false
		a.backend.Skills(t.env, t.id(), func(l api.SkillList, err error) {
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
