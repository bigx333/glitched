package main

import (
	"context"
	"image"
	"image/png"
	"os"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"

	"github.com/egoist/mygo/ui"
)

// At the window's narrowest, the composer's actions are drawn whole
// beside a model whose name doesn't fit: the model's name gives way.
func TestTheComposerActionsFitAtTheNarrowestWindow(t *testing.T) {
	for name, tc := range map[string]struct {
		open    func(t *testing.T) *harness
		actions map[string]float32
	}{
		"draft": {func(t *testing.T) *harness {
			h := enterReady(t, longModel{burst{}}, "Start work.")
			h.until("the agent at work", func() bool { return h.a.current().View.Run == api.RunRunning })
			h.a.draft = "Then update the docs."
			return h
		}, map[string]float32{"Queue": 60, "Stop": 30, "Send": 30}},
		"code": {func(t *testing.T) *harness {
			h := enterReady(t, longModel{burst{}}, "Start work.")
			h.until("the agent at work", func() bool { return h.a.current().View.Run == api.RunRunning })
			h.a.code = blocks(h.a, "go")
			codeSeg(h.a).text = "var x = 1"
			return h
		}, map[string]float32{"Queue": 60, "Stop": 30, "Send": 30}},
		"held": {func(t *testing.T) *harness {
			h := enterReady(t, longModel{fake.New(runTests)}, "Run the sidebar tests.")
			h.until("the approval card", func() bool { return h.tt.HasText("Approval needed") })
			th := h.a.current()
			h.a.backend.Send(th.env, th.id(), "Then update the docs.", api.AfterActive)
			h.until("the queued message", func() bool { return h.a.current().View.Queued == 1 })
			h.click("Stop")
			h.until("Resume", func() bool { return h.tt.HasText("Resume") && !h.a.current().View.Live() })
			h.a.draft = "And the changelog."
			return h
		}, map[string]float32{"Resume": 60, "Send": 30}},
	} {
		t.Run(name, func(t *testing.T) {
			h := tc.open(t)
			h.tt.SetSize(760, 860)
			h.tt.Advance(time.Second)
			if out := os.Getenv("FITSHOTS"); out != "" {
				f, err := os.Create(out + "/composer-760-" + name + ".png")
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				if err := png.Encode(f, h.tt.Image()); err != nil {
					t.Fatal(err)
				}
			}
			for s, want := range tc.actions {
				r, ok := h.tt.Find(s)
				if !ok {
					t.Fatalf("no %s in %q", s, h.tt.Texts())
				}
				if w := paintedWidth(h.tt.Image(), r); w < want-1 || r.X+r.W > 760 {
					t.Errorf("%s at %+v is drawn %.0f points wide; want %.0f, in the window", s, r, w, want)
				}
			}
		})
	}
}

// paintedWidth is how wide the control at r is drawn along its middle
// row: from the first pixel of it that isn't the card's ground to the
// last. A control the card cut is drawn as far as the cut.
func paintedWidth(img *image.RGBA, r ui.Rect) float32 {
	y := int(r.Y + r.H/2)
	ground := img.At(int(r.X+r.W)+1, int(r.Y)-4)
	first, last := -1, -1
	for x := int(r.X); x <= int(r.X+r.W); x++ {
		if img.At(x, y) != ground {
			if first < 0 {
				first = x
			}
			last = x
		}
	}
	if first < 0 {
		return 0
	}
	return float32(last - first + 1)
}

// longModel is an adapter whose model, the one threads run, has a name
// as long as Claude's are drawn on macOS: in the test's font, the
// narrowest window's composer row doesn't fit it with the actions.
type longModel struct{ provider.Adapter }

func (longModel) Describe(context.Context) (provider.Info, error) {
	return provider.Info{Name: "fake", Version: "1", Models: []provider.Model{{
		ID: "fake", Name: "Claude Opus 5.5 with a long context window", Default: true,
		Efforts: []provider.Effort{{ID: "high", Label: "High"}}, DefaultEffort: "high",
	}}}, nil
}
