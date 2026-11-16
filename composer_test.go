package main

import (
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/daemon"
	"glitched/internal/provider/fake"
)

// Scrolled up, the composer folds, most of the way in about 125ms, and
// opens again at the end, frame by frame, never jumping; turned round
// mid-way, it goes back from where it is, at once and as quickly; with less
// motion asked for, it's at once.
func TestComposerMorph(t *testing.T) {
	// ks is k in each frame run, as more than one can be in 16ms: the
	// caret's blink takes a frame of its own between two refreshes.
	var ks []float32
	a, tt, th := longThread(t, func(a *app) func(*ui.Context) {
		return func(c *ui.Context) {
			a.view(c)
			ks = append(ks, a.composerK)
		}
	})
	l := a.listFor(th.id())
	tt.Advance(time.Second)
	if a.composerK != 1 || a.jumpK != 0 {
		t.Fatalf("at the end: k %v, jump %v; want 1, 0", a.composerK, a.jumpK)
	}
	const frame = 16 * time.Millisecond
	// The most a frame of the folding spring moves, about 0.19: any more
	// is a jump.
	const maxStep = 0.25
	// step runs n frames, failing on a jump from one frame to the next, a
	// turn the wrong way, or k out of [0, 1]; it returns the k reached.
	step := func(n int, down bool) float32 {
		t.Helper()
		for range n {
			prev := a.composerK
			ks = ks[:0]
			tt.Advance(frame)
			for _, k := range ks {
				d := k - prev
				if down {
					d = -d
				}
				if d < 0 || d > maxStep || k < 0 || k > 1 {
					t.Fatalf("k went %v → %v in a frame, the wrong way or too far", prev, k)
				}
				prev = k
			}
		}
		return a.composerK
	}
	l.ScrollTo(0, ui.Start)
	tt.Frame()
	if k := step(6, true); k <= 0 || k >= 1 {
		t.Fatalf("~100ms into folding, k %v; want mid-way", k)
	}
	// Back to the end mid-way, as the list glides there: it turns round
	// from where it is, never jumping.
	l.ScrollToEnd()
	for range 40 {
		prev := a.composerK
		tt.Advance(frame)
		if d := a.composerK - prev; d < -maxStep || d > maxStep {
			t.Fatalf("turning round, k went %v → %v in a frame", prev, a.composerK)
		}
	}
	if a.composerK != 1 || a.jumpK != 0 {
		t.Fatalf("opened: k %v, jump %v", a.composerK, a.jumpK)
	}
	l.ScrollTo(0, ui.Start)
	tt.Frame()
	step(24, true)
	if a.composerK != 0 || a.jumpK != 1 {
		t.Fatalf("folded after 384ms: k %v, jump %v; want 0, 1", a.composerK, a.jumpK)
	}
	l.ScrollToEnd()
	tt.Frame()
	for range 60 {
		tt.Advance(frame)
		if l.AtEnd() {
			break
		}
	}
	step(24, false)
	if a.composerK != 1 {
		t.Fatalf("opened 384ms after the end: k %v; want 1", a.composerK)
	}
	// Turned round at once, by typing, as it folds: it doesn't stall and
	// start over from rest, but is most of the way back in ~160ms.
	l.ScrollTo(0, ui.Start)
	tt.Frame()
	mid := step(5, true)
	if mid <= 0.1 || mid >= 0.9 {
		t.Fatalf("~80ms into folding, k %v; want mid-way", mid)
	}
	a.draft = "x"
	low := mid
	for i := range 10 {
		prev := a.composerK
		tt.Advance(frame)
		if d := a.composerK - prev; d < -maxStep || d > maxStep || a.composerK < 0 || a.composerK > 1 {
			t.Fatalf("turning round, k went %v → %v in a frame", prev, a.composerK)
		}
		low = min(low, a.composerK)
		// A curve starting over from rest would barely have moved yet.
		if i == 3 && a.composerK-low < 0.15 {
			t.Fatalf("64ms after turning round, k %v from its lowest %v: stalled", a.composerK, low)
		}
	}
	if a.composerK < 0.85 {
		t.Fatalf("160ms after turning round from %v, k %v; want most of the way back", mid, a.composerK)
	}
	step(16, false)
	if a.composerK != 1 {
		t.Fatalf("416ms after turning round, k %v; want 1", a.composerK)
	}
	a.draft = ""
	l.ScrollToEnd()
	for range 60 {
		tt.Advance(frame)
		if l.AtEnd() {
			break
		}
	}
	tt.Advance(time.Second)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	l.ScrollTo(0, ui.Start)
	tt.Frame()
	tt.Frame()
	if a.composerK != 0 || a.jumpK != 1 {
		t.Errorf("with less motion, folding moved: k %v, jump %v", a.composerK, a.jumpK)
	}
	a.draft = "x"
	tt.Frame()
	tt.Frame()
	if a.composerK != 1 {
		t.Errorf("with less motion, opening moved: k %v", a.composerK)
	}
	a.draft = ""
}

// The first fold of a session takes the compact card's height, before it's
// been at rest compact to be measured, as it is: it doesn't change by more
// than a hair from one frame to the next, nor at rest, with Liquid Glass or
// the plain surface's border, in any font size. Nor does the first fold
// after the font size or Liquid Glass changes, what was measured before
// going with the old settings.
func TestComposerFirstFoldHeight(t *testing.T) {
	// fold folds the composer, then opens it, watching the card's
	// heights, as heights has them each frame, as it does.
	fold := func(t *testing.T, a *app, tt *ui.Tester, th thread, heights *[2]float32) {
		t.Helper()
		a.listFor(th.id()).ScrollTo(0, ui.Start)
		tt.Frame()
		prev := heights[0]
		for range 40 {
			tt.Advance(16 * time.Millisecond)
			h := heights[0]
			if d := h - prev; d < -0.1 || d > 0.1 {
				t.Fatalf("at k %v, the compact card's height went %v → %v", a.composerK, prev, h)
			}
			prev = h
		}
		if a.composerK != 0 || a.cardCompactH == 0 {
			t.Fatalf("folded: k %v, compact %v", a.composerK, a.cardCompactH)
		}
		a.listFor(th.id()).ScrollToEnd()
		tt.Frame()
		prev = heights[1]
		for range 40 {
			tt.Advance(16 * time.Millisecond)
			h := heights[1]
			if a.cardFullH != 0 {
				if d := h - prev; d < -0.1 || d > 0.1 {
					t.Fatalf("at k %v, the full card's height went %v → %v", a.composerK, prev, h)
				}
			}
			prev = h
		}
		if a.composerK != 1 {
			t.Fatalf("opened: k %v", a.composerK)
		}
	}
	for _, on := range []bool{true, false} {
		for _, size := range []float64{11, 13, 16} {
			t.Run(fmt.Sprintf("glass=%v/%vpt", on, size), func(t *testing.T) {
				var heights [2]float32
				a, tt, th := longThread(t, func(a *app) func(*ui.Context) {
					a.settings.glass = on
					a.settings.fontSize = size
					return watchCard(a, &heights)
				})
				fold(t, a, tt, th, &heights)
			})
		}
	}
	for _, change := range []struct {
		name string
		set  func(*settings)
	}{
		{"font", func(s *settings) { s.fontSize = 16 }},
		{"smaller font", func(s *settings) { s.fontSize = 11 }},
		{"glass", func(s *settings) { s.glass = !s.glass }},
	} {
		t.Run("then "+change.name, func(t *testing.T) {
			var heights [2]float32
			a, tt, th := longThread(t, func(a *app) func(*ui.Context) { return watchCard(a, &heights) })
			fold(t, a, tt, th, &heights)
			change.set(&a.settings)
			tt.Advance(time.Second)
			fold(t, a, tt, th, &heights)
		})
	}
}

// watchCard is a's view, which leaves the composer card's heights, compact
// and full, as cardHeights has them at the end of each frame, in heights.
func watchCard(a *app, heights *[2]float32) func(*ui.Context) {
	return func(c *ui.Context) {
		a.view(c)
		heights[0], heights[1] = a.cardHeights(c)
	}
}

// Changed in the settings while the composer is compact, the font size or
// Liquid Glass doesn't make it jump as it opens (checkOpen).
func TestComposerOpensAfterSettingsChange(t *testing.T) {
	for _, change := range []struct {
		name string
		set  func(*settings)
	}{
		{"11pt", func(s *settings) { s.fontSize = 11 }},
		{"16pt", func(s *settings) { s.fontSize = 16 }},
		{"glass", func(s *settings) { s.glass = !s.glass }},
	} {
		t.Run(change.name, func(t *testing.T) {
			a, tt, th := longThread(t, nil)
			l := a.listFor(th.id())
			foldComposer(t, a, tt, th)
			a.router.Push("/settings/general")
			tt.Advance(time.Second)
			change.set(&a.settings)
			tt.Advance(time.Second)
			a.router.Push("/")
			tt.Advance(time.Second)
			if a.composerK != 0 {
				t.Fatalf("back from the settings: k %v; want still compact", a.composerK)
			}
			checkOpen(t, a, tt, func() { l.ScrollToEnd() })
		})
	}
}

// Compact, the composer opens as it comes to hold something, a long draft
// typed into its line or a paste long enough to be an attachment, without a
// jump (checkOpen), its text and caret where they'll be at rest; what's
// typed next goes on into the draft.
func TestComposerOpensWithContent(t *testing.T) {
	draft := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 8)[:360]
	for _, size := range []float64{13, 16} {
		for _, open := range []struct {
			name     string
			start    func(tt *ui.Tester)
			attached int
		}{
			{"long draft", func(tt *ui.Tester) { tt.Type(draft) }, 0},
			{"pasted attachment", func(tt *ui.Tester) {
				tt.SetClipboard(strings.Repeat("a pasted line\n", longPaste/14+1))
				tt.Key(ui.Cmd, ui.KeyV)
			}, 1},
		} {
			t.Run(fmt.Sprintf("%vpt/%s", size, open.name), func(t *testing.T) {
				a, tt, th := longThread(t, func(a *app) func(*ui.Context) {
					a.settings.fontSize = size
					return a.view
				})
				foldComposer(t, a, tt, th)
				// The line, by the keyboard: a click on it would open the
				// composer before anything is typed.
				for range 200 {
					if active, _ := tt.TextInputActive(); active {
						break
					}
					tt.Key(0, ui.KeyTab)
				}
				if active, _ := tt.TextInputActive(); !active || a.composerK != 0 {
					t.Fatalf("the compact line has no focus: active %v, k %v", active, a.composerK)
				}
				checkOpen(t, a, tt, func() { open.start(tt) })
				want := draft
				if open.attached > 0 {
					want = ""
				}
				if a.draft != want || len(a.attachments) != open.attached {
					t.Fatalf("opened with the draft %q and %d attachments; want %q and %d", a.draft, len(a.attachments), want, open.attached)
				}
				before := a.draft
				tt.Type("!")
				tt.Frame()
				if a.draft != before+"!" {
					t.Errorf("typed after the open, the draft went %q → %q: the text area has no focus", before, a.draft)
				}
			})
		}
	}
}

// What's typed into the compact line as the composer comes to open, in the
// frames before it shows, keeps its order in the text area that takes the
// line's place, the caret going on from the end of the draft: the unseen
// text area the open is measured by keeps no caret of its own for it.
func TestComposerOpenKeepsTyping(t *testing.T) {
	for _, tc := range []struct {
		name string
		// keys are sent each in a frame of its own, the composer still
		// compact for the first.
		keys []func(tt *ui.Tester)
		want string
	}{
		{"second key the next frame", []func(tt *ui.Tester){
			func(tt *ui.Tester) { tt.Type("h") },
			func(tt *ui.Tester) { tt.Type("e") },
			func(tt *ui.Tester) { tt.Type("llo") },
		}, "hello!"},
		{"keys while compact", []func(tt *ui.Tester){
			func(tt *ui.Tester) { tt.Type("a") },
			func(tt *ui.Tester) { tt.Type("b") },
			func(tt *ui.Tester) { tt.Type("c") },
			func(tt *ui.Tester) { tt.Type("d") },
		}, "abcd!"},
		{"paste then type", []func(tt *ui.Tester){
			func(tt *ui.Tester) {
				tt.SetClipboard(strings.Repeat("a pasted line\n", longPaste/14+1))
				tt.Key(ui.Cmd, ui.KeyV)
			},
			func(tt *ui.Tester) { tt.Type("x") },
			func(tt *ui.Tester) { tt.Type("yz") },
		}, "xyz!"},
		// A caret moved back in the line stays with the line: the text
		// area takes the focus at the end of the draft.
		{"caret mid-line", []func(tt *ui.Tester){
			func(tt *ui.Tester) { tt.Type("abc") },
			func(tt *ui.Tester) { tt.Key(0, ui.KeyLeft) },
		}, "abc!"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, tt, th := longThread(t, nil)
			foldComposer(t, a, tt, th)
			for range 200 {
				if active, _ := tt.TextInputActive(); active {
					break
				}
				tt.Key(0, ui.KeyTab)
			}
			if active, _ := tt.TextInputActive(); !active || a.composerK != 0 {
				t.Fatalf("the compact line has no focus: active %v, k %v", active, a.composerK)
			}
			for i, key := range tc.keys {
				key(tt)
				if i == 0 && a.composerK != 0 {
					t.Fatalf("after the first key, k %v; want still compact", a.composerK)
				}
			}
			tt.Advance(time.Second)
			if a.composerK != 1 {
				t.Fatalf("opened: k %v", a.composerK)
			}
			tt.Type("!")
			tt.Frame()
			if a.draft != tc.want {
				t.Errorf("the draft is %q; want %q", a.draft, tc.want)
			}
		})
	}
}

// Scrolled up with a draft, the composer stays as it is as Jump to latest
// comes in over it, its text area with it: the caret where it was left.
func TestComposerKeepsCaretAsPillShows(t *testing.T) {
	a, tt, th := longThread(t, nil)
	tt.Type("abc")
	tt.Key(0, ui.KeyLeft)
	tt.Key(0, ui.KeyLeft)
	a.listFor(th.id()).ScrollTo(0, ui.Start)
	tt.Frame()
	tt.Advance(time.Second)
	if a.composerK != 1 || a.jumpK != 1 {
		t.Fatalf("scrolled up with a draft: k %v, jump %v; want 1, 1", a.composerK, a.jumpK)
	}
	tt.Type("X")
	tt.Frame()
	if a.draft != "aXbc" {
		t.Errorf("the draft is %q; want %q, typed where the caret was left", a.draft, "aXbc")
	}
}

// foldComposer scrolls th's chat up from its end, the composer folding,
// and waits for it to be at rest, compact and measured.
func foldComposer(t *testing.T, a *app, tt *ui.Tester, th thread) {
	t.Helper()
	tt.Advance(time.Second)
	a.listFor(th.id()).ScrollTo(0, ui.Start)
	tt.Frame()
	tt.Advance(time.Second)
	if a.composerK != 0 || a.cardCompactH == 0 {
		t.Fatalf("scrolled up: k %v, compact %v; want 0, measured", a.composerK, a.cardCompactH)
	}
}

// checkOpen opens the compact composer, by start, and fails if the card's
// height, as composer sets it frame by frame, ever drops, or moves in a
// frame by more than a pixel and a half from the share of the way from
// the compact card to the full one, as they are at rest, that k moved; or
// if the morph's last frame has the card more than half a pixel from its
// height at rest, or the caret more than half a pixel from where it rests.
func checkOpen(t *testing.T, a *app, tt *ui.Tester, start func()) {
	t.Helper()
	compact := a.cardCompactH
	start()
	tt.Frame()
	type step struct{ k, h float32 }
	var steps []step
	var caret ui.Rect
	var caretOK bool
	for range 60 {
		tt.Advance(16 * time.Millisecond)
		if k := a.composerK; k > 0 && k < 1 {
			steps = append(steps, step{k, a.cardH})
			caret, caretOK = tt.TextCaret()
		}
	}
	tt.Advance(time.Second)
	if a.composerK != 1 || len(steps) == 0 {
		t.Fatalf("opened: k %v after %d frames of morph", a.composerK, len(steps))
	}
	full := a.cardFullH
	if full == 0 {
		t.Fatal("the full card wasn't measured at rest")
	}
	prev := step{0, compact}
	for _, s := range steps {
		dh := s.h - prev.h
		if dh < -0.1 {
			t.Errorf("at k %.3f, the card went %.1f → %.1f high: it shrank", s.k, prev.h, s.h)
		}
		if want := (full - compact) * (s.k - prev.k); dh-want > 1.5 || want-dh > 1.5 {
			t.Errorf("at k %.3f, the card went %.1f → %.1f high, %.1f px where k's step from %.1f to %.1f is %.1f", s.k, prev.h, s.h, dh, compact, full, want)
		}
		prev = s
	}
	if d := prev.h - full; d < -0.5 || d > 0.5 {
		t.Errorf("the card was %v high in the morph's last frame, %v at rest: a %v px jump", prev.h, full, -d)
	}
	rest, ok := tt.TextCaret()
	if !ok || !caretOK {
		t.Fatalf("no caret: in the morph's last frame %v, at rest %v", caretOK, ok)
	}
	{
		if dx, dy := rest.X-caret.X, rest.Y-caret.Y; dx < -0.5 || dx > 0.5 || dy < -0.5 || dy > 0.5 {
			t.Errorf("the caret went from %v in the morph's last frame to %v at rest", caret, rest)
		}
	}
}

// What fades with the composer's morph shows as far as it's faded in,
// either side of where it starts taking presses, MyGo's dimming of what's
// disabled made up for: a block faded as the morph fades its parts, over a
// plain ground, is painted x of the way from the ground to its color, not
// half that below shownAt.
func TestComposerFadesEvenly(t *testing.T) {
	var x float32
	view := func(c *ui.Context) {
		ui.Column(c).Size(40, 40).Background(ui.RGBA(255, 255, 255, 1)).Children(func() {
			faded(ui.Box(c).Size(40, 40).Background(ui.RGBA(0, 0, 0, 1)), x)
		})
	}
	tt := ui.NewTester(view, 40, 40)
	for i := 1; i < 100; i++ {
		x = float32(i) / 100
		tt.Frame()
		img := tt.Image()
		b := img.Bounds()
		c := img.RGBAAt(b.Min.X+b.Dx()/2, b.Min.Y+b.Dy()/2)
		if got := 1 - float32(c.G)/255; got < x-0.02 || got > x+0.02 {
			t.Errorf("faded %.2f of the way in, painted %.2f of the way", x, got)
		}
		if _, off := fadedOpacity(x); off != (x < shownAt) {
			t.Errorf("faded %.2f of the way in, disabled %v", x, off)
		}
	}
}

// TestComposerStrip writes frames of the composer folding, then opening,
// into STRIP=<dir>, each the bottom of the chat, and strip.png of them all:
// folding down the left, opening down the right.
func TestComposerStrip(t *testing.T) {
	dir := os.Getenv("STRIP")
	if dir == "" {
		t.Skip()
	}
	a, tt, th := longThread(t, func(a *app) func(*ui.Context) {
		a.settings.theme = darkTheme.name
		return a.view
	})
	l := a.listFor(th.id())
	const cut, frames = 330, 7
	crop := func() *image.RGBA {
		img := tt.Image()
		b := img.Bounds()
		// The chat's bottom, beside the sidebar.
		x := b.Min.X + int(a.sidebarWidth)*b.Dx()/1320
		r := image.Rect(x, b.Max.Y-cut*b.Dy()/860, b.Max.X, b.Max.Y)
		out := image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
		draw.Draw(out, out.Bounds(), img, r.Min, draw.Src)
		return out
	}
	var shots []*image.RGBA
	// shoot starts the morph, waits for it to start, then shoots it.
	shoot := func(name string, start func(), started func() bool, total time.Duration) {
		start()
		tt.Frame()
		for range 60 {
			if started() {
				break
			}
			tt.Advance(16 * time.Millisecond)
		}
		for i := range frames {
			if i > 0 {
				tt.Advance(total / (frames - 1))
			}
			s := crop()
			shots = append(shots, s)
			f, err := os.Create(filepath.Join(dir, fmt.Sprintf("%s-%d.png", name, i)))
			if err != nil {
				t.Fatal(err)
			}
			png.Encode(f, s)
			f.Close()
		}
		tt.Advance(time.Second)
	}
	shoot("close", func() { l.ScrollTo(0, ui.Start) }, func() bool { return a.composerK < 1 }, 200*time.Millisecond)
	shoot("open", func() { l.ScrollToEnd() }, func() bool { return a.composerK > 0 }, 240*time.Millisecond)
	w, h := shots[0].Bounds().Dx(), shots[0].Bounds().Dy()
	strip := image.NewRGBA(image.Rect(0, 0, 2*w+8, frames*(h+8)))
	for i, s := range shots {
		col, row := i/frames, i%frames
		at := image.Pt(col*(w+8), row*(h+8))
		draw.Draw(strip, s.Bounds().Add(at), s, image.Point{}, draw.Src)
	}
	f, err := os.Create(filepath.Join(dir, "strip.png"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	png.Encode(f, strip)
}

// longThread is the app showing a thread long enough to scroll, in the
// view view makes of it, or its own; the tester's first second passed.
func longThread(t *testing.T, view func(a *app) func(*ui.Context)) (*app, *ui.Tester, thread) {
	t.Helper()
	h, th := longHarness(t, view)
	return h.a, h.tt, th
}

// longHarness is longThread's harness.
func longHarness(t *testing.T, view func(a *app) func(*ui.Context)) (*harness, thread) {
	t.Helper()
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(false)))
	var msgs []string
	for i := range 8 {
		msgs = append(msgs, fmt.Sprintf("Step %d: look at main.go again.", i+1))
	}
	seed(t, d, "/work/t3code", "A long thread", msgs...)
	h := start(t, dir)
	if view != nil {
		h.tt = ui.NewTester(view(h.a), 1320, 860)
	}
	th := h.thread("A long thread")
	h.a.select_(th)
	h.until("its transcript", func() bool { return h.tt.HasText("Step 8: look at main.go again.") })
	h.tt.Advance(time.Second)
	return h, th
}
