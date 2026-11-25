package main

import (
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"slices"
	"testing"
)

// The hash picks below are written out, not computed by the code under test:
// glitched wind #ec4899, mygo wind #0ea5e9, pstack-t3 wind #0ea5e9,
// openbot phone #22c55e, sparky wind #eab308, fiona-voice code #ec4899,
// b magic-stick-3 #8b5cf6, aaa phone #0ea5e9.

func markOf(t *testing.T, marks []projectMark, name string) projectMark {
	t.Helper()
	for _, m := range marks {
		if m.Name == name {
			return m
		}
	}
	t.Fatalf("no mark for %q in %v", name, marks)
	return projectMark{}
}

func distinctIcons(t *testing.T, marks []projectMark, names []string) {
	t.Helper()
	seen := map[string]string{}
	for _, n := range names {
		m := markOf(t, marks, n)
		if o, ok := seen[m.Icon]; ok {
			t.Errorf("%s and %s share %s", o, n, m.Icon)
		}
		seen[m.Icon] = n
	}
}

func distinctPairs(t *testing.T, marks []projectMark, names []string) {
	t.Helper()
	seen := map[[2]string]string{}
	for _, n := range names {
		m := markOf(t, marks, n)
		k := [2]string{m.Icon, m.Color}
		if o, ok := seen[k]; ok {
			t.Errorf("%s and %s share %v", o, n, k)
		}
		seen[k] = n
	}
}

func steady(t *testing.T, marks []projectMark, shown []string) {
	t.Helper()
	again, changed := settleMarks(marks, shown)
	if changed || !slices.Equal(again, marks) {
		t.Errorf("settling again changed %v to %v", marks, again)
	}
}

func TestProjectsWithNoClashKeepTheirHashMarks(t *testing.T) {
	out, changed := settleMarks(nil, []string{"openbot", "fiona-voice", "b"})
	want := []projectMark{
		{"b", "magic-stick-3", "#8b5cf6"},
		{"fiona-voice", "code", "#ec4899"},
		{"openbot", "phone", "#22c55e"},
	}
	if !changed || !slices.Equal(out, want) {
		t.Errorf("got %v, %v; want %v, true", out, changed, want)
	}
	steady(t, out, []string{"b", "fiona-voice", "openbot"})
}

func TestProjectsThatHashToOneIconGetThreeIcons(t *testing.T) {
	shown := []string{"glitched", "mygo", "pstack-t3"}
	out, changed := settleMarks(nil, shown)
	if !changed {
		t.Error("not changed")
	}
	if m := markOf(t, out, "glitched"); m != (projectMark{"glitched", "wind", "#ec4899"}) {
		t.Errorf("glitched is %v", m)
	}
	distinctIcons(t, out, shown)
	for _, n := range []string{"mygo", "pstack-t3"} {
		if m := markOf(t, out, n); m.Icon == "wind" || m.Color != "#0ea5e9" {
			t.Errorf("%s is %v; want another icon, #0ea5e9", n, m)
		}
	}
	steady(t, out, shown)
}

func TestAMovedProjectTakesNoIconAnotherHashesTo(t *testing.T) {
	out, _ := settleMarks(nil, []string{"glitched", "mygo", "openbot"})
	if m := markOf(t, out, "openbot"); m != (projectMark{"openbot", "phone", "#22c55e"}) {
		t.Errorf("openbot is %v; want phone #22c55e", m)
	}
	distinctIcons(t, out, []string{"glitched", "mygo", "openbot"})
}

func TestTheFirstSeenProjectKeepsItsIcon(t *testing.T) {
	stored := []projectMark{{"openbot", "phone", "#22c55e"}}
	for _, shown := range [][]string{{"openbot", "aaa"}, {"aaa", "openbot"}} {
		out, changed := settleMarks(stored, shown)
		if !changed || len(out) != 2 || out[0] != stored[0] {
			t.Errorf("%v: got %v, %v", shown, out, changed)
			continue
		}
		if m := markOf(t, out, "aaa"); m.Icon == "phone" {
			t.Errorf("%v: aaa took openbot's phone: %v", shown, m)
		}
	}
}

func TestAReturningProjectTakesItsIconBack(t *testing.T) {
	// openbot was seen first, then left; aaa came and took its phone.
	stored := []projectMark{
		{"openbot", "phone", "#22c55e"},
		{"aaa", "phone", "#0ea5e9"},
		{"glitched", "wind", "#ec4899"},
	}
	shown := []string{"glitched", "aaa", "openbot"}
	out, changed := settleMarks(stored, shown)
	if !changed || len(out) != 3 {
		t.Fatalf("got %v, %v", out, changed)
	}
	if out[0] != stored[0] || out[2] != stored[2] {
		t.Errorf("openbot or glitched moved: %v", out)
	}
	if out[1].Name != "aaa" || out[1].Icon == "phone" || out[1].Icon == "wind" {
		t.Errorf("aaa is %v; want it moved off phone in place", out[1])
	}
	if stored[1].Icon != "phone" {
		t.Error("stored was changed")
	}
	steady(t, out, shown)
}

func TestAProjectNotShownHoldsNoIcon(t *testing.T) {
	stored := []projectMark{{"openbot", "phone", "#22c55e"}}
	out, changed := settleMarks(stored, []string{"aaa"})
	want := []projectMark{{"openbot", "phone", "#22c55e"}, {"aaa", "phone", "#0ea5e9"}}
	if !changed || !slices.Equal(out, want) {
		t.Errorf("got %v, %v; want %v", out, changed, want)
	}
}

func names(n int) []string {
	var s []string
	for i := range n {
		s = append(s, fmt.Sprintf("p%d", i))
	}
	return s
}

func TestManyProjectsShareAsLittleAsTheyCan(t *testing.T) {
	eight := names(8)
	out, _ := settleMarks(nil, eight)
	distinctIcons(t, out, eight)
	steady(t, out, eight)

	many := names(56)
	out, _ = settleMarks(nil, many)
	distinctPairs(t, out, many)
	steady(t, out, many)

	// Grown one at a time, too, as projects are added.
	var grown []projectMark
	for i := 1; i <= 56; i++ {
		grown, _ = settleMarks(grown, many[:i])
	}
	distinctPairs(t, grown, many)
	steady(t, grown, many)

	lots := names(200)
	a, _ := settleMarks(nil, lots)
	b, _ := settleMarks(nil, lots)
	if len(a) != 200 || !slices.Equal(a, b) {
		t.Errorf("200 projects: %d marks, deterministic %v", len(a), slices.Equal(a, b))
	}
	steady(t, a, lots)
}

func TestTheOrderProjectsAreShownInChangesNoMark(t *testing.T) {
	stored := []projectMark{{"aaa", "phone", "#0ea5e9"}, {"sparky", "wind", "#eab308"}}
	shown := append([]string{"glitched", "mygo", "pstack-t3", "openbot", "aaa", "sparky", "b"}, names(12)...)
	want, _ := settleMarks(stored, shown)
	for i := range 10 {
		p := slices.Clone(shown)
		for j := range p {
			k := (j*7 + i*3) % len(p)
			p[j], p[k] = p[k], p[j]
		}
		slices.Reverse(p[:i])
		if got, _ := settleMarks(stored, p); !slices.Equal(got, want) {
			t.Errorf("%v gave %v; want %v", p, got, want)
		}
	}
}

func TestAProjectShownTwiceGetsOneMark(t *testing.T) {
	shown := []string{"b", "openbot", "b"}
	out, _ := settleMarks(nil, shown)
	if len(out) != 2 {
		t.Errorf("got %v", out)
	}
	steady(t, out, shown)
}

func TestAMarkThisVersionDoesNotKnowIsKept(t *testing.T) {
	stored := []projectMark{
		{"glitched", "rocket", "#123456"},
		{"openbot", "phone", "#000000"},
	}
	out, _ := settleMarks(stored, []string{"glitched", "openbot", "mygo"})
	if out[0] != stored[0] || out[1] != stored[1] {
		t.Errorf("unknown marks were rewritten: %v", out)
	}
	if m := markOf(t, out, "mygo"); m.Icon == "wind" || m.Icon == "phone" {
		t.Errorf("mygo took a drawn icon: %v", m)
	}
	by := marksByName(out)
	if m := by["glitched"]; m.Icon != "wind" || m.Color != "#ec4899" {
		t.Errorf("glitched draws %v", m)
	}
	if m := by["openbot"]; m.Icon != "phone" || m.Color != "#22c55e" {
		t.Errorf("openbot draws %v", m)
	}

	// One that clashes, as drawn, with one seen before it stays as it is.
	stored = []projectMark{{"aaa", "phone", "#0ea5e9"}, {"openbot", "rocket", "#22c55e"}}
	out, changed := settleMarks(stored, []string{"aaa", "openbot"})
	if changed || !slices.Equal(out, stored) {
		t.Errorf("got %v, %v", out, changed)
	}
}

func TestNoProjectsSettleToNothing(t *testing.T) {
	if out, changed := settleMarks(nil, nil); changed || len(out) != 0 {
		t.Errorf("got %v, %v", out, changed)
	}
	stored := []projectMark{{"openbot", "phone", "#22c55e"}}
	if out, changed := settleMarks(stored, []string{}); changed || !slices.Equal(out, stored) {
		t.Errorf("got %v, %v", out, changed)
	}
	if len(marksByName(nil)) != 0 {
		t.Error("marks from nothing")
	}
}

func TestAProjectsHashMarkIsAsItWas(t *testing.T) {
	for _, c := range []struct{ ws, icon, color string }{
		{"/src/glitched", "wind", "#ec4899"},
		{"/src/openbot", "phone", "#22c55e"},
		{"/src/fiona-voice", "code", "#ec4899"},
		{"/src/b", "magic-stick-3", "#8b5cf6"},
	} {
		p := &project{key: keyOf(nil, c.ws)}
		if icon, color := p.markNames(); icon != c.icon || color != c.color {
			t.Errorf("%s: %s %s; want %s %s", c.ws, icon, color, c.icon, c.color)
		}
	}
	p := &project{noProject: true}
	if icon, color := p.markNames(); icon != "chat-round-line" || color != "#8e8e93" {
		t.Errorf("no project: %s %s", icon, color)
	}
}

func TestAProjectAloneOnItsHashIconKeepsItsMarkPastEight(t *testing.T) {
	// glitched, mygo and pstack-t3 hash to wind, site and bot to fast-tier;
	// the rest are alone on theirs.
	shown := []string{"glitched", "mygo", "pstack-t3", "openbot", "fiona-voice", "b", "site", "tools", "bot"}
	out, _ := settleMarks(nil, shown)
	for _, want := range []projectMark{
		{"tools", "global", "#8b5cf6"},
		{"openbot", "phone", "#22c55e"},
		{"fiona-voice", "code", "#ec4899"},
		{"b", "magic-stick-3", "#8b5cf6"},
		{"glitched", "wind", "#ec4899"},
	} {
		if m := markOf(t, out, want.Name); m != want {
			t.Errorf("%s is %v; want %v", want.Name, m, want)
		}
	}
	distinctPairs(t, out, shown)
	steady(t, out, shown)
}

func TestAMarkThisVersionDoesNotKnowHoldsTheIconItIsDrawnWith(t *testing.T) {
	// openbot is drawn on its hash phone, which aaa, seen first, has too.
	// q1 folder, q3 magic-stick-3, q4 terminal, q5 code, q6 fast-tier and
	// q7 wind are alone on their hash icons; sparky hashes to wind.
	stored := []projectMark{{"aaa", "phone", "#0ea5e9"}, {"openbot", "rocket", "#22c55e"}}
	eight := []string{"aaa", "openbot", "q1", "q3", "q4", "q5", "q6", "q7"}
	for _, shown := range [][]string{eight, append(slices.Clone(eight), "sparky")} {
		out, _ := settleMarks(stored, shown)
		if out[0] != stored[0] || out[1] != stored[1] {
			t.Errorf("%v: stored marks moved: %v", shown, out)
		}
		by := marksByName(out)
		icons := map[string]string{}
		for _, n := range shown {
			icon := by[n].Icon
			if o, ok := icons[icon]; ok && n != "openbot" && o != "openbot" {
				t.Errorf("%v: %s and %s are both drawn on %s", shown, o, n, icon)
			}
			icons[icon] = n
		}
		if want := min(len(shown)-1, 8); len(icons) != want {
			t.Errorf("%v: %d icons drawn; want %d", shown, len(icons), want)
		}
		steady(t, out, shown)
	}
}

// hashOf is name's hash pick, worked out here and not by hashMark.
func hashOf(name string) projectMark {
	h := fnv.New32a()
	h.Write([]byte(name))
	n := h.Sum32()
	return projectMark{name, projectIcons[n%8], projectColors[n/7%7]}
}

func TestUnknownMarksTakeUpOnlyTheIconsTheyAreDrawnWith(t *testing.T) {
	// aaa and c hash to phone. The seven this version can't redraw are
	// drawn on their hash icons: a, i and q global, d and l code, k and s
	// phone. Nine are shown, but only five icons are taken up.
	stored := []projectMark{{"aaa", "phone", "#0ea5e9"}, {"c", "phone", "#14b8a6"}}
	shown := []string{"aaa", "c"}
	for _, n := range []string{"a", "i", "q", "d", "l", "k", "s"} {
		stored = append(stored, projectMark{n, "rocket", "#eab308"})
		shown = append(shown, n)
	}
	out, changed := settleMarks(stored, shown)
	if !changed || !slices.Equal(out[2:], stored[2:]) || out[0] != stored[0] {
		t.Fatalf("got %v, %v", out, changed)
	}
	if c := out[1]; slices.Contains([]string{"phone", "global", "code"}, c.Icon) {
		t.Errorf("c is %v; want an icon of its own", c)
	}
	steady(t, out, shown)
}

func TestUnknownMarksDrawnWithOnePairTakeUpThatPairOnce(t *testing.T) {
	// Four this version can't redraw, all drawn magic-stick-3 #ec4899,
	// and 55 more: 56 pairs for 56 drawn, so none may share.
	var stored []projectMark
	var shown []string
	for i := 0; len(stored) < 4; i++ {
		if n := fmt.Sprintf("f%d", i); hashOf(n).Icon == "magic-stick-3" {
			stored = append(stored, projectMark{n, "rocket", "#ec4899"})
			shown = append(shown, n)
		}
	}
	var rest []string
	for i := range 55 {
		rest = append(rest, fmt.Sprintf("a%03d", i))
	}
	out, _ := settleMarks(stored, append(slices.Clone(shown), rest...))
	distinctPairs(t, append(out[4:], projectMark{"f", "magic-stick-3", "#ec4899"}), append(slices.Clone(rest), "f"))
	steady(t, out, append(slices.Clone(shown), rest...))
}

// drawnHere is the mark m is drawn with, worked out here and not by
// marksByName: its own icon and color where this version knows them.
func drawnHere(m projectMark) (d projectMark, frozen bool) {
	d = hashOf(m.Name)
	if slices.Contains(projectIcons, m.Icon) {
		d.Icon = m.Icon
	} else {
		frozen = true
	}
	if slices.Contains(projectColors, m.Color) {
		d.Color = m.Color
	} else {
		frozen = true
	}
	return d, frozen
}

func TestFiftyOneProjectsShareNoPair(t *testing.T) {
	// Fifty names that don't hash to terminal, then one that does.
	var shown []string
	for i := 0; len(shown) < 50; i++ {
		if n := fmt.Sprintf("a%03d", i); hashOf(n).Icon != "terminal" {
			shown = append(shown, n)
		}
	}
	z := "z005"
	if hashOf(z).Icon != "terminal" {
		t.Fatalf("%s hashes to %s", z, hashOf(z).Icon)
	}
	shown = append(shown, z)
	out, _ := settleMarks(nil, shown)
	distinctPairs(t, out, shown)
	if m := markOf(t, out, z); m != hashOf(z) {
		t.Errorf("%s is %v; want %v", z, m, hashOf(z))
	}
	steady(t, out, shown)
}

func TestMarksSettleForAnySetOfProjects(t *testing.T) {
	r := rand.New(rand.NewPCG(281, 2026))
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_. "
	var pool []string
	for len(pool) < 400 {
		b := make([]byte, 1+r.IntN(14))
		for i := range b {
			b[i] = chars[r.IntN(len(chars))]
		}
		if n := string(b); !slices.Contains(pool, n) {
			pool = append(pool, n)
		}
	}
	randomNames := func(n int) []string {
		p := slices.Clone(pool)
		r.Shuffle(len(p), func(i, j int) { p[i], p[j] = p[j], p[i] })
		return p[:n]
	}
	sizes := []int{11, 61, 221}
	for iter := range 2000 {
		shown := randomNames(r.IntN(sizes[iter%3]))
		var stored []projectMark
		if r.IntN(3) > 0 {
			stored, _ = settleMarks(nil, randomNames(r.IntN(len(shown)+2)))
			for range r.IntN(4) {
				stored = append(stored, projectMark{pool[r.IntN(len(pool))], projectIcons[r.IntN(8)], projectColors[r.IntN(7)]})
			}
			for i := range stored {
				switch r.IntN(15) {
				case 0:
					stored[i].Icon = "rocket"
				case 1:
					stored[i].Color = "#123456"
				}
			}
		}
		if r.IntN(4) == 0 {
			// Some this version can't redraw, drawn with one pair.
			icon, color := projectIcons[r.IntN(8)], projectColors[r.IntN(7)]
			k := 2 + r.IntN(6)
			for _, n := range pool {
				if k > 0 && hashOf(n).Icon == icon && !slices.ContainsFunc(stored, func(m projectMark) bool { return m.Name == n }) {
					stored = append(stored, projectMark{n, "rocket", color})
					if !slices.Contains(shown, n) {
						shown = append(shown, n)
					}
					k--
				}
			}
		}
		before := slices.Clone(stored)
		out, _ := settleMarks(stored, shown)
		if !slices.Equal(stored, before) {
			t.Fatalf("%d: stored was changed", iter)
		}
		hasFrozen := false
		for _, m := range stored {
			if _, f := drawnHere(m); f && slices.Contains(shown, m.Name) {
				hasFrozen = true
			}
		}
		drawn := map[string]projectMark{}
		for _, m := range out {
			if _, ok := drawn[m.Name]; !ok {
				drawn[m.Name], _ = drawnHere(m)
			}
		}

		// P1 and P2: no two share an icon while icons go round, nor a pair
		// while pairs do. Those with marks this version doesn't know take up
		// what they're drawn with, and the rest share none among them.
		var thawed []projectMark
		frozenIcons, frozenPairs := map[string]bool{}, map[[2]string]bool{}
		for _, n := range shown {
			i := slices.IndexFunc(out, func(m projectMark) bool { return m.Name == n })
			if i < 0 {
				t.Fatalf("%d: no mark for %q", iter, n)
			}
			d, f := drawnHere(out[i])
			if !f {
				thawed = append(thawed, d)
				continue
			}
			frozenIcons[d.Icon] = true
			frozenPairs[[2]string{d.Icon, d.Color}] = true
		}
		icons, pairs := map[string]bool{}, map[[2]string]bool{}
		for _, d := range thawed {
			icons[d.Icon] = true
			pairs[[2]string{d.Icon, d.Color}] = true
		}
		if len(thawed)+len(frozenIcons) <= 8 && len(icons) != len(thawed) {
			t.Fatalf("%d: %d names drawn on %d icons", iter, len(thawed), len(icons))
		}
		if len(thawed)+len(frozenPairs) <= 56 && len(pairs) != len(thawed) {
			t.Fatalf("%d: %d names drawn with %d pairs", iter, len(thawed), len(pairs))
		}
		if !hasFrozen && len(shown) <= 8 && len(icons) != len(shown) {
			t.Fatalf("%d: %d names drawn on %d icons", iter, len(shown), len(icons))
		}
		// P3: settling its own output changes nothing.
		if again, changed := settleMarks(out, shown); changed || !slices.Equal(again, out) {
			t.Fatalf("%d: settling %d names again changed them", iter, len(shown))
		}

		// P4: new names move none of those seen before.
		more := slices.Clone(shown)
		for _, n := range randomNames(r.IntN(12)) {
			if !slices.ContainsFunc(out, func(m projectMark) bool { return m.Name == n }) {
				more = append(more, n)
			}
		}
		if grown, _ := settleMarks(out, more); !slices.Equal(grown[:len(out)], out) {
			t.Fatalf("%d: adding %d names to %d moved some", iter, len(more)-len(shown), len(shown))
		}

		// P5: on upgrade, a name alone on its hash icon keeps its hash mark.
		if len(stored) == 0 && len(shown) <= 56 {
			hashed := map[string]int{}
			for _, n := range shown {
				hashed[hashOf(n).Icon]++
			}
			for _, n := range shown {
				if want := hashOf(n); hashed[want.Icon] == 1 && drawn[n] != want {
					t.Fatalf("%d: %q alone on its hash icon is %v; want %v", iter, n, drawn[n], want)
				}
			}
		}

		// P6: the same, whatever the order of shown.
		p := slices.Clone(shown)
		r.Shuffle(len(p), func(i, j int) { p[i], p[j] = p[j], p[i] })
		if other, _ := settleMarks(stored, p); !slices.Equal(other, out) {
			t.Fatalf("%d: the order of %d names changed the marks", iter, len(shown))
		}
	}
}
