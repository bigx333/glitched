package api

import (
	"context"
	"time"
)

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
	// Synced is the machine the engine's synced skills came from, and
	// when that machine last said they were its own; nil for none.
	Synced *SyncedFrom `json:"synced,omitempty"`
}

// Skill is one skill of a SkillList. Source is "home", the user's in the
// engine's state folder, "synced", the user's from the machine From, or
// "project", the workspace's .glitched/skills; Shown is its folder as the
// user reads it; Replaces is the Source of the skill of its name that it
// replaces, "" for none.
type Skill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source"`
	Shown       string `json:"shown"`
	From        string `json:"from,omitempty"`
	Replaces    string `json:"replaces,omitempty"`
}

// SkippedSkill is a skill folder no agent gets, and why: Name is the
// folder's, "" for a whole skills folder.
type SkippedSkill struct {
	Source string `json:"source"`
	Name   string `json:"name,omitempty"`
	Why    string `json:"why"`
}

// SyncedFrom is the machine whose skills an engine holds, by Name, and
// Pulled, when it last said they were its own.
type SyncedFrom struct {
	Name   string    `json:"name"`
	Pulled time.Time `json:"pulled"`
}

// SkillManifest is a machine's skills as it syncs them to another: Set,
// the hash of its skills' names and trees, and each skill's files.
type SkillManifest struct {
	Set    string      `json:"set"`
	Skills []SkillTree `json:"skills"`
}

// SkillTree is one skill of a SkillManifest: Tree is the hash of Files.
type SkillTree struct {
	Name  string      `json:"name"`
	Tree  string      `json:"tree"`
	Files []SkillFile `json:"files"`
}

// SkillFile is one file of a SkillTree: Path is slash-separated, in the
// skill's folder, and Mode "644" or "755".
type SkillFile struct {
	Path   string `json:"path"`
	Mode   string `json:"mode"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// SkillSync offers an engine the skills of the machine whose engine is
// Source, named Name.
type SkillSync struct {
	Source   ID            `json:"source"`
	Name     string        `json:"name"`
	Manifest SkillManifest `json:"manifest"`
}

// SkillsSynced is the skills an engine holds from another machine after a
// SkillSync: none at all when Source is zero. Missing names the trees it
// still needs, by hash, before it takes the offer.
type SkillsSynced struct {
	Source  ID        `json:"source,omitzero"`
	Name    string    `json:"name,omitempty"`
	Set     string    `json:"set,omitempty"`
	Pulled  time.Time `json:"pulled,omitzero"`
	Missing []string  `json:"missing,omitempty"`
}

// SkillPut gives an engine the files of the skill Name whose tree is Tree.
type SkillPut struct {
	Name  string         `json:"name"`
	Tree  string         `json:"tree"`
	Files []SkillContent `json:"files"`
}

// SkillContent is one file of a SkillPut, as SkillFile names it.
type SkillContent struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Data []byte `json:"data"`
}

// SkillDrop has an engine drop the skills it holds from Source's machine.
type SkillDrop struct {
	Source ID `json:"source"`
}

type arrivalKey struct{}

// WithArrival is ctx for a call its engine's server read n-th since the
// engine started, counting every socket: of two calls one machine sent,
// the later read came later.
func WithArrival(ctx context.Context, n uint64) context.Context {
	return context.WithValue(ctx, arrivalKey{}, n)
}

// Arrival is n of WithArrival for ctx's call, 0 for one no server read,
// as one in the engine's own process.
func Arrival(ctx context.Context) uint64 {
	n, _ := ctx.Value(arrivalKey{}).(uint64)
	return n
}
