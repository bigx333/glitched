package main

import (
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

// settleMarks gives every shown name a mark no other shown name has while
// icons go round. stored is in first-seen order; the result keeps that order,
// rewrites entries that had to move in place, and appends new names (in name
// order). changed is whether the result differs from stored.
func settleMarks(stored []projectMark, shown []string) (out []projectMark, changed bool) {
	out = slices.Clone(stored)
	want := map[string]bool{}
	for _, n := range shown {
		want[n] = true
	}

	type pick struct {
		at          int // the entry in out; -1 for a new name
		name        string
		icon, color int // the ones to start from: stored, or the hash ones
		hashIcon    int
		claim       bool
	}
	iconUses := make([]int, len(projectIcons))
	colorUses := make([]int, len(projectColors))
	pairs := map[[2]int]bool{}
	hold := func(icon, color int) {
		iconUses[icon]++
		colorUses[color]++
		pairs[[2]int{icon, color}] = true
	}

	// The names seen before keep their marks, the first seen of two that
	// clash keeping it, and the rest pick again. They clash on an icon while
	// icons go round, on the pair while pairs do, and past that never. One
	// this version can't rewrite holds what it's drawn with, shared or not,
	// and takes up only that.
	room := len(want)
	frozenIcons, frozenPairs := map[int]bool{}, map[[2]int]bool{}
	seen := map[string]bool{}
	for _, m := range out {
		if want[m.Name] && !seen[m.Name] {
			seen[m.Name] = true
			if frozen(m) {
				icon, color := drawn(m)
				frozenIcons[icon] = true
				frozenPairs[[2]int{icon, color}] = true
				room--
			}
		}
	}
	byIcon := room+len(frozenIcons) <= len(projectIcons)
	byPair := !byIcon && room+len(frozenPairs) <= len(projectIcons)*len(projectColors)
	clash := func(icon, color int) bool {
		switch {
		case byIcon:
			return iconUses[icon] > 0
		case byPair:
			return pairs[[2]int{icon, color}]
		}
		return false
	}
	// Those not shown hold nothing, but a name that moves keeps off their
	// marks where it can, those seen before it first, so that the projects
	// shown now with some, now with others, settle once and stay.
	type absentMark struct{ at, icon, color int }
	var absent []absentMark
	gone := map[string]bool{}
	for i, m := range out {
		if !want[m.Name] && !gone[m.Name] {
			gone[m.Name] = true
			icon, color := drawn(m)
			absent = append(absent, absentMark{i, icon, color})
		}
	}
	var picks []pick
	clear(seen)
	for i, m := range out {
		if !want[m.Name] || seen[m.Name] {
			continue
		}
		seen[m.Name] = true
		icon, color := drawn(m)
		if frozen(m) {
			// Drawn with a pair another holds, it takes up nothing more.
			if !pairs[[2]int{icon, color}] {
				hold(icon, color)
			}
			continue
		}
		if !clash(icon, color) {
			hold(icon, color)
			continue
		}
		hi, _ := hashMark(m.Name)
		picks = append(picks, pick{at: i, name: m.Name, icon: icon, color: color, hashIcon: hi})
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
		picks = append(picks, pick{at: -1, name: n, icon: hi, color: hc, hashIcon: hi})
	}

	settle := func(p pick, icon, color int) {
		hold(icon, color)
		m := projectMark{p.name, projectIcons[icon], projectColors[color]}
		switch {
		case p.at < 0:
			out = append(out, m)
			changed = true
		case out[p.at] != m:
			out[p.at] = m
			changed = true
		}
	}
	// avoid is what p keeps off: 3 for an icon or pair no one shown or
	// absent has, 2 for one only those absent seen after p have, 1 for one
	// only those absent have, 0 for one shown.
	avoid := func(p pick) (iconRank func(int) int, pairRank func(int, int) int) {
		before := p.at
		if before < 0 {
			before = len(out)
		}
		rank := func(shown bool, absentHas func(absentMark) bool) int {
			if shown {
				return 0
			}
			r := 3
			for _, x := range absent {
				if absentHas(x) {
					r = min(r, 2)
					if x.at < before {
						return 1
					}
				}
			}
			return r
		}
		iconRank = func(i int) int {
			return rank(iconUses[i] > 0, func(x absentMark) bool { return x.icon == i })
		}
		pairRank = func(i, c int) int {
			return rank(pairs[[2]int{i, c}], func(x absentMark) bool { return x.icon == i && x.color == c })
		}
		return iconRank, pairRank
	}
	pickColor := func(p pick, icon int) int {
		_, pairRank := avoid(p)
		n := len(projectColors)
		best, bestRank := p.color, pairRank(icon, p.color)
		for d := range n {
			c := (p.color + d) % n
			if r := pairRank(icon, c); r > bestRank {
				best, bestRank = c, r
			}
		}
		return best
	}
	// pickIcon is the icon for p, which must move or may: while icons go
	// round, a free one, kept off those absent have; while pairs do, one
	// with a pair free, its own if it has one, then a free one. Past that,
	// the least used; the first from p's hash icon of those as good.
	pickIcon := func(p pick) int {
		iconRank, pairRank := avoid(p)
		score := func(i int) []int {
			colors := 0
			for c := range projectColors {
				colors = max(colors, pairRank(i, c))
			}
			free, own := 0, 0
			if iconUses[i] == 0 {
				free = 1
			}
			if p.at >= 0 && i == p.icon {
				own = 1
			}
			if byIcon {
				return []int{free, min(iconRank(i), 2), iconRank(i), colors, -iconUses[i]}
			}
			return []int{min(colors, 1), own, colors, free, min(iconRank(i), 2), iconRank(i), -iconUses[i]}
		}
		n := len(projectIcons)
		from := p.hashIcon
		if p.at >= 0 {
			from = p.icon
		}
		best := from
		for d := range n {
			if i := (from + d) % n; slices.Compare(score(i), score(best)) > 0 {
				best = i
			}
		}
		return best
	}

	// The first to hash to an icon no one holds takes it, before any that
	// must move does, so that a name alone on its hash icon gets it. One
	// seen before claims it only where it must move anyway, while icons go
	// round, and none seen before it is absent with it.
	claimed := map[int]bool{}
	for k, p := range picks {
		if iconUses[p.hashIcon] > 0 || claimed[p.hashIcon] {
			continue
		}
		if p.at >= 0 {
			if iconRank, _ := avoid(p); !byIcon || iconRank(p.hashIcon) < 2 {
				continue
			}
		}
		claimed[p.hashIcon] = true
		picks[k].claim = true
	}
	for _, p := range picks {
		if p.claim {
			settle(p, p.hashIcon, pickColor(p, p.hashIcon))
		}
	}
	for _, p := range picks {
		if !p.claim {
			icon := pickIcon(p)
			settle(p, icon, pickColor(p, icon))
		}
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

// loadMarks takes the projects' marks as marks.json holds them.
func (a *app) loadMarks() {
	a.marksRead, a.marksShown = a.statMarks(), ""
	a.marks = nil
	if err := jsonfile.Load(a.marksPath(), &a.marks); err != nil {
		log.Printf("glitched: marks: %v", err)
		a.marks = nil
	}
	a.drawnMarks = marksByName(a.marks)
}

// errMarksSettled leaves marks.json as it is: what another window wrote
// there settles the projects shown here already.
var errMarksSettled = errors.New("marks settled")

// settleShownMarks gives the projects the sidebar has a mark each, as
// settleMarks does, onto what another window wrote to marks.json since,
// and writes the file only when that changes it. Marks it couldn't save
// hold here for as long as the app runs. With the same projects shown,
// and the file as it was, there is nothing to do.
func (a *app) settleShownMarks() {
	// A file gone leaves the marks here, to write again.
	if f := a.statMarks(); a.dir != "" && !f.same(marksFile{}) && !f.same(a.marksRead) {
		a.loadMarks()
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
	if shown == a.marksShown && a.drawnMarks != nil {
		return
	}
	a.marksShown = shown
	before := a.marks
	if out, changed := settleMarks(a.marks, names); changed {
		a.marks = out
		if a.dir != "" {
			ms, err := jsonfile.EditOrReset(a.marksPath(), func(ms *[]projectMark) error {
				// A file gone, or made anew, leaves the marks here to keep.
				from, lost := *ms, len(*ms) == 0 && len(before) > 0
				if lost {
					from = before
				}
				out, changed := settleMarks(from, names)
				if !changed && !lost {
					return errMarksSettled
				}
				*ms = out
				return nil
			})
			switch {
			case err == nil || errors.Is(err, errMarksSettled):
				a.marks, a.marksRead = ms, a.statMarks()
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
