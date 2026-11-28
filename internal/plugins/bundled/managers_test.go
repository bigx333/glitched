package bundled_test

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/plugins"
	"glitched/internal/plugins/bundled"
	"glitched/internal/plugins/pack"
	"glitched/internal/plugins/widget"
)

func openManagers(t *testing.T, do func(plugins.Effect) (any, error)) (*app, *plugins.Session) {
	t.Helper()
	root := t.TempDir()
	key, err := pack.LoadKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sub, err := fs.Sub(bundled.FS, "managers")
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(root, "managers")
	err = fs.WalkDir(sub, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || p == pack.SignatureFile {
			return err
		}
		b, err := fs.ReadFile(sub, p)
		if err != nil {
			return err
		}
		os.MkdirAll(filepath.Join(dst, filepath.Dir(p)), 0o755)
		return os.WriteFile(filepath.Join(dst, p), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := pack.Sign(dst, key); err != nil {
		t.Fatal(err)
	}
	a := &app{t: t, q: make(chan func(), 1024)}
	a.h = plugins.Open(t.TempDir(), plugins.Options{
		Post:        func(fn func()) { a.q <- fn },
		Logf:        t.Logf,
		Bundled:     os.DirFS(root),
		ReleaseKeys: []string{key.Public()},
		CallTimeout: 30 * time.Second,
		Do: func(_ string, e plugins.Effect) (any, error) {
			if do != nil {
				return do(e)
			}
			return nil, nil
		},
	})
	t.Cleanup(a.h.Close)
	s := a.h.App("managers")
	if s == nil {
		t.Fatalf("managers plugin failed to start: %v", a.h.Failed("managers"))
	}
	return a, s
}

func TestManagersPluginHeaderPopoverAndOverview(t *testing.T) {
	var configured []struct {
		Thread string             `json:"thread"`
		Sweep  *api.SweepSchedule `json:"sweep"`
	}
	var swept []string
	var toggled []string
	a, s := openManagers(t, func(e plugins.Effect) (any, error) {
		switch e.Type {
		case "threads.configure":
			var req struct {
				Thread string             `json:"thread"`
				Sweep  *api.SweepSchedule `json:"sweep"`
			}
			if err := json.Unmarshal(e.Args, &req); err != nil {
				return nil, err
			}
			configured = append(configured, req)
		case "threads.sweepNow":
			var req struct{ Thread string }
			if err := json.Unmarshal(e.Args, &req); err != nil {
				return nil, err
			}
			swept = append(swept, req.Thread)
		case "panels.toggle":
			var req struct{ Panel string }
			if err := json.Unmarshal(e.Args, &req); err != nil {
				return nil, err
			}
			toggled = append(toggled, req.Panel)
		}
		return nil, nil
	})

	const customPrompt = "Check CI and report only red builds."
	mgr := map[string]any{
		"id":    "mgr-1",
		"role":  "manager",
		"sweep": map[string]any{"intervalSeconds": 1800, "prompt": customPrompt},
		"manager": map[string]any{
			"sweepTiming": "Next in 30m",
			"needs":       2,
		},
	}
	plain := map[string]any{"id": "plain-1"}

	s.SetItems("pills", []plugins.PlaceItem{
		{Key: "mgr-1", Version: "1", Context: mgr},
		{Key: "plain-1", Version: "1", Context: plain},
	})
	a.until("header pills", func() bool { return s.PlaceView("pills", "mgr-1") != nil })

	pills := &widget.View{Body: s.PlaceView("pills", "mgr-1")}
	if sw := pills.Find("sweep"); sw == nil || sw.Label != "Sweep 30m" || sw.Open {
		t.Fatalf("sweep button = %+v, want closed Sweep 30m", sw)
	}
	if ov := pills.Find("overview"); ov == nil || ov.Label != "Overview" {
		t.Fatalf("overview button = %+v, want Overview", ov)
	}
	if s.PlaceView("pills", "plain-1") != nil {
		t.Fatal("plain thread should not receive manager header pills")
	}

	ref := &plugins.PlaceRef{ID: "pills", At: "thread.header", Key: "mgr-1", Context: mgr}
	// Open and close without editing: must not cache stale prompt when Overview later saves one.
	s.Dispatch(plugins.UIEvent{Type: "click", Handler: "sweep.toggle", Node: "sweep", Place: ref}, nil)
	a.until("sweep popover open", func() bool {
		n := s.PlaceView("pills", "mgr-1")
		return n != nil && (&widget.View{Body: n}).Find("sweep").Open
	})
	s.Dispatch(plugins.UIEvent{Type: "click", Handler: "sweep.close", Node: "sweep", Place: ref}, nil)
	a.until("sweep popover closed", func() bool {
		n := s.PlaceView("pills", "mgr-1")
		return n != nil && !(&widget.View{Body: n}).Find("sweep").Open
	})

	updatedMgr := map[string]any{
		"id":      "mgr-1",
		"role":    "manager",
		"sweep":   map[string]any{"intervalSeconds": 1800, "prompt": "Saved in Overview"},
		"manager": map[string]any{"sweepTiming": "Next in 30m", "needs": 2},
	}
	s.SetItems("pills", []plugins.PlaceItem{{Key: "mgr-1", Version: "2", Context: updatedMgr}})
	refUpdated := &plugins.PlaceRef{ID: "pills", At: "thread.header", Key: "mgr-1", Context: updatedMgr}
	s.Dispatch(plugins.UIEvent{Type: "click", Handler: "sweep.toggle", Node: "sweep", Place: refUpdated}, nil)
	a.until("reopened with Overview prompt", func() bool {
		n := s.PlaceView("pills", "mgr-1")
		if n == nil {
			return false
		}
		v := &widget.View{Body: n}
		sw := v.Find("sweep")
		if sw == nil || !sw.Open || sw.Popover == nil {
			return false
		}
		pv := &widget.View{Body: sw.Popover}
		inp, save := pv.Find("sweep-prompt"), pv.Find("sweep-save")
		return inp != nil && string(inp.Value) == `"Saved in Overview"` && save != nil && save.Disabled
	})

	// Selecting 1h then immediately saving a prompt composes with 3600 instead of reverting to 1800.
	s.Dispatch(plugins.UIEvent{Type: "click", Handler: "sweep.cadence", Node: "cadence-3600", Payload: json.RawMessage(`3600`), Place: refUpdated}, nil)
	a.until("cadence updated", func() bool { return len(configured) == 1 })
	if got := configured[0]; got.Thread != "mgr-1" || got.Sweep == nil || got.Sweep.IntervalSeconds != 3600 || got.Sweep.Prompt != "Saved in Overview" {
		t.Fatalf("cadence configure = %+v (sweep=%+v), want 3600 keeping prompt", got, got.Sweep)
	}

	s.Dispatch(plugins.UIEvent{Type: "change", Handler: "sweep.draft", Node: "sweep-prompt", Value: "  Only urgent blockers  ", Place: refUpdated}, nil)
	s.Dispatch(plugins.UIEvent{Type: "click", Handler: "sweep.save", Node: "sweep-save", Place: refUpdated}, nil)
	a.until("prompt saved", func() bool { return len(configured) == 2 })
	if got := configured[1]; got.Sweep == nil || got.Sweep.IntervalSeconds != 3600 || got.Sweep.Prompt != "Only urgent blockers" {
		t.Fatalf("save configure = %+v (sweep=%+v), want 3600 + trimmed prompt", got, got.Sweep)
	}

	// place.hidden closes an open popover so widening does not reopen it.
	s.SetItems("pills", nil)
	a.until("pills hidden", func() bool { return s.PlaceView("pills", "mgr-1") == nil && a.h.Idle() })
	s.SetItems("pills", []plugins.PlaceItem{{Key: "mgr-1", Version: "3", Context: mgr}})
	a.until("popover stays closed after hide/show", func() bool {
		n := s.PlaceView("pills", "mgr-1")
		return n != nil && !(&widget.View{Body: n}).Find("sweep").Open
	})

	s.Dispatch(plugins.UIEvent{Type: "click", Handler: "sweep.toggle", Node: "sweep", Place: ref}, nil)
	a.until("popover opened for run", func() bool {
		n := s.PlaceView("pills", "mgr-1")
		return n != nil && (&widget.View{Body: n}).Find("sweep").Open
	})
	s.Dispatch(plugins.UIEvent{Type: "click", Handler: "sweep.run", Node: "sweep-run", Place: ref}, nil)
	a.until("sweepNow called and popover closed", func() bool {
		n := s.PlaceView("pills", "mgr-1")
		return len(swept) == 1 && n != nil && !(&widget.View{Body: n}).Find("sweep").Open
	})
	if swept[0] != "mgr-1" {
		t.Fatalf("swept = %q, want [mgr-1]", swept)
	}

	s.Dispatch(plugins.UIEvent{Type: "click", Handler: "overview.toggle", Node: "overview", Place: ref}, nil)
	a.until("overview toggled", func() bool { return len(toggled) == 1 })
	if toggled[0] != "overview" {
		t.Fatalf("toggled = %q, want [overview]", toggled)
	}

	// Oversized persisted prompt (> 32 KiB) must not fail widget validation closed or open.
	huge := map[string]any{
		"id":      "mgr-huge",
		"role":    "manager",
		"sweep":   map[string]any{"intervalSeconds": 1800, "prompt": strings.Repeat("x", 33000)},
		"manager": map[string]any{"sweepTiming": "Next in 30m"},
	}
	s.SetItems("pills", []plugins.PlaceItem{{Key: "mgr-huge", Version: "1", Context: huge}})
	a.until("huge prompt header renders", func() bool { return s.PlaceView("pills", "mgr-huge") != nil })
	refHuge := &plugins.PlaceRef{ID: "pills", At: "thread.header", Key: "mgr-huge", Context: huge}
	s.Dispatch(plugins.UIEvent{Type: "click", Handler: "sweep.toggle", Node: "sweep", Place: refHuge}, nil)
	a.until("huge prompt popover opens without error", func() bool {
		n := s.PlaceView("pills", "mgr-huge")
		return n != nil && (&widget.View{Body: n}).Find("sweep").Open
	})
	if s.Err != nil {
		t.Fatalf("huge prompt produced session error: %v", s.Err)
	}

	// Oversized paste into sweep.draft is clamped so the editor stays usable even with empty saved prompt.
	emptyMgr := map[string]any{
		"id":      "mgr-empty",
		"role":    "manager",
		"sweep":   map[string]any{"intervalSeconds": 1800},
		"manager": map[string]any{"sweepTiming": "Next in 30m"},
	}
	s.SetItems("pills", []plugins.PlaceItem{{Key: "mgr-empty", Version: "1", Context: emptyMgr}})
	a.until("empty mgr header", func() bool { return s.PlaceView("pills", "mgr-empty") != nil })
	refEmpty := &plugins.PlaceRef{ID: "pills", At: "thread.header", Key: "mgr-empty", Context: emptyMgr}
	s.Dispatch(plugins.UIEvent{Type: "click", Handler: "sweep.toggle", Node: "sweep", Place: refEmpty}, nil)
	s.Dispatch(plugins.UIEvent{Type: "change", Handler: "sweep.draft", Node: "sweep-prompt", Value: strings.Repeat("a", 33000), Place: refEmpty}, nil)
	a.until("clamped draft keeps input editable", func() bool {
		n := s.PlaceView("pills", "mgr-empty")
		if n == nil {
			return false
		}
		sw := (&widget.View{Body: n}).Find("sweep")
		if sw == nil || !sw.Open || sw.Popover == nil {
			return false
		}
		pv := &widget.View{Body: sw.Popover}
		inp, save := pv.Find("sweep-prompt"), pv.Find("sweep-save")
		return inp != nil && len(inp.Value) == 32002 && save != nil && !save.Disabled
	})

	// Pending prompt reset clears once host confirms it, so a later prompt from another client is preserved.
	withOldPrompt := map[string]any{
		"id":      "mgr-reset",
		"role":    "manager",
		"sweep":   map[string]any{"intervalSeconds": 1800, "prompt": "Old prompt"},
		"manager": map[string]any{"sweepTiming": "Next in 30m"},
	}
	s.SetItems("pills", []plugins.PlaceItem{{Key: "mgr-reset", Version: "1", Context: withOldPrompt}})
	a.until("mgr-reset shown", func() bool { return s.Answered("pills", "mgr-reset") == "1" && a.h.Idle() })
	refReset := &plugins.PlaceRef{ID: "pills", At: "thread.header", Key: "mgr-reset", Context: withOldPrompt}
	s.Dispatch(plugins.UIEvent{Type: "click", Handler: "sweep.toggle", Node: "sweep", Place: refReset}, nil)
	s.Dispatch(plugins.UIEvent{Type: "click", Handler: "sweep.reset", Node: "sweep-reset", Place: refReset}, nil)
	a.until("reset sent", func() bool { return len(configured) == 3 })
	confirmedReset := map[string]any{
		"id":      "mgr-reset",
		"role":    "manager",
		"sweep":   map[string]any{"intervalSeconds": 1800},
		"manager": map[string]any{"sweepTiming": "Next in 30m"},
	}
	s.SetItems("pills", []plugins.PlaceItem{{Key: "mgr-reset", Version: "2", Context: confirmedReset}})
	a.until("reset confirmed", func() bool { return s.Answered("pills", "mgr-reset") == "2" && a.h.Idle() })
	otherClientMgr := map[string]any{
		"id":      "mgr-reset",
		"role":    "manager",
		"sweep":   map[string]any{"intervalSeconds": 1800, "prompt": "From another client"},
		"manager": map[string]any{"sweepTiming": "Next in 30m"},
	}
	s.SetItems("pills", []plugins.PlaceItem{{Key: "mgr-reset", Version: "3", Context: otherClientMgr}})
	a.until("other client prompt shown", func() bool { return s.Answered("pills", "mgr-reset") == "3" && a.h.Idle() })
	refOther := &plugins.PlaceRef{ID: "pills", At: "thread.header", Key: "mgr-reset", Context: otherClientMgr}
	s.Dispatch(plugins.UIEvent{Type: "click", Handler: "sweep.cadence", Node: "cadence-3600", Payload: json.RawMessage(`3600`), Place: refOther}, nil)
	a.until("cadence preserves newer client prompt", func() bool { return len(configured) == 4 })
	if got := configured[3]; got.Sweep == nil || got.Sweep.IntervalSeconds != 3600 || got.Sweep.Prompt != "From another client" {
		t.Fatalf("cadence after confirmed reset = %+v (sweep=%+v), want newer prompt preserved", got, got.Sweep)
	}

	s.SetItems("pills", []plugins.PlaceItem{
		{Key: "mgr-1", Version: "4", Context: mgr, Compact: true},
	})
	a.until("compact sweep button", func() bool {
		n := s.PlaceView("pills", "mgr-1")
		return n != nil && n.Type == "button" && n.Icon == "clock-circle" && n.Label == "Sweep 30m"
	})
}
