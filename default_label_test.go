package main

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"glitched/internal/daemon"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
)

// modelButtonLanes are the model button's two drawers: the built-in Model
// picker plugin's, and Glitched's own with that plugin off.
var modelButtonLanes = []struct {
	name   string
	native bool
}{{"plugin", false}, {"native", true}}

// drawModelButton has the model button drawn as native says.
func drawModelButton(t *testing.T, h *harness, native bool) {
	t.Helper()
	if !native {
		return
	}
	if err := h.a.plugins.SetEnabled("model-picker", false); err != nil {
		t.Fatal(err)
	}
	h.a.pluginsChanged(nil)
}

// untilShows runs what the app posts, each with the frame its window's
// Update draws, and moves the clock on, till the window shows want and
// keeps showing it a second on: no frame the app doesn't ask for.
func untilShows(t *testing.T, h *harness, want string) {
	t.Helper()
	for end := time.Now().Add(30 * time.Second); ; {
		if h.p.run() {
			h.tt.Frame()
		}
		h.tt.Advance(16 * time.Millisecond)
		if h.tt.HasText(want) && (h.a.plugins == nil || h.a.plugins.Idle()) && !h.p.run() {
			h.tt.Advance(time.Second)
			if h.tt.HasText(want) {
				return
			}
		}
		if time.Now().After(end) {
			t.Fatalf("the window never showed %q: %q", want, h.tt.Texts())
		}
		select {
		case <-h.p.wake:
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// modelTip is what hovering the model button shows of its tooltip.
func modelTip(t *testing.T, h *harness) string {
	t.Helper()
	r, ok := h.tt.Find("Model")
	if !ok {
		t.Fatalf("no model button: %q", h.tt.Texts())
	}
	before := h.tt.Texts()
	h.tt.Move(r.X+r.W/2, r.Y+r.H/2)
	h.tt.Advance(2 * time.Second)
	tip := ""
	for _, s := range h.tt.Texts() {
		if !has(before, s) {
			tip += s
		}
	}
	h.tt.Move(0, 0)
	h.tt.Advance(time.Second)
	return tip
}

// labelShot writes the model button, with room for its tooltip, to
// DEFAULTLABELSHOTS as name.png.
func labelShot(t *testing.T, h *harness, name string) {
	t.Helper()
	out := os.Getenv("DEFAULTLABELSHOTS")
	if out == "" {
		return
	}
	r, ok := h.tt.Find("Model")
	if !ok {
		t.Fatalf("no model button: %q", h.tt.Texts())
	}
	img := h.tt.Image()
	scale := float32(img.Bounds().Dx()) / 1320
	crop := image.Rect(int((r.X-16)*scale), int((r.Y-56)*scale), int((r.X+r.W+220)*scale), int((r.Y+r.H+12)*scale)).Intersect(img.Bounds())
	f, err := os.Create(filepath.Join(out, name+".png"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	png.Encode(f, img.SubImage(crop))
}

// A thread that follows Claude's own default keeps the model and effort
// its draft expected from send till Claude says what it ran, then shows
// what it ran with nothing pending, whether that is the model expected or
// another: the thread asked for neither.
func TestAFollowedDefaultKeepsItsGuessTillTheReportAndIsntPending(t *testing.T) {
	for _, lane := range modelButtonLanes {
		for _, ran := range []struct{ model, name string }{{"beta", "Claude Beta"}, {"gamma", "Claude Gamma"}} {
			t.Run(lane.name+"/"+ran.model, func(t *testing.T) {
				report := make(chan string)
				h := startWith(t, tempDir(t), func(c *daemon.Config) {
					ad := fake.New(fake.Turn(false))
					ad.Confirms = func(s provider.SessionSpec) []provider.Event {
						return []provider.Event{provider.Granted{Access: s.Access, Mode: fake.Modes[s.Access]}, provider.Ran{Model: <-report}}
					}
					c.Providers["claude"] = ad
					engineReportsBy(map[string][]provider.Model{"claude": cliModels(true)})(c)
				})
				t.Cleanup(func() { close(report) })
				allAnswered(h)
				drawModelButton(t, h, lane.native)
				h.a.startIn(draft{h.local(), t.TempDir()})
				h.tt.Frame()
				untilShows(t, h, "Claude Beta")
				labelShot(t, h, "claude-"+lane.name+"-"+ran.model+"-1-draft")
				if !h.tt.HasText("Low") {
					t.Fatalf("the draft shows %q, want the Claude Beta at Low Claude runs where given none", h.tt.Texts())
				}

				h.a.draft = "Say hi."
				h.a.send(h.a.current())
				h.until("the thread's turn", func() bool { v := h.a.current().View; return !v.ID.IsZero() && v.Live() })
				untilShows(t, h, "Say hi.")
				labelShot(t, h, "claude-"+lane.name+"-"+ran.model+"-2-sent")
				if !h.tt.HasText("Claude Beta") || !h.tt.HasText("Low") || h.tt.HasText("Default model") {
					t.Errorf("sent and not yet reported, the button shows %q, want the Claude Beta at Low the draft showed", h.tt.Texts())
				}
				if m := h.a.current().View.Model; m.Model != "" || m.Effort != "" {
					t.Fatalf("the thread asks for %+v, want neither a model nor an effort", m)
				}

				report <- ran.model
				h.until("the report", func() bool { v := h.a.current().View; return v.Confirmed.Model != nil && !v.Live() })
				untilShows(t, h, ran.name)
				labelShot(t, h, "claude-"+lane.name+"-"+ran.model+"-3-confirmed")
				if tip := modelTip(t, h); tip != "Model and effort · the CLI's default model" {
					t.Errorf("Claude ran %s for the default the thread follows, and the button's tooltip says %q, want nothing pending", ran.name, tip)
				}
			})
		}
	}
}

// A thread that follows Codex's own default shows the model Codex ran
// with nothing pending, whether its config named that one or another.
func TestAFollowedCodexDefaultIsntPendingOnceReported(t *testing.T) {
	for _, lane := range modelButtonLanes {
		for _, c := range []struct{ scenario, guess string }{{"hi", "GPT-6.2 Sol"}, {"configured", "GPT-6 Luna"}} {
			t.Run(lane.name+"/"+c.scenario, func(t *testing.T) {
				dir := tempDir(t)
				if err := os.WriteFile(filepath.Join(dir, "composer.json"), []byte(`{"model":{"provider":"codex","id":""}}`), 0o600); err != nil {
					t.Fatal(err)
				}
				h := startWith(t, dir, withCodex(t, c.scenario))
				allAnswered(h)
				drawModelButton(t, h, lane.native)
				h.a.startIn(draft{h.local(), t.TempDir()})
				h.tt.Frame()
				untilShows(t, h, c.guess)
				labelShot(t, h, "codex-"+lane.name+"-"+c.scenario+"-1-draft")
				h.a.draft = "Say hi."
				h.a.send(h.a.current())
				h.until("the report", func() bool {
					v := h.a.current().View
					return v.Confirmed.Model != nil && v.LastTurn != nil && !v.Live()
				})
				untilShows(t, h, "GPT-6.2 Sol")
				labelShot(t, h, "codex-"+lane.name+"-"+c.scenario+"-3-confirmed")
				if tip := modelTip(t, h); tip != "Model and effort · the CLI's default model" {
					t.Errorf("Codex ran the default the thread follows, and the button's tooltip says %q, want nothing pending", tip)
				}
			})
		}
	}
}
