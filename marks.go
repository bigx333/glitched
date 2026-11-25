package main

import (
	"encoding/json"
	"errors"
	"hash/fnv"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

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

// settleMarks gives every shown name a mark. stored is in first-seen order;
// the result keeps that order, rewrites in place the entries that move, and
// appends new names (in name order). changed is whether the result differs
// from stored.
//
// While icons go round, a mark seen before keeps its icon unless one seen
// before it, and shown, holds it; then it moves to a free icon. Past that,
// icons repeat anyway and a mark seen before never moves: only new names
// pick, each onto a pair no one shown holds while pairs go round. One this
// version can't rewrite holds what it's drawn with, shared or not, and takes
// up only that. Those not shown hold nothing, but a name that moves takes
// none of their icons as its hash icon, and past 8 a new name keeps off
// their pairs where it can.
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
	}
	var picks []pick
	clear(seen)
	for i, m := range out {
		if !want[m.Name] || seen[m.Name] {
			continue
		}
		seen[m.Name] = true
		icon, color := drawn(m)
		switch {
		case frozen(m):
			if !pairs[[2]int{icon, color}] {
				hold(icon, color)
			}
		case !byIcon || iconUses[icon] == 0:
			hold(icon, color)
		default:
			hi, _ := hashMark(m.Name)
			picks = append(picks, pick{i, m.Name, icon, color, hi})
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
		picks = append(picks, pick{-1, n, hi, hc, hi})
	}

	// pickIcon is the first free icon from p's own while icons go round.
	// Past that, it is from p's hash icon the least used of those with a
	// pair no one shown or absent has, else with a pair free, else of all.
	pickIcon := func(p pick) int {
		n := len(projectIcons)
		if byIcon {
			for d := range n {
				if i := (p.icon + d) % n; iconUses[i] == 0 {
					return i
				}
			}
		}
		grade := func(i int) int {
			g := 0
			for c := range projectColors {
				if pair := [2]int{i, c}; !pairs[pair] {
					if !absentPairs[pair] {
						return 2
					}
					g = 1
				}
			}
			return g
		}
		best := p.hashIcon
		for d := range n {
			i := (p.hashIcon + d) % n
			if g, b := grade(i), grade(best); g > b || g == b && iconUses[i] < iconUses[best] {
				best = i
			}
		}
		return best
	}
	// pickColor is p's own colour on icon if that pair is free, else the
	// first free one from it; past 8, first one no one absent has either.
	pickColor := func(p pick, icon int) int {
		n := len(projectColors)
		free := -1
		for d := range n {
			c := (p.color + d) % n
			if pair := [2]int{icon, c}; !pairs[pair] {
				if byIcon || !absentPairs[pair] {
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
	settle := func(p pick, icon int) {
		color := pickColor(p, icon)
		hold(icon, color)
		m := projectMark{p.name, projectIcons[icon], projectColors[color]}
		switch {
		case p.at < 0:
			out = append(out, m)
			changed = true
		default:
			out[p.at] = m
			changed = true
		}
	}

	// The first to hash to an icon no one holds takes it, before any other
	// picks, so that a name alone on its hash icon keeps it. One that moves
	// takes it only where no one absent is drawn with it.
	var rest []pick
	for _, p := range picks {
		if iconUses[p.hashIcon] > 0 || p.at >= 0 && absentIcons[p.hashIcon] {
			rest = append(rest, p)
			continue
		}
		settle(p, p.hashIcon)
	}
	for _, p := range rest {
		settle(p, pickIcon(p))
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
// writes it changes its time or its size, or both. One that writes as
// many bytes within the same tick of the clock goes unseen till the next
// write; that is the risk taken for a stat a rebuild, not a read.
type marksFile struct {
	mod  time.Time
	size int64
}

func (a *app) statMarks() marksFile {
	fi, err := os.Stat(a.marksPath())
	if err != nil {
		return marksFile{}
	}
	return marksFile{fi.ModTime(), fi.Size()}
}

func (f marksFile) same(g marksFile) bool { return f.mod.Equal(g.mod) && f.size == g.size }

// decodeMarks is the marks in raw, a JSON value, in their order, and
// whether raw is an array of marks this version writes. An entry that
// doesn't decode, or has no name, is skipped. A newer version's extra fields on an entry
// are dropped when this one writes the entry again.
func decodeMarks(raw json.RawMessage) (ms []projectMark, ours bool) {
	var entries []json.RawMessage
	if raw != nil && json.Unmarshal(raw, &entries) != nil {
		return nil, false
	}
	ours = true
	for _, e := range entries {
		var m projectMark
		if json.Unmarshal(e, &m) != nil || m.Name == "" {
			ours = false
			continue
		}
		ms = append(ms, m)
	}
	return ms, ours
}

// loadMarks takes the projects' marks as marks.json holds them. One this
// version doesn't write is never written here, till it changes.
func (a *app) loadMarks() {
	a.marksRead, a.marksShown = a.statMarks(), ""
	var raw json.RawMessage
	if err := jsonfile.Load(a.marksPath(), &raw); err != nil {
		log.Printf("glitched: marks: %v", err)
	}
	ms, ours := decodeMarks(raw)
	a.heldMarks(!ours)
	a.marks = ms
	a.drawnMarks = marksByName(a.marks)
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

// settleShownMarks gives the projects the sidebar has a mark each, as
// settleMarks does, onto what another window wrote to marks.json since,
// and writes the file only when that changes it. Marks it couldn't save
// hold here for as long as the app runs. With the same projects shown,
// and the file as it was, there is nothing to do.
func (a *app) settleShownMarks() {
	// A file gone leaves the marks here, to write again on this rebuild.
	rewrite := false
	if f := a.statMarks(); a.dir != "" && !f.same(a.marksRead) {
		if f.same(marksFile{}) {
			a.marksRead, rewrite = f, len(a.marks) > 0
			a.heldMarks(false)
		} else {
			a.loadMarks()
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
				lost := len(from) == 0 && len(before) > 0
				if lost {
					from = before
				}
				out, changed := settleMarks(from, names)
				if ms = out; !changed && !lost {
					return errMarksSettled
				}
				b, err := json.Marshal(out)
				*raw = b
				return err
			})
			switch {
			case err == nil || errors.Is(err, errMarksSettled):
				a.marks, a.marksRead = ms, a.statMarks()
			case errors.Is(err, errMarksHeld):
				a.heldMarks(true)
			default:
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
