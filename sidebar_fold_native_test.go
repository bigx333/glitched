package main

import (
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
)

// nativeScene is scene "sidebar", Snoozed and Settled open with open,
// with many threads more settled, the built-in sidebar turned off: its
// bottom part is Glitched's own.
func nativeScene(t *testing.T, open bool, many int) *harness {
	t.Helper()
	o := ""
	if open {
		o = "1"
	}
	t.Setenv("OPEN", o)
	t.Setenv("MANY", strconv.Itoa(many))
	t.Setenv("MANYIN", "")
	t.Setenv("SIDEBARDOWN", "")
	h := sceneHarness(t, "sidebar")
	revealNative(t, h)
	for range 3 {
		h.tt.Frame()
		h.tt.Advance(time.Second)
	}
	nativeDraws(t, h)
	return h
}

// nativeDraws fails t unless Glitched's own drew the sidebar's bottom
// part in the last frame, not a plugin's list.
func nativeDraws(t *testing.T, h *harness) {
	t.Helper()
	d := h.a.partsDrawn["sidebar.body"]
	if d == nil || len(d.copies) != 2 || d.copies[1].plugin != "" || !d.copies[1].glitched {
		var by []copyDrawn
		if d != nil {
			by = d.copies
		}
		t.Fatalf("the sidebar's bottom part isn't Glitched's own: copies %+v", by)
	}
	if _, ok := h.tt.AccessByRole(ui.RoleList, "Working, Snoozed and Settled"); ok {
		t.Fatal("a plugin's list of Working, Snoozed and Settled shows")
	}
}

// rowBox is the whole box of the sidebar's row of the thread titled
// title, as laid out, whether the curtain shows any of it or not.
func rowBox(h *harness, title string) (ui.Rect, bool) {
	return rowBoxUnder(h, title, float32(math.Inf(-1)))
}

// rowBoxUnder is rowBox of the row below top, as a row leaving the
// section over it may still show there.
func rowBoxUnder(h *harness, title string, top float32) (ui.Rect, bool) {
	for _, x := range h.tt.AccessAll() {
		if x.Role != ui.RoleButton || x.Bounds.X >= h.a.sidebarWidth || x.Bounds.Y <= top {
			continue
		}
		if x.Name == title || strings.HasPrefix(x.Name, title+" ") {
			return x.Bounds, true
		}
	}
	return ui.Rect{}, false
}

// shownOf is what shows of text in the sidebar, after layout and every
// clip: zero where nothing does.
func shownOf(h *harness, text string) ui.Rect {
	r, ok := h.tt.Find(text)
	if !ok || r.X >= h.a.sidebarWidth || r.H <= 0 {
		return ui.Rect{}
	}
	return r
}

// nativeHeads is what stands between the tops of a section's header and
// the next one's, beside its rows: the header, the gap under it and the
// gap between sections.
const nativeHeads = 30 + 2 + 6

// nativeShot is what a frame of Glitched's own sidebar shows of a section
// folding: its progress, the tops of its header's title and the next
// one's, its first row's whole box and what shows of its title.
type nativeShot struct {
	p          float32
	head, next float32
	row, title ui.Rect
	hasRow     bool
	axis       float64 // the header's chevron's (chevronAxis), NaN where none
}

// shows is how much of the section's rows the curtain uncovers, as the
// next header sits under them.
func (s nativeShot) shows() float32 { return s.next - s.head - nativeHeads }

func (s nativeShot) String() string {
	return fmt.Sprintf("p=%.3f head=%.2f next=%.2f shows=%.2f row=%v title=%v", s.p, s.head, s.next, s.shows(), s.row, s.title)
}

type nativeWatch struct {
	t                 *testing.T
	h                 *harness
	cat               category
	header, next, row string
	shots             []nativeShot
}

func (w *nativeWatch) shot() nativeShot {
	s := nativeShot{p: w.h.a.sectionP(w.cat), head: yOf(w.h, w.header), title: shownOf(w.h, w.row)}
	if w.next != "" {
		s.next = yOf(w.h, w.next)
	}
	s.row, s.hasRow = rowBox(w.h, w.row)
	s.axis = math.NaN()
	if axis, ok := chevronAxis(w.h, w.header); ok {
		s.axis = axis
	}
	return s
}

// checkChevron fails t where the header's chevron, in the frames w has,
// isn't turned 90°·p, or turns back from the way the section goes: shut
// and open are its axis at rest shut and open.
func (w *nativeWatch) checkChevron(shut, open float64) {
	w.t.Helper()
	turn := chevronTurnOf(w.t, shut, open)
	var ps []float32
	var turns []float64
	for i, s := range w.shots {
		checkTurn(w.t, i, foldShot{p: s.p, turn: turn.at(s.axis)})
		ps, turns = append(ps, s.p), append(turns, turn.at(s.axis))
	}
	checkTurns(w.t, ps, turns)
}

// click clicks the header: the frame the fold starts in.
func (w *nativeWatch) click() {
	w.t.Helper()
	if err := w.h.tt.Click(w.header); err != nil {
		w.t.Fatal(err)
	}
	w.shots = append(w.shots, w.shot())
}

func (w *nativeWatch) frames(n int) {
	for range n {
		w.h.tt.Advance(foldFrame)
		w.shots = append(w.shots, w.shot())
	}
}

func (w *nativeWatch) log() {
	for i, s := range w.shots {
		w.t.Logf("frame %2d (%5.1f ms): %v", i, float64(i)*1000/60, s)
	}
}

// curtainTitle is how much of a title shows that sits off below the top
// of its row, tall, the row's top at rowY, with the curtain's bottom at
// bottom.
func curtainTitle(bottom, rowY, off, tall float32) float32 {
	return min(max(bottom-(rowY+off), 0), tall)
}

// Glitched's own Snoozed opens as a curtain, over the chevron's 200 ms:
// the bottom box grows up, so its header rises frame by frame, as much
// as its row shows of itself, from the top, the row sitting still in it;
// Settled, under it, stays. Then it is as a section left open is, and as
// Glitched's own sidebar was before sections folded.
func TestSidebarFoldNativeOpensSnoozedAsACurtain(t *testing.T) {
	h := nativeScene(t, false, 0)
	w := &nativeWatch{t: t, h: h, cat: catSnoozed, header: "Snoozed", next: "Settled", row: "Snoozed until tomorrow"}
	before := w.shot()
	if before.p != 0 || !closeTo(before.shows(), 0, 0.01) || before.title.H != 0 {
		t.Fatalf("Snoozed shut: %v", before)
	}
	w.click()
	w.frames(16)
	h.tt.Advance(time.Second)
	nativeDraws(t, h)
	rest := w.shot()
	off, tall := rest.title.Y-rest.row.Y, rest.title.H
	if !rest.hasRow || tall < 10 {
		t.Fatalf("open, Snoozed shows no row: %v", rest)
	}
	w.checkChevron(before.axis, rest.axis)
	end := -1
	for i, s := range w.shots {
		if end < 0 && s.p == 1 {
			end = i
		}
		if !closeTo(s.next, before.next, 0.01) {
			t.Errorf("frame %d: Settled moved, %.2f → %.2f", i, before.next, s.next)
		}
		if want := s.p * rowHeight; !closeTo(s.shows(), want, 0.75) {
			t.Errorf("frame %d: the curtain uncovers %.2f at p %.3f, want %.2f", i, s.shows(), s.p, want)
		}
		if !closeTo(before.head-s.head, s.shows(), 0.75) {
			t.Errorf("frame %d: the header rose %.2f, the row shows %.2f: out of step", i, before.head-s.head, s.shows())
		}
		if s.p > 0 {
			if !s.hasRow || s.row.H != rowHeight || !closeTo(s.row.Y, s.head+(rest.row.Y-rest.head), 0.75) {
				t.Errorf("frame %d: the row isn't whole, under its header: %v", i, s)
			}
			want := curtainTitle(s.row.Y+s.shows(), s.row.Y, off, tall)
			if !closeTo(s.title.H, want, 0.75) || want > 0 && !closeTo(s.title.Y, s.row.Y+off, 0.75) {
				t.Errorf("frame %d: the title shows %v, the curtain uncovered %.2f of it at %.2f", i, s.title, want, s.row.Y+off)
			}
		}
		if i == 0 {
			continue
		}
		prev := w.shots[i-1]
		switch {
		case s.p > prev.p && s.shows() > 0.75:
			if !(s.head < prev.head) || !(s.shows() > prev.shows()) {
				t.Errorf("frame %d: %v after %v; want the header rising, the row showing more", i, s, prev)
			}
		case s.p < prev.p:
			t.Errorf("frame %d: it went back, %v after %v", i, s, prev)
		}
	}
	if end < 0 || end > 14 {
		t.Fatalf("not open by %d ms: %v", 14*1000/60, w.shots[len(w.shots)-1])
	}
	if mid := w.shots[3]; !(mid.p > 0 && mid.p < 1) || !(mid.shows() > 1 && mid.shows() < rowHeight-1) {
		t.Errorf("at 50 ms: %v, want it half way", mid)
	}
	for i := end; i < len(w.shots); i++ {
		if s := w.shots[i]; s.head != rest.head || s.next != rest.next || s.row != rest.row || s.title != rest.title {
			t.Errorf("frame %d, open: %v; at rest %v: it jumps at the end", i, s, rest)
		}
	}
	// As Glitched's own drew it opened before sections folded.
	if !closeTo(rest.head, 710.396, 0.01) || !closeTo(rest.next, 780.396, 0.01) || !closeTo(rest.title.Y, 743.0635, 0.01) {
		t.Errorf("open at rest: Snoozed %.4f, Settled %.4f, its row's title %.4f; before, 710.396, 780.396 and 743.0635", rest.head, rest.next, rest.title.Y)
	}
	// As one left open.
	fresh := nativeScene(t, false, 0)
	fresh.a.sectionClosed[catSnoozed] = false
	fresh.tt.Frame()
	fresh.tt.Advance(time.Second)
	fw := &nativeWatch{t: t, h: fresh, cat: catSnoozed, header: "Snoozed", next: "Settled", row: "Snoozed until tomorrow"}
	// The pointer, where the click left it, is over the row opened, whose
	// title gives way to its actions.
	if f := fw.shot(); f.head != rest.head || f.next != rest.next || f.row != rest.row || f.title.Y != rest.title.Y || f.title.H != rest.title.H {
		t.Errorf("opened, it shows %v; left open, %v", rest, f)
	}
	if t.Failed() || testing.Verbose() {
		w.log()
	}
}

// Glitched's own Snoozed shuts as the curtain goes back down: the header
// falls, the row shows less of itself and is there till nothing of it
// shows, and then it is as Glitched's own was shut before.
func TestSidebarFoldNativeShutsSnoozedAsACurtain(t *testing.T) {
	h := nativeScene(t, true, 0)
	w := &nativeWatch{t: t, h: h, cat: catSnoozed, header: "Snoozed", next: "Settled", row: "Snoozed until tomorrow"}
	before := w.shot()
	if before.p != 1 || !closeTo(before.shows(), rowHeight, 0.01) {
		t.Fatalf("Snoozed open: %v", before)
	}
	off, tall := before.title.Y-before.row.Y, before.title.H
	w.click()
	w.frames(16)
	h.tt.Advance(time.Second)
	nativeDraws(t, h)
	rest := w.shot()
	w.checkChevron(rest.axis, before.axis)
	shut := -1
	for i, s := range w.shots {
		if shut < 0 && s.p == 0 {
			shut = i
		}
		if !closeTo(s.next, before.next, 0.01) {
			t.Errorf("frame %d: Settled moved, %.2f → %.2f", i, before.next, s.next)
		}
		if want := s.p * rowHeight; !closeTo(s.shows(), want, 0.75) {
			t.Errorf("frame %d: the curtain uncovers %.2f at p %.3f, want %.2f", i, s.shows(), s.p, want)
		}
		if s.p > 0 {
			if !s.hasRow || s.row.H != rowHeight {
				t.Errorf("frame %d: at p %.3f the row is gone or squashed: %v", i, s.p, s)
			}
			want := curtainTitle(s.row.Y+s.shows(), s.row.Y, off, tall)
			if !closeTo(s.title.H, want, 0.75) || want > 0 && !closeTo(s.title.Y, s.row.Y+off, 0.75) {
				t.Errorf("frame %d: the title shows %v, the curtain uncovered %.2f of it", i, s.title, want)
			}
		} else if s.hasRow || h.tt.HasText("Snoozed until tomorrow") && s.title.H > 0 {
			t.Errorf("frame %d: shut, the row still shows: %v", i, s)
		}
		if i == 0 {
			continue
		}
		prev := w.shots[i-1]
		switch {
		case s.p < prev.p && prev.shows() > 0.75:
			if !(s.head > prev.head) || !(s.shows() < prev.shows()) {
				t.Errorf("frame %d: %v after %v; want the header falling, the row showing less", i, s, prev)
			}
		case s.p > prev.p:
			t.Errorf("frame %d: it went back, %v after %v", i, s, prev)
		}
	}
	if shut < 0 || shut > 14 {
		t.Fatalf("not shut by %d ms: %v", 14*1000/60, w.shots[len(w.shots)-1])
	}
	if mid := w.shots[3]; !(mid.p > 0 && mid.p < 1) || !(mid.shows() > 1 && mid.shows() < rowHeight-1) || !mid.hasRow {
		t.Errorf("at 50 ms: %v, want it half way", mid)
	}
	for i := shut; i < len(w.shots); i++ {
		if s := w.shots[i]; s.head != rest.head || s.next != rest.next || s.hasRow {
			t.Errorf("frame %d, shut: %v; at rest %v: it jumps at the end", i, s, rest)
		}
	}
	if !closeTo(rest.head, 710.396, 0.01) || !closeTo(rest.next, 748.396, 0.01) {
		t.Errorf("shut at rest: Snoozed %.4f, Settled %.4f; before, 710.396 and 748.396", rest.head, rest.next)
	}
	if t.Failed() || testing.Verbose() {
		w.log()
	}
}

// longRows are the tops of the rows of Settled the sidebar shows in
// its bottom box, under the header of Settled, by title.
func longRows(h *harness) map[string]float32 {
	top := yOf(h, "Settled")
	out := map[string]float32{}
	for _, x := range h.tt.AccessAll() {
		if x.Role == ui.RoleButton && !x.Offscreen && x.Bounds.X < h.a.sidebarWidth && x.Bounds.Y > top && strings.HasPrefix(x.Name, "Settled ") {
			out[x.Name] = x.Bounds.Y
		}
	}
	return out
}

// Glitched's own Settled with 40 threads more opens as far as the bottom
// box shows under it, half the sidebar: the box grows, Snoozed rising over
// it, as the curtain uncovers Settled's rows, frame by frame till the
// section is all but open, and then it is full; no row moves from the
// last frame it opens to its rest. It shuts back the same way, the box
// shrinking from the first frame.
func TestSidebarFoldNativeOpensAndShutsALongSection(t *testing.T) {
	h := nativeScene(t, false, 40)
	w := &nativeWatch{t: t, h: h, cat: catSettled, header: "Settled", next: "Snoozed", row: "Settled long ago"}
	before := w.shot()
	w.click()
	w.frames(16)
	h.tt.Advance(time.Second)
	rest := w.shot()
	rose := func(s nativeShot) float32 { return before.next - s.next }
	full := rose(rest)
	if full < 200 {
		t.Fatalf("opened, Snoozed rose %.1f: the box didn't fill", full)
	}
	// How far the curtain goes, as its first frame shows it.
	span := rose(w.shots[1]) / w.shots[1].p
	last := -1
	for i, s := range w.shots {
		if s.p < 1 {
			last = i
		}
		if i == 0 {
			continue
		}
		prev := w.shots[i-1]
		if rose(s) < rose(prev)-0.01 {
			t.Errorf("opening, frame %d: Snoozed fell, %v after %v", i, s, prev)
		}
		// Till it is all but open, the box grows, Snoozed rising as far as
		// the curtain uncovers Settled's rows.
		if s.p < 0.98 {
			if !(rose(s) > rose(prev)+0.05) || !(rose(s) < full-0.5) {
				t.Errorf("opening, frame %d: the box stopped growing, Snoozed rose %.2f of %.2f, %v after %v", i, rose(s), full, s, prev)
			}
			if !closeTo(rose(s), s.p*span, 0.75) {
				t.Errorf("opening, frame %d: Snoozed rose %.2f at p %.3f, want %.2f", i, rose(s), s.p, s.p*span)
			}
		}
	}
	if span < full || span > full+20 {
		t.Errorf("the curtain goes %.2f, the box grows %.2f: it uncovers more than the box shows, or less", span, full)
	}
	if last < 0 || last >= len(w.shots)-1 || last > 13 {
		t.Fatalf("Settled not open by 216 ms: %v", w.shots[len(w.shots)-1])
	}
	if s := w.shots[last+1]; !closeTo(rose(s), full, 0.5) {
		t.Errorf("open at 200 ms, %v; at rest %v", s, rest)
	}
	if t.Failed() || testing.Verbose() {
		w.log()
	}
	// The rows, between the last frame it opens and its rest.
	h2 := nativeScene(t, false, 40)
	w2 := &nativeWatch{t: t, h: h2, cat: catSettled, header: "Settled", next: "Snoozed", row: "Settled long ago"}
	w2.click()
	var then map[string]float32
	for range 16 {
		h2.tt.Advance(foldFrame)
		if h2.a.sectionP(catSettled) < 1 {
			then = longRows(h2)
		}
	}
	h2.tt.Advance(time.Second)
	now := longRows(h2)
	if len(now) < 5 {
		t.Fatalf("open, Settled shows %d rows", len(now))
	}
	for name, y := range now {
		if was, ok := then[name]; !ok || !closeTo(was, y, 0.01) {
			t.Errorf("the row %q was at %.2f (%v) the last frame Settled opened, at rest %.2f", name, was, ok, y)
		}
	}
	// And shut.
	w2.shots = nil
	opened := w2.shot()
	w2.click()
	w2.frames(16)
	h2.tt.Advance(time.Second)
	fell := func(s nativeShot) float32 { return s.next - opened.next }
	for i := 1; i < len(w2.shots); i++ {
		s, prev := w2.shots[i], w2.shots[i-1]
		if fell(s) < fell(prev)-0.01 {
			t.Errorf("shutting, frame %d: Snoozed rose, %v after %v", i, s, prev)
		}
		// Till it is all but shut, the box shrinks, Snoozed falling as far
		// as the curtain covers Settled's rows.
		if s.p > 0.02 && !(fell(s) > fell(prev)+0.05) {
			t.Errorf("shutting, frame %d: the box didn't shrink, %v after %v", i, s, prev)
		}
		if s.p > 0.02 && !closeTo(full-fell(s), s.p*span, 0.75) {
			t.Errorf("shutting, frame %d: Snoozed fell %.2f at p %.3f, want %.2f", i, fell(s), s.p, full-s.p*span)
		}
	}
	if s := w2.shot(); !closeTo(s.next, before.next, 0.01) || !closeTo(s.head, before.head, 0.01) {
		t.Errorf("shut, %v; before it opened %v", s, before)
	}
	if t.Failed() || testing.Verbose() {
		w2.log()
	}
}

// With less motion asked for, Glitched's own opens and shuts a section in
// the click's frame.
func TestSidebarFoldNativeSnapsWithLessMotion(t *testing.T) {
	h := nativeScene(t, false, 0)
	h.tt.SetPreferences(ui.Preferences{ReduceMotion: true, TextScale: 1})
	h.tt.Advance(time.Second)
	w := &nativeWatch{t: t, h: h, cat: catSnoozed, header: "Snoozed", next: "Settled", row: "Snoozed until tomorrow"}
	shut := w.shot()
	w.click()
	h.tt.Frame()
	opened := w.shot()
	if opened.p != 1 || !closeTo(opened.shows(), rowHeight, 0.01) || !closeTo(shut.head-opened.head, rowHeight, 0.01) {
		t.Errorf("opened with less motion: %v, shut %v", opened, shut)
	}
	h.tt.Advance(foldFrame)
	if s := w.shot(); s.head != opened.head || s.title != opened.title {
		t.Errorf("opened with less motion, it moved on: %v, then %v", opened, s)
	}
	w.click()
	h.tt.Frame()
	if s := w.shot(); s.p != 0 || s.hasRow || s.head != shut.head {
		t.Errorf("shut with less motion: %v, shut before %v", s, shut)
	}
}

// A thread joining Snoozed as it opens shows in the curtain whole, at
// once, as the row there does: neither folds in on its own, behind the
// curtain.
func TestSidebarFoldNativeRowsDontFoldInBehindTheCurtain(t *testing.T) {
	h := nativeScene(t, false, 0)
	w := &nativeWatch{t: t, h: h, cat: catSnoozed, header: "Snoozed", next: "Settled", row: "Snoozed until tomorrow"}
	w.click()
	w.frames(3)
	x := h.thread("SIP re-INVITE handling")
	h.a.backend.SetDisposition(x.env, x.id(), api.Disposition{State: api.Snoozed, Until: morning(h.a.now, 1)})
	h.until("it in Snoozed", func() bool { return len(h.a.itemsIn(catSnoozed)) == 2 })
	h.tt.Frame()
	if _, ok := rowBoxUnder(h, "SIP re-INVITE handling", yOf(h, "Snoozed")); !ok {
		t.Fatal("the thread's row isn't in Snoozed")
	}
	if p := h.a.sectionP(catSnoozed); !(p > 0 && p < 1) {
		t.Fatalf("Snoozed is %.3f open as the thread joins", p)
	}
	rows := []string{"Snoozed until tomorrow", "SIP re-INVITE handling"}
	type seen struct {
		p     float32
		box   [2]ui.Rect
		title [2]ui.Rect
		shows float32
	}
	var frames []seen
	look := func() {
		head := yOf(h, "Snoozed")
		s := seen{p: h.a.sectionP(catSnoozed), shows: yOf(h, "Settled") - head - nativeHeads}
		for i, r := range rows {
			s.box[i], _ = rowBoxUnder(h, r, head)
			s.title[i] = ui.Rect{}
			if t := shownOf(h, r); t.Y > head {
				s.title[i] = t
			}
		}
		frames = append(frames, s)
	}
	look()
	for range 14 {
		h.tt.Advance(foldFrame)
		look()
	}
	h.tt.Advance(time.Second)
	look()
	rest := frames[len(frames)-1]
	if rest.p != 1 {
		t.Fatalf("Snoozed never opened: %+v", rest)
	}
	if d := rest.box[1].Y - rest.box[0].Y; !closeTo(float32(math.Abs(float64(d))), rowHeight+2, 0.01) {
		t.Errorf("open, Snoozed's rows are %.2f apart, want %d", d, rowHeight+2)
	}
	for i, f := range frames {
		top := min(f.box[0].Y, f.box[1].Y)
		for j, r := range rows {
			if f.box[j].H != rowHeight {
				t.Errorf("frame %d: the row of %s is %.2f tall, folding in on its own", i, r, f.box[j].H)
			}
			off, tall := rest.title[j].Y-rest.box[j].Y, rest.title[j].H
			want := curtainTitle(top+f.shows, f.box[j].Y, off, tall)
			if !closeTo(f.title[j].H, want, 0.75) || want > 0 && !closeTo(f.title[j].Y, f.box[j].Y+off, 0.75) {
				t.Errorf("frame %d: %s's title shows %v; the curtain, at %.2f, uncovers %.2f of it", i, r, f.title[j], top+f.shows, want)
			}
		}
	}
	if t.Failed() || testing.Verbose() {
		for i, f := range frames {
			t.Logf("frame %d: %+v", i, f)
		}
	}
}

// Children opened under a row of a section of Glitched's own that doesn't
// fold, the inbox, still fold in: their rows grow over frames.
func TestSidebarFoldNativeLeavesRowsOpeningInTheInboxFoldingIn(t *testing.T) {
	grows := func(t *testing.T, h *harness, title string, full float32) {
		t.Helper()
		var hs []float32
		for range 14 {
			r, _ := rowBox(h, title)
			hs = append(hs, r.H)
			h.tt.Advance(foldFrame)
		}
		r, _ := rowBox(h, title)
		hs = append(hs, r.H)
		grew := false
		for i := 1; i < len(hs); i++ {
			grew = grew || hs[i] > hs[i-1] && hs[i-1] > 0
		}
		if hs[len(hs)-1] != full || !grew || hs[1] >= full {
			t.Errorf("the row of %s doesn't fold in: its heights %v", title, hs)
		}
	}
	t.Run("children opened in the inbox", func(t *testing.T) {
		h := nativeScene(t, true, 0)
		root := h.thread("Ship the sidebar sort")
		h.a.childrenOpen[root.id()] = false
		h.tt.Frame()
		h.tt.Advance(time.Second)
		h.a.childrenOpen[root.id()] = true
		h.tt.Frame()
		grows(t, h, "Write the tests", 28)
	})
}

// Children opened under a row of Settled as it opens show in the curtain
// whole, at once: they don't fold in on their own behind it.
func TestSidebarFoldNativeChildRowsDontFoldInBehindTheCurtain(t *testing.T) {
	h := nativeScene(t, false, 0)
	root := h.thread("Ship the sidebar sort")
	h.a.childrenOpen[root.id()] = false
	h.a.backend.SetDisposition(root.env, root.id(), api.Disposition{State: api.Settled})
	h.until("it in Settled", func() bool { return len(h.a.itemsIn(catSettled)) == 2 })
	h.tt.Advance(time.Second)
	nativeDraws(t, h)
	if err := h.tt.Click("Settled"); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		h.tt.Advance(foldFrame)
	}
	h.a.childrenOpen[root.id()] = true
	h.tt.Frame()
	if p := h.a.sectionP(catSettled); !(p > 0 && p < 1) {
		t.Fatalf("Settled is %.3f open as the children open", p)
	}
	var hs []float32
	for range 12 {
		r, _ := rowBox(h, "Write the tests")
		hs = append(hs, r.H)
		h.tt.Advance(foldFrame)
	}
	for i, x := range hs {
		if x != 28 {
			t.Errorf("frame %d: the child's row is %.2f tall, folding in on its own: %v", i, x, hs)
			break
		}
	}
}

// A thread sent from the hero still slides into Glitched's own sidebar:
// into the inbox, which never folds. It guards that a row arrives as
// before while no section folds.
func TestSidebarFoldNativeLeavesTheHerosRowArrivingInTheInbox(t *testing.T) {
	h := start(t, inboxDir(t))
	revealNative(t, h)
	h.a.select_(h.thread("Investigate websocket reconnect loop"))
	h.tt.Advance(time.Second)
	h.click("New thread")
	h.tt.Advance(time.Second)
	h.tt.Type("Build the hero")
	h.click("Send")
	var xs []float32
	for range 30 {
		h.tt.Advance(foldFrame)
		if r, ok := h.tt.Find("Build the hero"); ok && r.X < h.a.sidebarWidth {
			xs = append(xs, r.X)
		}
	}
	if d := h.a.partsDrawn["sidebar.body"]; d == nil || len(d.copies) == 0 || d.copies[0].plugin != "" {
		t.Fatal("the inbox isn't Glitched's own")
	}
	if len(xs) < 2 || !(xs[0] < xs[len(xs)-1]) {
		t.Errorf("the hero's row doesn't slide in: its x frame by frame %v", xs)
	}
}

// At rest, Snoozed opened or shut, Glitched's own sidebar draws as it did
// before sections folded, pixel for pixel: NATIVE_BEFORE is the folder of
// before-native-{expand,collapse}-snoozed.png, strips of frames of the
// left 330 DIPs of the window, 4 apart, a label over each 18 high, the
// eighth 300 ms after the click.
func TestSidebarFoldNativeDrawsAtRestAsBefore(t *testing.T) {
	dir := os.Getenv("NATIVE_BEFORE")
	if dir == "" {
		t.Skip()
	}
	for _, mode := range []string{"expand", "collapse"} {
		t.Run(mode, func(t *testing.T) {
			f, err := os.Open(filepath.Join(dir, "before-native-"+mode+"-snoozed.png"))
			if err != nil {
				t.Fatal(err)
			}
			strip, err := png.Decode(f)
			f.Close()
			if err != nil {
				t.Fatal(err)
			}
			h := nativeScene(t, mode == "collapse", 0)
			if err := h.tt.Click("Snoozed"); err != nil {
				t.Fatal(err)
			}
			for range 300 / 16 {
				h.tt.Advance(16 * time.Millisecond)
			}
			h.tt.Advance(300*time.Millisecond - 300/16*16*time.Millisecond)
			got := h.tt.Image()
			w := 330 * got.Bounds().Dx() / 1320
			x0 := 7 * (w + 4)
			diff, box := 0, image.Rectangle{Min: image.Point{1 << 30, 1 << 30}}
			for y := 18; y < got.Bounds().Dy(); y++ {
				for x := 0; x < w; x++ {
					r1, g1, b1, _ := got.At(x, y).RGBA()
					r2, g2, b2, _ := strip.At(x0+x, y).RGBA()
					if r1>>8 != r2>>8 || g1>>8 != g2>>8 || b1>>8 != b2>>8 {
						diff++
						box.Min.X, box.Min.Y = min(box.Min.X, x), min(box.Min.Y, y)
						box.Max.X, box.Max.Y = max(box.Max.X, x+1), max(box.Max.Y, y+1)
					}
				}
			}
			if diff > 0 {
				if out := os.Getenv("NATIVE_DIR"); out != "" {
					o, _ := os.Create(filepath.Join(out, "native-rest-"+mode+"-now.png"))
					png.Encode(o, got)
					o.Close()
				}
				t.Errorf("at rest Glitched's own sidebar draws %d pixels otherwise than before, within %v", diff, box)
			}
		})
	}
}
