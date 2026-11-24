// Package skills finds the skills Glitched gives the agents it starts: the
// user's in <stateDir>/skills and a project's in .glitched/skills, which no
// agent the user runs by hand sees. Everything in those folders is the
// user's or a repository's, so each skill is checked before any agent gets
// it, and one that fails a check is skipped with the reason.
package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Source is where a skill was found.
type Source string

const (
	Home    Source = "home"
	Synced  Source = "synced"
	Project Source = "project"
)

const (
	// maxSkills is as many skill folders as one root is read for.
	maxSkills = 1000
	maxFiles  = 1000
	maxDepth  = 24
	maxBytes  = 8 << 20
)

// Skill is one skill an agent may get. Its identity is Name and Hash, the
// hash of its files' tree, never where it is: Dir is the folder it is
// read from, its links resolved, and Shown where the user put it, as they
// read it.
type Skill struct {
	Name        string
	Description string
	Source      Source
	Dir         string
	Shown       string
	Hash        string
	// Replaces is the Source of the skill of its name it replaces, "" for
	// none.
	Replaces Source
	// folder is Dir as Find checked it: Build reads it only while Dir is
	// still that folder.
	folder os.FileInfo
}

// Skipped is a skill folder no agent gets, Name as the folder is named, ""
// for a whole root, with Why in words for the user.
type Skipped struct {
	Source Source
	Name   string
	Why    string
}

// Set is what one workspace's agents get: Skills by name, and Shadowed,
// the skills of a name another replaces. Synced is where the synced ones
// came from, nil for none.
type Set struct {
	Skills   []Skill
	Shadowed []Skill
	Skipped  []Skipped
	Synced   *Origin
}

// Hash is the set's identity, by its skills' names and trees.
func (s Set) Hash() string {
	h := sha256.New()
	for _, sk := range s.Skills {
		fmt.Fprintf(h, "%s\x00%s\n", sk.Name, sk.Hash)
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// Find is the skills the agent of a thread in workspace gets: the user's
// in <stateDir>/skills, the user's synced from another machine, which win
// over those by name, and the project's in <workspace>/.glitched/skills,
// which win over both. A thread in a worktree of project whose worktree
// has no .glitched/skills, as skills git doesn't track, gets project's. A
// workspace in stateDir has no project's but for a worktree's: a scratch
// thread gets only the user's. Find reads nothing else in stateDir.
func Find(stateDir, workspace, project string) Set {
	o, ok := readOrigin(stateDir)
	if !ok {
		return find(stateDir, workspace, project, nil)
	}
	return find(stateDir, workspace, project, &o)
}

// find is Find with the skills synced to the engine, nil for none, as
// origin names them.
func find(stateDir, workspace, project string, origin *Origin) Set {
	f := newFinder(stateDir)
	home, _ := f.root(Home, filepath.Join(stateDir, "skills"), "")
	var synced []Skill
	if origin != nil {
		f.set.Synced = origin
		synced, _ = f.root(Synced, filepath.Join(stateDir, syncedDir, "sets", origin.Set), "")
	}
	var mine []Skill
	if base, dir := f.projectRoot(workspace, project); dir != "" {
		mine, _ = f.root(Project, dir, base)
	}
	byName := map[string]int{}
	for _, tier := range [][]Skill{mine, synced, home} {
		for _, sk := range tier {
			i, ok := byName[sk.Name]
			if !ok {
				byName[sk.Name] = len(f.set.Skills)
				f.set.Skills = append(f.set.Skills, sk)
				continue
			}
			if f.set.Skills[i].Replaces == "" {
				f.set.Skills[i].Replaces = sk.Source
			}
			f.set.Shadowed = append(f.set.Shadowed, sk)
		}
	}
	slices.SortFunc(f.set.Skills, func(a, b Skill) int { return strings.Compare(a.Name, b.Name) })
	return f.set
}

type finder struct {
	// state is stateDir, homeRoot <stateDir>/skills, synced the synced
	// sets and worktrees <stateDir>/worktrees, with stateDir's links
	// resolved; homeReal is homeRoot with its own resolved too.
	state, homeRoot, homeReal, synced, worktrees string
	userHome                                     string
	set                                          Set
}

func newFinder(stateDir string) *finder {
	f := &finder{state: real(stateDir), homeReal: real(filepath.Join(stateDir, "skills"))}
	f.homeRoot = filepath.Join(f.state, "skills")
	f.synced = filepath.Join(f.state, syncedDir, "sets")
	f.worktrees = filepath.Join(f.state, "worktrees")
	f.userHome, _ = os.UserHomeDir()
	return f
}

// projectRoot is the folder a workspace's project skills are in, and the
// folder they may not lead out of; "" for none.
func (f *finder) projectRoot(workspace, project string) (base, dir string) {
	if workspace == "" {
		return "", ""
	}
	base = workspace
	if project != "" {
		if _, err := os.Lstat(filepath.Join(workspace, ".glitched", "skills")); errors.Is(err, fs.ErrNotExist) {
			base = project
		}
	}
	if rb := real(base); within(rb, f.state) && !within(rb, f.worktrees) {
		return "", ""
	}
	return base, filepath.Join(base, ".glitched", "skills")
}

// root reads the skills in dir, which a project's may not lead out of
// base. A dir that isn't there holds none; one it can't read is an error,
// besides a Skipped.
func (f *finder) root(src Source, dir, base string) ([]Skill, error) {
	rd, err := filepath.EvalSymlinks(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	shown := f.shown(src, dir, base)
	unread := func(err error) ([]Skill, error) {
		why := fmt.Sprintf("Glitched couldn't read %s: %v", shown, err)
		f.skip(src, "", why)
		return nil, errors.New(why)
	}
	if err != nil {
		return unread(err)
	}
	rb := real(base)
	switch {
	case src == Project && same(rd, f.homeReal):
		return nil, nil
	case src == Project && !within(rd, rb):
		f.skip(src, "", shown+" leads out of its project, so Glitched reads none of it.")
		return nil, nil
	case src == Synced && !within(rd, f.synced):
		f.skip(src, "", shown+" leads out of the synced skills, so Glitched reads none of it.")
		return nil, nil
	case within(rd, f.state) && (src == Home && !same(rd, f.homeRoot) || src == Project && !within(rb, f.worktrees)):
		f.skip(src, "", shown+" leads into Glitched's own folder, so Glitched reads none of it.")
		return nil, nil
	}
	d, err := os.Open(rd)
	if err != nil {
		return unread(err)
	}
	names, err := d.Readdirnames(-1)
	d.Close()
	if err != nil {
		return unread(err)
	}
	names = slices.DeleteFunc(names, func(n string) bool { return strings.HasPrefix(n, ".") })
	slices.Sort(names)
	if len(names) > maxSkills {
		f.skip(src, "", fmt.Sprintf("%s holds %d folders, and Glitched reads only the first %d by name.", shown, len(names), maxSkills))
		names = names[:maxSkills]
	}
	var out []Skill
	for _, name := range names {
		fi, err := os.Lstat(filepath.Join(rd, name))
		if err != nil || !fi.IsDir() && fi.Mode()&fs.ModeSymlink == 0 {
			continue
		}
		sk, err := f.skill(src, filepath.Join(rd, name), rb, name)
		if err != nil {
			f.skip(src, name, err.Error())
			continue
		}
		sk.Shown = filepath.Join(shown, name)
		out = append(out, sk)
	}
	return out, nil
}

// skill reads the skill in dir, the folder named name in a root.
func (f *finder) skill(src Source, dir, base, name string) (Skill, error) {
	if !validName(name) {
		return Skill{}, errors.New("Its folder's name isn't a skill name: up to 64 lowercase letters, digits and single hyphens, starting and ending with a letter or digit.")
	}
	target, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return Skill{}, fmt.Errorf("Glitched couldn't open it: %v", err)
	}
	fi, err := os.Stat(target)
	if err != nil || !fi.IsDir() {
		return Skill{}, errors.New("It links to something that isn't a folder.")
	}
	switch {
	case src == Project && !within(target, base):
		return Skill{}, errors.New("It links out of its project.")
	case src == Synced && !within(target, f.synced):
		return Skill{}, errors.New("It links out of the synced skills.")
	case within(target, f.state) && (src == Home && !within(target, f.homeRoot) || src == Project && !within(base, f.worktrees)):
		return Skill{}, errors.New("It links into Glitched's own folder.")
	}
	var md []byte
	hash, err := walk(target, fi, func(rel string, _ bool, data []byte) error {
		if rel == "SKILL.md" {
			md = data
		}
		return nil
	})
	if err != nil {
		return Skill{}, err
	}
	if md == nil {
		return Skill{}, errors.New("It has no SKILL.md.")
	}
	fm, err := frontmatter(md)
	if err != nil {
		return Skill{}, err
	}
	if fm.Name != name {
		return Skill{}, fmt.Errorf("Its SKILL.md names it %q, and its folder %q: the two must match.", fm.Name, name)
	}
	return Skill{Name: name, Description: fm.Description, Source: src, Dir: target, Hash: hash, folder: fi}, nil
}

func (f *finder) skip(src Source, name, why string) {
	f.set.Skipped = append(f.set.Skipped, Skipped{Source: src, Name: name, Why: why})
}

// shown is a root as the user reads it: the user's under ~, and a
// project's under its folder's name.
func (f *finder) shown(src Source, dir, base string) string {
	if src == Project {
		if rel, err := filepath.Rel(base, dir); err == nil {
			return filepath.Join(filepath.Base(base), rel)
		}
		return dir
	}
	if rel, err := filepath.Rel(f.userHome, dir); f.userHome != "" && err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.Join("~", rel)
	}
	return dir
}

// validName is whether name may name a skill, as Claude Code and Codex
// name one.
func validName(name string) bool {
	if len(name) == 0 || len(name) > 64 || strings.Contains(name, "--") || name[0] == '-' || name[len(name)-1] == '-' {
		return false
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

// real is path with its links resolved, or absolute and clean where it
// can't be.
func real(path string) string {
	if path == "" {
		return ""
	}
	if r, err := filepath.EvalSymlinks(path); err == nil {
		return r
	}
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return filepath.Clean(path)
}

// within is whether path is dir or in it, by the folders themselves, not
// how either is spelt: path or a folder it is in is dir, as a filesystem
// that ignores case or a second name for a folder still says. A dir that
// isn't there holds nothing, nor does one that is a link: dir names the
// folder itself.
func within(path, dir string) bool {
	if path == "" || dir == "" {
		return false
	}
	d, err := os.Lstat(dir)
	if err != nil {
		return false
	}
	for p := filepath.Clean(path); ; p = filepath.Dir(p) {
		if fi, err := os.Stat(p); err == nil && os.SameFile(fi, d) {
			return true
		}
		if p == filepath.Dir(p) {
			return false
		}
	}
}

// same is whether a is the folder b names, however spelt; a b that is a
// link is no folder.
func same(a, b string) bool {
	fa, errA := os.Stat(a)
	fb, errB := os.Lstat(b)
	return errA == nil && errB == nil && os.SameFile(fa, fb)
}
