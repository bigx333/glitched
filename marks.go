package main

import (
	"hash/fnv"
	"slices"
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
		icon, color int // the stored ones of a re-picker; -1 for a new name
		hashIcon    int
		hashColor   int
	}
	iconUses := make([]int, len(projectIcons))
	colorUses := make([]int, len(projectColors))
	pairs := map[[2]int]bool{}
	hold := func(icon, color int) {
		iconUses[icon]++
		colorUses[color]++
		pairs[[2]int{icon, color}] = true
	}

	// The names seen before keep their icons, the first seen of two that
	// share one keeping it, and the rest pick again. One this version can't
	// rewrite holds what it's drawn with, shared or not.
	var picks []pick
	seen := map[string]bool{}
	for i, m := range out {
		if !want[m.Name] || seen[m.Name] {
			continue
		}
		seen[m.Name] = true
		icon, color := drawn(m)
		if iconUses[icon] == 0 || frozen(m) {
			hold(icon, color)
			continue
		}
		hi, hc := hashMark(m.Name)
		picks = append(picks, pick{i, m.Name, icon, color, hi, hc})
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
		picks = append(picks, pick{-1, n, -1, -1, hi, hc})
	}

	hashes := make([]int, len(picks))
	for k, p := range picks {
		hashes[k] = p.hashIcon
	}
	hasPair := func(icon int) bool {
		for c := range projectColors {
			if !pairs[[2]int{icon, c}] {
				return true
			}
		}
		return false
	}
	for k, p := range picks {
		icon := pickIcon(p.hashIcon, p.icon, hashes[k+1:], iconUses, hasPair)
		start := p.hashColor
		if p.at >= 0 {
			start = p.color
		}
		color := pickColor(icon, start, colorUses, pairs)
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
	return out, changed
}

// pickIcon is the icon for a name that hashes to hash and isn't keeping
// its own: its hash icon while no one holds it; else the first free one
// from there that no later name hashes to, while that is free. Failing
// that, a re-picker keeps stored while it has a color free with it, and
// the rest take the least used of the icons no later name wants, so that
// a name alone on its hash icon always gets it.
func pickIcon(hash, stored int, later, uses []int, hasPair func(int) bool) int {
	if uses[hash] == 0 {
		return hash
	}
	wanted := map[int]bool{}
	for _, h := range later {
		if uses[h] == 0 {
			wanted[h] = true
		}
	}
	n := len(uses)
	for d := range n {
		if i := (hash + d) % n; uses[i] == 0 && !wanted[i] {
			return i
		}
	}
	if stored >= 0 && hasPair(stored) {
		return stored
	}
	least := hash // held, so not wanted
	for d := range n {
		if i := (hash + d) % n; !wanted[i] && uses[i] < uses[least] {
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
