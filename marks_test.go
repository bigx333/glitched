package main

import (
	"fmt"
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
