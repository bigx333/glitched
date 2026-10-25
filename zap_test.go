package main

import (
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// Allow once breaks the answers up from its button, in fast mode's colors,
// and the zap is over once the row's room has closed; the card keeps its
// answer.
func TestAllowZapsTheAnswers(t *testing.T) {
	h, _ := asking(t, runTests)
	h.click("Allow once")
	z := h.a.zap
	if z.request.IsZero() || z.denied {
		t.Fatalf("after Allow once: zap %+v; want one, allowed", z)
	}
	if mid := h.a.answerAt.allow.X + h.a.answerAt.allow.W/2 - h.a.answerAt.row.X; z.ox < mid-1 || z.ox > mid+1 {
		t.Errorf("the zap breaks up from %v in the row; want Allow once's middle, %v", z.ox, mid)
	}
	h.until("the answer", func() bool { return h.tt.HasText("Allowed") })
	h.tt.Advance(2 * time.Second)
	if !h.a.zap.request.IsZero() {
		t.Error("the zap is still held once its room closed")
	}
	if h.tt.HasText("Allow once") {
		t.Error("the answers still show once answered")
	}
}

// ⇧⌘D zaps the answers away in red, from Deny.
func TestDenyZapsInRed(t *testing.T) {
	h, _ := asking(t, runTests)
	h.tt.Key(ui.Cmd|ui.Shift, ui.KeyD)
	if z := h.a.zap; z.request.IsZero() || !z.denied {
		t.Fatalf("after ⇧⌘D: zap %+v; want one, denied", z)
	}
	h.until("the denial", func() bool { return h.tt.HasText("Denied") })
}

// TestZapStrip writes the frames of an approval's answers zapped away, and
// them one over the other in strip.png: ZAPSTRIP=<dir>, with LIGHT and
// BUTTON (Allow once or Deny). The tester paints as on a GPU.
func TestZapStrip(t *testing.T) {
	dir := os.Getenv("ZAPSTRIP")
	if dir == "" {
		t.Skip()
	}
	h, _ := asking(t, runTests)
	h.a.settings.theme = darkTheme.name
	if os.Getenv("LIGHT") != "" {
		h.a.settings.theme = lightTheme.name
	}
	tt := ui.NewTester(func(c *ui.Context) { gpu = true; h.a.view(c) }, 1320, 860)
	h.tt = tt
	tt.Frame()
	tt.Advance(time.Second)
	b, ok := tt.Find("Allow once")
	if !ok {
		t.Fatal("no approval")
	}
	crop := image.Rect(int(b.X)-40, int(b.Y)-200, int(b.X)+800, int(b.Y)+80)
	button := os.Getenv("BUTTON")
	if button == "" {
		button = "Allow once"
	}
	var frames []image.Image
	shot := func(name string) {
		img := tt.Image().SubImage(crop)
		frames = append(frames, img)
		f, err := os.Create(filepath.Join(dir, name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		png.Encode(f, img)
	}
	shot("000-before")
	if err := tt.Click(button); err != nil {
		t.Fatal(err)
	}
	for ms := 0; ms <= 720; ms += 40 {
		if ms > 0 {
			tt.Advance(40 * time.Millisecond)
		}
		shot(fmt.Sprintf("%03d-zap-%04dms", ms/40+1, ms))
	}
	h.until("the answer", func() bool { return h.tt.HasText("Allowed") || h.tt.HasText("Denied") })
	tt.Advance(time.Second)
	shot("999-settled")
	hgt := crop.Dy()
	strip := image.NewRGBA(image.Rect(0, 0, crop.Dx(), hgt*len(frames)))
	for i, f := range frames {
		draw.Draw(strip, image.Rect(0, i*hgt, crop.Dx(), (i+1)*hgt), f, crop.Min, draw.Src)
	}
	f, err := os.Create(filepath.Join(dir, "strip.png"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	png.Encode(f, strip)
}
