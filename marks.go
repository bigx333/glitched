package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"hash/fnv"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"glitched/internal/client"
	"glitched/internal/jsonfile"
)

// projectMark is a project's mark as marks.json keeps it, in the order the
// projects were first seen.
type projectMark struct {
	Name  string `json:"name"`
	Icon  string `json:"icon"`
	Color string `json:"color"`
}

// hashMark is the icon and color a name's hash picks, as indexes into
// projectIcons and projectColors.
func hashMark(name string) (icon, color int) {
	h := fnv.New32a()
	h.Write([]byte(name))
	n := h.Sum32()
	return int(n % uint32(len(projectIcons))), int(n / 7 % uint32(len(projectColors)))
}

// drawn is the icon and color m is drawn with, as indexes: its own, or its
// name's hash pick for one this version doesn't know.
func drawn(m projectMark) (icon, color int) {
	icon, color = hashMark(m.Name)
	if i := slices.Index(projectIcons, m.Icon); i >= 0 {
		icon = i
	}
	if c := slices.Index(projectColors, m.Color); c >= 0 {
		color = c
	}
	return icon, color
}

// frozen is whether m has an icon or color this version doesn't know, which
// a newer one wrote, so it is never rewritten.
func frozen(m projectMark) bool {
	return !slices.Contains(projectIcons, m.Icon) || !slices.Contains(projectColors, m.Color)
}

type iconSet uint8

func (s iconSet) has(i int) bool { return s&(1<<i) != 0 }

func (s *iconSet) add(i int) { *s |= 1 << i }

type pairGrade uint8

const (
	pairTaken pairGrade = iota
	pairFreeShown
	pairFreeAll
)

// settleMarks gives every shown name a mark. stored is in first-seen order;
// the result keeps that order, rewrites in place the entries that move, and
// appends new names (in name order). changed is whether the result differs
// from stored.
func settleMarks(stored []projectMark, shown []string) (out []projectMark, changed bool) {
	out = slices.Clone(stored)
	want := map[string]bool{}
	for _, n := range shown {
		want[n] = true
	}
	iconUses := make([]int, len(projectIcons))
	pairs := map[[2]int]bool{}
	hold := func(icon, color int) {
		iconUses[icon]++
		pairs[[2]int{icon, color}] = true
	}

	// Icons go round while the shown, an unknown mark counted once per icon
	// it's drawn with, fit on them.
	room := len(want)
	frozenIcons := map[int]bool{}
	absentIcons, absentPairs := map[int]bool{}, map[[2]int]bool{}
	seen := map[string]bool{}
	for _, m := range out {
		if seen[m.Name] {
			continue
		}
		seen[m.Name] = true
		icon, color := drawn(m)
		switch {
		case !want[m.Name]:
			absentIcons[icon] = true
			absentPairs[[2]int{icon, color}] = true
		case frozen(m):
			frozenIcons[icon] = true
			room--
		}
	}
	byIcon := room+len(frozenIcons) <= len(projectIcons)

	type pick struct {
		at          int // the entry in out; -1 for a new name
		name        string
		icon, color int // the ones to start from: stored, or the hash ones
		hashIcon    int
		olderAbsent iconSet
	}
	var picks []pick
	var olderAbsent iconSet
	clear(seen)
	for i, m := range out {
		if seen[m.Name] {
			continue
		}
		seen[m.Name] = true
		icon, color := drawn(m)
		switch {
		case !want[m.Name]:
			olderAbsent.add(icon)
		case frozen(m):
			if !pairs[[2]int{icon, color}] {
				hold(icon, color)
			}
		case !byIcon || iconUses[icon] == 0:
			hold(icon, color)
		default:
			hi, _ := hashMark(m.Name)
			picks = append(picks, pick{i, m.Name, icon, color, hi, olderAbsent})
		}
	}
	var fresh []string
	for n := range want {
		if !seen[n] {
			fresh = append(fresh, n)
		}
	}
	slices.Sort(fresh)
	for _, n := range fresh {
		hi, hc := hashMark(n)
		picks = append(picks, pick{-1, n, hi, hc, hi, olderAbsent})
	}

	grade := func(i int) pairGrade {
		g := pairTaken
		for c := range projectColors {
			if pair := [2]int{i, c}; !pairs[pair] {
				if !absentPairs[pair] {
					return pairFreeAll
				}
				g = pairFreeShown
			}
		}
		return g
	}
	pickIcon := func(p pick) (int, bool) {
		n := len(projectIcons)
		if byIcon {
			free := func(ok func(i int) bool) (int, bool) {
				for d := range n {
					if i := (p.icon + d) % n; iconUses[i] == 0 && ok(i) {
						return i, true
					}
				}
				return 0, false
			}
			if i, ok := free(func(i int) bool { return !absentIcons[i] }); ok {
				return i, true
			}
			if i, ok := free(func(i int) bool { return !p.olderAbsent.has(i) }); ok || p.at >= 0 {
				return i, ok
			}
			return free(func(int) bool { return true })
		}
		best := p.hashIcon
		for d := range n {
			i := (p.hashIcon + d) % n
			if g, b := grade(i), grade(best); g > b || g == b && iconUses[i] < iconUses[best] {
				best = i
			}
		}
		return best, true
	}
	pickColor := func(p pick, icon int) int {
		n := len(projectColors)
		free := -1
		for d := range n {
			c := (p.color + d) % n
			if pair := [2]int{icon, c}; !pairs[pair] {
				if !absentPairs[pair] {
					return c
				}
				if free < 0 {
					free = c
				}
			}
		}
		if free >= 0 {
			return free
		}
		return p.color
	}
	settle := func(p pick, icon int, move bool) {
		if !move {
			hold(p.icon, p.color)
			return
		}
		color := pickColor(p, icon)
		hold(icon, color)
		m := projectMark{p.name, projectIcons[icon], projectColors[color]}
		if p.at < 0 {
			out = append(out, m)
		} else {
			out[p.at] = m
		}
		changed = true
	}

	// The first to hash to an icon no one holds takes it, before any other
	// picks, so that a name alone on its hash icon keeps it. One that moves
	// takes it only where no one absent is drawn with it.
	var rest []pick
	for _, p := range picks {
		if iconUses[p.hashIcon] > 0 || p.at >= 0 && absentIcons[p.hashIcon] || !byIcon && grade(p.hashIcon) < pairFreeAll {
			rest = append(rest, p)
			continue
		}
		settle(p, p.hashIcon, true)
	}
	for _, p := range rest {
		icon, move := pickIcon(p)
		settle(p, icon, move)
	}
	// New names were appended claims first; put them in name order.
	slices.SortStableFunc(out[len(stored):], func(a, b projectMark) int {
		return strings.Compare(a.Name, b.Name)
	})
	return out, changed
}

// marksByName is each name's mark to draw: the stored icon and colour, or the
// name's hash pick for a value this version doesn't know.
func marksByName(marks []projectMark) map[string]projectMark {
	by := map[string]projectMark{}
	for _, m := range marks {
		if _, ok := by[m.Name]; ok {
			continue
		}
		icon, color := drawn(m)
		by[m.Name] = projectMark{m.Name, projectIcons[icon], projectColors[color]}
	}
	return by
}

func (a *app) marksPath() string { return filepath.Join(a.dir, "marks.json") }

// marksFile is what a stat of marks.json says of it: another window that
// writes it, in place or by moving a file over it, changes its time, its
// size or the file. One that writes as many bytes in place within the
// same tick of the clock goes unseen till the next write; that is the
// risk taken for a stat a rebuild, not a read.
type marksFile struct{ fi os.FileInfo }

func (a *app) statMarks() marksFile {
	fi, err := os.Stat(a.marksPath())
	if err != nil {
		return marksFile{}
	}
	return marksFile{fi}
}

func (f marksFile) same(g marksFile) bool {
	if f.fi == nil || g.fi == nil {
		return f.fi == g.fi
	}
	return os.SameFile(f.fi, g.fi) && f.fi.ModTime().Equal(g.fi.ModTime()) && f.fi.Size() == g.fi.Size()
}

func decodeMarks(raw json.RawMessage) (ms []projectMark, ours bool) {
	if len(raw) == 0 {
		return nil, true
	}
	var entries []json.RawMessage
	if t := bytes.TrimSpace(raw); len(t) == 0 || t[0] != '[' || json.Unmarshal(t, &entries) != nil {
		return nil, false
	}
	ours = true
	for _, e := range entries {
		ours = ours && writtenMark(e)
		var m projectMark
		if t := bytes.TrimSpace(e); len(t) > 0 && t[0] == '{' && json.Unmarshal(t, &m) == nil && m.Name != "" {
			ms = append(ms, m)
		}
	}
	return ms, ours
}

func writtenMark(e json.RawMessage) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(e, &fields) != nil || len(fields) != 3 {
		return false
	}
	for _, k := range []string{"name", "icon", "color"} {
		var v string
		if f := fields[k]; len(f) == 0 || f[0] != '"' || json.Unmarshal(f, &v) != nil || v == "" {
			return false
		}
	}
	return true
}

// loadMarks takes the projects' marks as marks.json holds them. One this
// version doesn't write is never written here, till it changes.
func (a *app) loadMarks() (corrupt bool) {
	a.marksRead, a.marksShown = a.statMarks(), ""
	var raw json.RawMessage
	if err := jsonfile.Load(a.marksPath(), &raw); err != nil {
		log.Printf("glitched: marks: %v", err)
		if len(a.marks) > 0 {
			a.heldMarks(false)
			syntax, kind := (*json.SyntaxError)(nil), (*json.UnmarshalTypeError)(nil)
			return errors.As(err, &syntax) || errors.As(err, &kind)
		}
	} else {
		a.marksUnsaved = false
	}
	ms, ours := decodeMarks(raw)
	a.heldMarks(!ours)
	a.marks = ms
	a.drawnMarks = marksByName(a.marks)
	return false
}

// heldMarks is whether marks.json is one this version doesn't write, and
// so the marks are held here; it says so once.
func (a *app) heldMarks(held bool) {
	if held && !a.marksHeld {
		log.Printf("glitched: marks: %s is not one this version writes; marks held for this session", a.marksPath())
	}
	a.marksHeld = held
}

// errMarksSettled leaves marks.json as it is: what another window wrote
// there settles the projects shown here already.
var errMarksSettled = errors.New("marks settled")

// errMarksHeld leaves marks.json as it is: another version wrote it.
var errMarksHeld = errors.New("marks held")

func (a *app) syncMarks() {
	if a.dir != "" && !a.statMarks().same(a.marksRead) {
		a.settleShownMarks()
	}
}

func (a *app) waitingForLocalSnapshot() bool {
	if len(a.marks) > 0 || a.dir == "" || a.backend == nil {
		return false
	}
	e := a.localEnv()
	return e != nil && e.Log().IsZero() && e.Status == client.Status(client.Connecting{})
}

// settleShownMarks gives the projects the sidebar has a mark each, as
// settleMarks does, onto what another window wrote to marks.json since,
// and writes the file only when that changes it. With the same projects
// shown, and the file as it was, there is nothing to do.
func (a *app) settleShownMarks() {
	// A file gone leaves the marks here, to write again on this rebuild.
	rewrite := false
	if f := a.statMarks(); a.dir != "" && !f.same(a.marksRead) {
		if f.same(marksFile{}) {
			a.marksRead, rewrite = f, len(a.marks) > 0
			a.heldMarks(false)
		} else {
			rewrite = a.loadMarks()
		}
	}
	var names []string
	for _, p := range a.projects {
		if !p.noProject {
			names = append(names, p.name())
		}
	}
	slices.Sort(names)
	names = slices.Compact(names)
	shown := strings.Join(names, "\x00")
	if shown == a.marksShown && a.drawnMarks != nil && !rewrite {
		return
	}
	if a.waitingForLocalSnapshot() {
		out, _ := settleMarks(nil, names)
		a.drawnMarks = marksByName(out)
		for _, p := range a.projects {
			a.markProject(p)
		}
		return
	}
	a.marksShown = shown
	before := a.marks
	if out, changed := settleMarks(a.marks, names); changed || rewrite {
		a.marks = out
		if a.dir != "" && !a.marksHeld {
			var ms []projectMark
			_, err := jsonfile.EditOrReset(a.marksPath(), func(raw *json.RawMessage) error {
				from, ours := decodeMarks(*raw)
				if !ours {
					return errMarksHeld
				}
				// A file gone, or made anew, leaves the marks here to keep.
				disk := len(from)
				if disk == 0 || a.marksUnsaved {
					for _, m := range before {
						if !slices.ContainsFunc(from, func(o projectMark) bool { return o.Name == m.Name }) {
							from = append(from, m)
						}
					}
				}
				out, changed := settleMarks(from, names)
				if ms = out; !changed && len(from) == disk {
					return errMarksSettled
				}
				b, err := json.Marshal(out)
				*raw = b
				return err
			})
			switch {
			case err == nil || errors.Is(err, errMarksSettled):
				a.marks, a.marksRead, a.marksUnsaved = ms, a.statMarks(), false
			case errors.Is(err, errMarksHeld):
				a.heldMarks(true)
			default:
				a.marksUnsaved = true
				log.Printf("glitched: marks: %v", err)
			}
		}
	}
	a.drawnMarks = marksByName(a.marks)
	for _, p := range a.projects {
		a.markProject(p)
	}
}

// markProject gives p the mark drawn for its name, if it has one.
func (a *app) markProject(p *project) {
	if m, ok := a.drawnMarks[p.name()]; ok && !p.noProject {
		p.icon, p.color = m.Icon, m.Color
	}
}
