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
}

// Skipped is a skill folder no agent gets, Name as the folder is named, ""
// for a whole root, with Why in words for the user.
type Skipped struct {
	Source Source
	Name   string
	Why    string
}

// Set is what one workspace's agents get: Skills by name, and Shadowed,
// the user's own that a project's skill of the same name replaces.
type Set struct {
	Skills   []Skill
	Shadowed []Skill
	Skipped  []Skipped
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
// in <stateDir>/skills, and the project's in <workspace>/.glitched/skills,
// which win by name. A thread in a worktree of project whose worktree has
// no .glitched/skills, as skills git doesn't track, gets project's. A
// workspace in stateDir has no project's but for a worktree's: a scratch
// thread gets only the user's. Find reads nothing else in stateDir.
func Find(stateDir, workspace, project string) Set {
	f := finder{state: real(stateDir), homeReal: real(filepath.Join(stateDir, "skills"))}
	f.homeRoot = filepath.Join(f.state, "skills")
	f.worktrees = filepath.Join(f.state, "worktrees")
	f.userHome, _ = os.UserHomeDir()
	home := f.root(Home, filepath.Join(stateDir, "skills"), "")
	var mine []Skill
	if base, dir := f.projectRoot(workspace, project); dir != "" {
		mine = f.root(Project, dir, base)
	}
	byName := map[string]bool{}
	for _, sk := range mine {
		byName[sk.Name] = true
	}
	f.set.Skills = mine
	for _, sk := range home {
		if byName[sk.Name] {
			f.set.Shadowed = append(f.set.Shadowed, sk)
		} else {
			f.set.Skills = append(f.set.Skills, sk)
		}
	}
	slices.SortFunc(f.set.Skills, func(a, b Skill) int { return strings.Compare(a.Name, b.Name) })
	return f.set
}

type finder struct {
	// state is stateDir, homeRoot <stateDir>/skills and worktrees
	// <stateDir>/worktrees, with stateDir's links resolved; homeReal is
	// homeRoot with its own resolved too.
	state, homeRoot, homeReal, worktrees string
	userHome                             string
	set                                  Set
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
// base.
func (f *finder) root(src Source, dir, base string) []Skill {
	rd, err := filepath.EvalSymlinks(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	shown := f.shown(src, dir, base)
	if err != nil {
		f.skip(src, "", fmt.Sprintf("Glitched couldn't read %s: %v", shown, err))
		return nil
	}
	rb := real(base)
	switch {
	case src == Project && rd == f.homeReal:
		return nil
	case src == Project && !within(rd, rb):
		f.skip(src, "", shown+" leads out of its project, so Glitched reads none of it.")
		return nil
	case within(rd, f.state) && (src == Home && rd != f.homeRoot || src == Project && !within(rb, f.worktrees)):
		f.skip(src, "", shown+" leads into Glitched's own folder, so Glitched reads none of it.")
		return nil
	}
	d, err := os.Open(rd)
	if err != nil {
		f.skip(src, "", fmt.Sprintf("Glitched couldn't read %s: %v", shown, err))
		return nil
	}
	names, err := d.Readdirnames(-1)
	d.Close()
	if err != nil {
		f.skip(src, "", fmt.Sprintf("Glitched couldn't read %s: %v", shown, err))
		return nil
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
	return out
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
	if fi, err := os.Stat(target); err != nil || !fi.IsDir() {
		return Skill{}, errors.New("It links to something that isn't a folder.")
	}
	switch {
	case src == Project && !within(target, base):
		return Skill{}, errors.New("It links out of its project.")
	case within(target, f.state) && (src == Home && !within(target, f.homeRoot) || src == Project && !within(base, f.worktrees)):
		return Skill{}, errors.New("It links into Glitched's own folder.")
	}
	var md []byte
	hash, err := walk(target, func(rel string, _ bool, data []byte) error {
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
	return Skill{Name: name, Description: fm.Description, Source: src, Dir: target, Hash: hash}, nil
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
	if f.userHome != "" && within(dir, f.userHome) {
		rel, _ := filepath.Rel(f.userHome, dir)
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

// within is whether path is dir or in it.
func within(path, dir string) bool {
	if path == "" || dir == "" {
		return false
	}
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
