package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log"
	"maps"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"glitched/internal/daemon"
	"glitched/internal/provider/fake"
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

		// P7: shown one way and another by turns, as a server comes and
		// goes, the marks settle in a round and then stay.
		// Nested sets always do; two that overlap do while all the names
		// the file holds fit, so that none need take another's mark.
		a := shown[:r.IntN(len(shown)+1)]
		b := slices.Clone(shown)
		if r.IntN(2) == 0 {
			b = append(b[len(a)/2:], randomNames(r.IntN(20))...)
			all := map[string]bool{}
			for _, m := range stored {
				all[m.Name] = true
			}
			for _, n := range slices.Concat(a, b) {
				all[n] = true
			}
			if fits := len(all) <= 8 || len(a) > 8 && len(b) > 8 && len(all) <= 56; !fits || hasFrozen {
				b = shown
			}
		}
		s := stored
		s, _ = settleMarks(s, a)
		s, _ = settleMarks(s, b)
		for round := range 3 {
			for _, set := range [][]string{a, b} {
				next, changed := settleMarks(s, set)
				if changed {
					var moved []string
					for i := range s {
						if s[i] != next[i] {
							moved = append(moved, fmt.Sprintf("%v -> %v", s[i], next[i]))
						}
					}
					t.Fatalf("%d: round %d, %d names and %d by turns, settling %d moved %q", iter, round+2, len(a), len(b), len(set), moved)
				}
			}
		}
	}
}

// marksDir is a state directory whose engine holds a thread in each of
// the folders, under /work, and the engine.
func marksDir(t *testing.T, folders ...string) (string, *daemon.Daemon) {
	t.Helper()
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(false)))
	for _, f := range folders {
		seed(t, d, "/work/"+f, "A thread in "+f)
	}
	return dir, d
}

// listed is each project's mark as projects.list gives it, once every
// one of names is listed.
func (h *harness) listed(names ...string) map[string]projectMark {
	h.t.Helper()
	got := map[string]projectMark{}
	h.until(fmt.Sprint("projects ", names), func() bool {
		clear(got)
		for _, r := range h.a.projectRecords() {
			got[r.Name] = projectMark{r.Name, r.Icon, r.Color}
		}
		for _, n := range names {
			if _, ok := got[n]; !ok {
				return false
			}
		}
		return true
	})
	return got
}

// rebuild has the sidebar sort its projects again, as a change does.
func (h *harness) rebuild() {
	h.a.sorted = sortedThreads{}
	h.a.sortThreads()
	h.tt.Frame()
}

func readMarks(t *testing.T, dir string) ([]projectMark, time.Time) {
	t.Helper()
	path := filepath.Join(dir, "marks.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var ms []projectMark
	if err := json.Unmarshal(b, &ms); err != nil {
		t.Fatalf("marks.json: %v: %s", err, b)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return ms, fi.ModTime()
}

func TestProjectsThatHashToOneIconAreListedOnThreeAndKeptSo(t *testing.T) {
	dir, _ := marksDir(t, "glitched", "mygo", "pstack-t3")
	h := start(t, dir)
	got := h.listed("glitched", "mygo", "pstack-t3")
	if got["glitched"] != (projectMark{"glitched", "wind", "#ec4899"}) {
		t.Errorf("glitched is %v", got["glitched"])
	}
	if a, b, c := got["glitched"].Icon, got["mygo"].Icon, got["pstack-t3"].Icon; a == b || b == c || a == c {
		t.Errorf("icons %s %s %s", a, b, c)
	}
	ms, mtime := readMarks(t, dir)
	want := []projectMark{got["glitched"], got["mygo"], got["pstack-t3"]}
	if !slices.Equal(ms, want) {
		t.Errorf("marks.json holds %v; want %v", ms, want)
	}

	// Every thread's tile, and quick ask's projects, draw the same.
	for _, p := range h.a.projects {
		for _, th := range p.threads {
			icon, color := h.a.projectOf(th).markNames()
			if m := got[p.name()]; icon != m.Icon || color != m.Color {
				t.Errorf("%q's tile is %s %s; listed %v", th.View.Title, icon, color, m)
			}
		}
	}
	quick := 0
	for _, p := range h.a.localProjects() {
		if m, ok := got[p.name()]; ok {
			quick++
			if icon, color := p.markNames(); icon != m.Icon || color != m.Color {
				t.Errorf("quick ask draws %s %s %s; listed %v", p.name(), icon, color, m)
			}
		}
	}
	if quick != 3 {
		t.Errorf("quick ask has %d of the projects", quick)
	}

	// Started again, the app draws them as it did, and writes nothing.
	again := start(t, dir)
	if g := again.listed("glitched", "mygo", "pstack-t3"); !maps.Equal(g, got) {
		t.Errorf("after a restart %v; before %v", g, got)
	}
	if again, m := readMarks(t, dir); !m.Equal(mtime) || !slices.Equal(again, ms) {
		t.Errorf("a restart wrote marks.json: %v to %v", ms, again)
	}
}

func TestANewProjectMovesOffTheIconsOfThoseSeenBefore(t *testing.T) {
	dir, d := marksDir(t, "glitched", "mygo", "pstack-t3")
	h := start(t, dir)
	before := h.listed("glitched", "mygo", "pstack-t3")
	seed(t, d, "/work/sparky", "A thread in sparky")
	got := h.listed("glitched", "mygo", "pstack-t3", "sparky")
	for n, m := range before {
		if got[n] != m {
			t.Errorf("%s moved from %v to %v", n, m, got[n])
		}
		if got["sparky"].Icon == m.Icon {
			t.Errorf("sparky has %s's icon: %v", n, got["sparky"])
		}
	}
	if ms, _ := readMarks(t, dir); len(ms) != 4 || ms[3].Name != "sparky" {
		t.Errorf("marks.json holds %v", ms)
	}
}

func TestRebuildingTheSidebarWritesNoMarks(t *testing.T) {
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(false)))
	seed(t, d, "/a/app", "One app")
	seed(t, d, "/b/app", "Another app")
	seed(t, d, "/work/mygo", "A thread in mygo")
	h := start(t, dir)
	h.listed("app", "mygo")
	h.until("both apps", func() bool { return len(h.a.projects) >= 3 })
	h.rebuild()
	ms, mtime := readMarks(t, dir)
	if len(ms) != 2 {
		t.Errorf("marks.json holds %v", ms)
	}
	for range 5 {
		h.rebuild()
	}
	if again, m := readMarks(t, dir); !m.Equal(mtime) || !slices.Equal(again, ms) {
		t.Errorf("rebuilds wrote marks.json: %v to %v", ms, again)
	}
}

func TestAMarkAnotherWindowWroteIsDrawn(t *testing.T) {
	dir, _ := marksDir(t, "glitched", "mygo", "pstack-t3")
	h := start(t, dir)
	h.listed("glitched", "mygo", "pstack-t3")
	ms, mtime := readMarks(t, dir)
	i := slices.IndexFunc(ms, func(m projectMark) bool { return m.Name == "glitched" })
	ms[i] = projectMark{"glitched", "folder", "#14b8a6"}
	b, _ := json.Marshal(ms)
	path := filepath.Join(dir, "marks.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(path, mtime.Add(time.Second), mtime.Add(time.Second))
	h.rebuild()
	if got := h.listed("glitched")["glitched"]; got != ms[i] {
		t.Errorf("glitched is %v; another window wrote %v", got, ms[i])
	}
}

func TestAnAppWithNoStateDirectoryWritesNoMarks(t *testing.T) {
	t.Chdir(t.TempDir())
	a := &app{}
	a.projects = []*project{{key: keyOf(nil, "/work/glitched")}, {key: keyOf(nil, "/work/mygo")}}
	a.settleShownMarks()
	if _, err := os.Stat("marks.json"); err == nil {
		t.Error("marks.json was written")
	}
	if len(a.marks) != 2 {
		t.Errorf("marks %v", a.marks)
	}
	if i, _ := a.projects[0].markNames(); i == func() string { i, _ := a.projects[1].markNames(); return i }() {
		t.Errorf("both drawn on %s", i)
	}
}

// syncBuffer is a log's output, which the app may write as a test reads.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestMarksThatDoNotParseAreMadeAgain(t *testing.T) {
	dir, _ := marksDir(t, "glitched", "mygo", "pstack-t3")
	if err := os.WriteFile(filepath.Join(dir, "marks.json"), []byte("\x00not json{"), 0o600); err != nil {
		t.Fatal(err)
	}
	var logged syncBuffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	h := start(t, dir)
	got := h.listed("glitched", "mygo", "pstack-t3")
	if a, b, c := got["glitched"].Icon, got["mygo"].Icon, got["pstack-t3"].Icon; a == b || b == c || a == c {
		t.Errorf("icons %s %s %s", a, b, c)
	}
	if !strings.Contains(logged.String(), "glitched: marks:") {
		t.Errorf("nothing logged: %q", logged.String())
	}
	if ms, _ := readMarks(t, dir); len(ms) != 3 {
		t.Errorf("marks.json holds %v", ms)
	}
}

func TestAThreadOutOfTheListDrawsItsProjectsMark(t *testing.T) {
	dir, _ := marksDir(t, "glitched", "mygo", "pstack-t3")
	h := start(t, dir)
	got := h.listed("glitched", "mygo", "pstack-t3")
	th := h.thread("A thread in mygo")
	h.a.projects = slices.DeleteFunc(h.a.projects, func(p *project) bool { return p.name() == "mygo" })
	if icon, color := h.a.projectOf(th).markNames(); icon != got["mygo"].Icon || color != got["mygo"].Color {
		t.Errorf("mygo out of the list draws %s %s; listed %v", icon, color, got["mygo"])
	}
}

func TestAMarkStaysPutAsAServersProjectsComeAndGo(t *testing.T) {
	// Six projects here and four on a server, then a seventh here, which
	// comes to share an icon with one of these while the server's show.
	local := []string{"glitched", "mygo", "openbot", "fiona-voice", "b", "site", "tool0"}
	all := append(slices.Clone(local), "r-api", "r-web", "r-ops", "r-db")
	s, _ := settleMarks(nil, slices.Concat(all[:6], all[7:]))
	s, _ = settleMarks(s, all)
	if m := markOf(t, s, "tool0"); m != (projectMark{"tool0", "magic-stick-3", "#0ea5e9"}) {
		t.Fatalf("tool0 is %v", m)
	}
	// Started again before the server answers, then with it, by turns.
	s, _ = settleMarks(s, local)
	s, _ = settleMarks(s, all)
	for range 3 {
		for _, shown := range [][]string{local, all} {
			if next, changed := settleMarks(s, shown); changed {
				t.Fatalf("%d shown changed %v to %v", len(shown), s, next)
			}
		}
	}
}

// marksApp is an app on dir with projects named names, as a rebuild
// leaves them.
func marksApp(dir string, names ...string) *app {
	a := &app{dir: dir}
	a.loadMarks()
	showMarks(a, names...)
	return a
}

func showMarks(a *app, names ...string) {
	a.projects = nil
	for _, n := range names {
		a.projects = append(a.projects, a.newProject(nil, keyOf(nil, "/work/"+n)))
	}
	a.settleShownMarks()
}

func marksDrawn(a *app) map[string]projectMark {
	by := map[string]projectMark{}
	for _, p := range a.projects {
		icon, color := p.markNames()
		by[p.name()] = projectMark{p.name(), icon, color}
	}
	return by
}

func TestAMarkAnotherWindowWroteAtTheSameTimeIsDrawn(t *testing.T) {
	dir := t.TempDir()
	a := marksApp(dir, "glitched", "mygo", "pstack-t3")
	path := filepath.Join(dir, "marks.json")
	ms, mtime := readMarks(t, dir)
	ms[0] = projectMark{ms[0].Name, "folder", "#14b8a6"}
	b, _ := json.Marshal(ms)
	if err := os.WriteFile(path, append(b, "\n\n"...), 0o600); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(path, mtime, mtime)
	showMarks(a, "glitched", "mygo", "pstack-t3")
	if got := marksDrawn(a)[ms[0].Name]; got != ms[0] {
		t.Errorf("%s draws %v; another window wrote %v", ms[0].Name, got, ms[0])
	}
}

func TestANewProjectIsMarkedOnTheNextRebuild(t *testing.T) {
	dir := t.TempDir()
	a := marksApp(dir, "glitched", "mygo")
	showMarks(a, "glitched", "mygo")
	showMarks(a, "glitched", "mygo", "pstack-t3")
	if ms, _ := readMarks(t, dir); len(ms) != 3 {
		t.Errorf("marks.json holds %v", ms)
	}
	by := marksDrawn(a)
	if by["pstack-t3"].Icon == by["glitched"].Icon || by["pstack-t3"].Icon == by["mygo"].Icon {
		t.Errorf("drawn %v", by)
	}
}

func TestMarksDeletedWhileTheAppRunsAreWrittenAgainAsTheyWere(t *testing.T) {
	dir := t.TempDir()
	a := marksApp(dir, "mygo")
	showMarks(a, "mygo", "pstack-t3")
	showMarks(a, "mygo", "pstack-t3", "glitched")
	before := marksDrawn(a)
	if err := os.Remove(filepath.Join(dir, "marks.json")); err != nil {
		t.Fatal(err)
	}
	showMarks(a, "mygo", "pstack-t3", "glitched", "sparky")
	after := marksDrawn(a)
	for n, m := range before {
		if after[n] != m {
			t.Errorf("%s moved from %v to %v", n, m, after[n])
		}
	}
	if ms, _ := readMarks(t, dir); len(ms) != 4 || ms[0].Name != "mygo" || ms[3].Name != "sparky" {
		t.Errorf("marks.json holds %v", ms)
	}
}

func TestMarksThatCantBeSavedHoldForTheSession(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "marks.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	var logged syncBuffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	a := marksApp(dir, "glitched", "mygo", "pstack-t3")
	lines := strings.Count(logged.String(), "\n")
	if lines == 0 || lines > 2 {
		t.Errorf("logged %q", logged.String())
	}
	for range 5 {
		showMarks(a, "glitched", "mygo", "pstack-t3")
	}
	if strings.Count(logged.String(), "\n") != lines {
		t.Errorf("rebuilds tried again: %q", logged.String())
	}
	by := marksDrawn(a)
	if by["glitched"].Icon == by["mygo"].Icon || by["mygo"].Icon == by["pstack-t3"].Icon || by["glitched"].Icon == by["pstack-t3"].Icon {
		t.Errorf("drawn %v", by)
	}
}
