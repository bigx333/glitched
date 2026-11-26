//go:build timelinebench

package main

import (
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/daemon"
	"glitched/internal/provider/fake"
)

// TestTimelinePixels writes PNGs of long and short threads in several
// states to PIXELS_DIR, to compare two trees.
func TestTimelinePixels(t *testing.T) {
	out := os.Getenv("PIXELS_DIR")
	if out == "" {
		t.Skip()
	}
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(false)))
	seed(t, d, "/work/t3code", "A long thread", "Start")
	seed(t, d, "/work/t3code", "Another thread", "Hello")
	h := start(t, dir)
	long, other := h.thread("A long thread"), h.thread("Another thread")
	h.a.select_(long)
	h.until("its transcript", func() bool { return long.Transcript != nil && len(long.Transcript.Entries) > 0 })
	shot := func(name string) {
		h.tt.Advance(time.Second)
		f, _ := os.Create(filepath.Join(out, name+".png"))
		png.Encode(f, h.tt.Image())
		f.Close()
	}
	for _, n := range []int{15, 300, 5000} {
		long.Transcript.Entries = synthetic(n)
		h.a.changed()
		h.a.select_(other)
		h.tt.Advance(time.Second)
		delete(h.a.lists, long.id())
		h.a.select_(long)
		shot(fmt.Sprintf("%05d-open", n))
		r, ok := h.tt.Find("Go to: Turn 2: look at chat.go and tell me why the list jumps when a reply streams in.")
		if !ok {
			t.Fatal("no tick for turn 2")
		}
		h.tt.Move(r.X+r.W/2, r.Y+r.H/2)
		shot(fmt.Sprintf("%05d-tick", n))
		h.tt.Click("Go to: Turn 2: look at chat.go and tell me why the list jumps when a reply streams in.")
		h.tt.Move(800, 400)
		shot(fmt.Sprintf("%05d-turn2", n))
		for range 5 {
			h.tt.Wheel(800, 400, 0, 120)
		}
		shot(fmt.Sprintf("%05d-wheel", n))
		h.a.listFor(long.id()).ScrollToEnd()
		h.tt.Frame()
		if r, ok := h.tt.Find("Ran 1 command · edited 1 file · searched 1 time"); ok {
			h.tt.Move(r.X+r.W/2, r.Y+r.H/2)
		}
		shot(fmt.Sprintf("%05d-hover", n))
		h.tt.Move(800, 30)
		_ = ui.Start
	}
}
