package api

// SkillQuery asks the engine of Thread's machine for the skills Glitched
// gives its agent.
type SkillQuery struct {
	Thread ID `json:"thread"`
}

// SkillList is the skills Glitched gives a thread's agent, as its engine
// finds them now: what the thread's next run gets.
type SkillList struct {
	Skills  []Skill        `json:"skills"`
	Skipped []SkippedSkill `json:"skipped,omitempty"`
	// Delivery is how the thread's provider gets them, in words for the
	// user; "" for a provider that gets none.
	Delivery string `json:"delivery,omitempty"`
}

// Skill is one skill of a SkillList. Source is "home", the user's in the
// engine's state folder, or "project", the workspace's .glitched/skills;
// Shown is its folder as the user reads it; Overrides says a project's
// skill replaces the user's own of its name.
type Skill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source"`
	Shown       string `json:"shown"`
	Overrides   bool   `json:"overrides,omitempty"`
}

// SkippedSkill is a skill folder no agent gets, and why: Name is the
// folder's, "" for a whole skills folder.
type SkippedSkill struct {
	Source string `json:"source"`
	Name   string `json:"name,omitempty"`
	Why    string `json:"why"`
}
