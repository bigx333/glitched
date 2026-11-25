package main

import (
	"hash/fnv"
	"slices"
	"strings"
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
		at       int // the entry in out; -1 for a new name
		name     string
		color    int // the one to start from: stored, or the hash one
		hashIcon int
		claim    bool
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
	clash := func(icon, color int) bool {
		switch {
		case room+len(frozenIcons) <= len(projectIcons):
			return iconUses[icon] > 0
		case room+len(frozenPairs) <= len(projectIcons)*len(projectColors):
			return pairs[[2]int{icon, color}]
		}
		return false
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
		picks = append(picks, pick{at: i, name: m.Name, color: color, hashIcon: hi})
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
		picks = append(picks, pick{at: -1, name: n, color: hc, hashIcon: hi})
	}

	// The first of those to hash to an icon no one holds takes it, before
	// any that must move does, so that a name alone on its hash icon gets it.
	claimed := map[int]bool{}
	for k, p := range picks {
		if iconUses[p.hashIcon] == 0 && !claimed[p.hashIcon] {
			claimed[p.hashIcon] = true
			picks[k].claim = true
		}
	}
	settle := func(p pick, icon int) {
		color := pickColor(icon, p.color, colorUses, pairs)
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
	for _, p := range picks {
		if p.claim {
			settle(p, p.hashIcon)
		}
	}
	for _, p := range picks {
		if !p.claim {
			settle(p, pickIcon(p.hashIcon, iconUses))
		}
	}
	// New names were appended claims first; put them in name order.
	slices.SortStableFunc(out[len(stored):], func(a, b projectMark) int {
		return strings.Compare(a.Name, b.Name)
	})
	return out, changed
}

// pickIcon is the icon for a name that must move off hash: the first free
// one from there; else the least used, the first from hash of those used
// as little. While pairs go round, that one has a color free with it.
func pickIcon(hash int, uses []int) int {
	n := len(uses)
	least := hash
	for d := range n {
		i := (hash + d) % n
		if uses[i] == 0 {
			return i
		}
		if uses[i] < uses[least] {
			least = i
		}
	}
	return least
}

// pickColor is the color to draw icon with, from start: start while no
// one has the pair; else the least used color whose pair with icon is
// free, the first from start of those used as little; else the least used.
func pickColor(icon, start int, uses []int, pairs map[[2]int]bool) int {
	if !pairs[[2]int{icon, start}] {
		return start
	}
	n := len(uses)
	best, bestFree := -1, false
	for d := range n {
		c := (start + d) % n
		free := !pairs[[2]int{icon, c}]
		switch {
		case best < 0, free && !bestFree:
			best, bestFree = c, free
		case free == bestFree && uses[c] < uses[best]:
			best = c
		}
	}
	return best
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
