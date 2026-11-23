package main

import (
	"encoding/json"
	"image/png"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/daemon"
	"glitched/internal/plugins"
	"glitched/internal/plugins/bundled"
	"glitched/internal/plugins/pack"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
)

func planOf(statuses ...api.PlanStatus) provider.Plan {
	texts := []string{"Read the code", "Fix the bug", "Run the tests"}
	p := provider.Plan{}
	for i, s := range statuses {
		p.Steps = append(p.Steps, api.PlanStep{Text: texts[i], Status: s})
	}
	return p
}

// planned is a thread whose one turn played script, shown once it ended,
// with the built-ins settled. With PLANSHOTS set, the window is written
// there as name.png.
func planned(t *testing.T, name string, script ...provider.Event) *harness {
	t.Helper()
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(script))
	seed(t, d, "/work/glitched", "Fix the parser", "Fix the parser bug.")
	h := start(t, dir)
	h.a.select_(h.thread("Fix the parser"))
	h.until("the turn's end", func() bool {
		th := h.a.current()
		return th.Transcript != nil && th.View.LastTurn != nil && !th.View.Live()
	})
	settlePlugins(t, h.a, h.tt, h.p)
	h.tt.Advance(2 * time.Second)
	planShot(t, h, name)
	return h
}

func planShot(t *testing.T, h *harness, name string) {
	t.Helper()
	out := os.Getenv("PLANSHOTS")
	if out == "" {
		return
	}
	f, err := os.Create(filepath.Join(out, name+".png"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	png.Encode(f, h.tt.Image())
}

// The agent's plan shows twice: in the header, how many of its steps are
// done and the one it is at, with every step on hover; and in the
// timeline, a checklist where the run first said one, which its later
// plans replace.
func TestTheThreadShowsItsAgentsPlan(t *testing.T) {
	h := planned(t, "plan",
		planOf(api.PlanActive, api.PlanPending, api.PlanPending),
		provider.TextDelta{Text: "Reading the parser."},
		planOf(api.PlanDone, api.PlanActive, api.PlanPending),
	)
	if by := drawnBy(h, "thread.header.plan"); by != "plan" {
		t.Fatalf("the plan's pill is drawn by %s, want the built-in Plan", by)
	}
	for _, want := range []string{"1 of 3 · Fix the bug", "Plan", "1 of 3 done", "Read the code", "Fix the bug", "Run the tests"} {
		if !h.tt.HasText(want) {
			t.Errorf("the thread doesn't show %q: %q", want, h.tt.Texts())
		}
	}
	if n := len(entriesOf(h.a.current())); n != 3 {
		t.Errorf("the timeline has %d rows, want the message, one plan and the reply: %s", n, texts(entriesOf(h.a.current())))
	}
	texts := hover(t, h, "1 of 3 · Fix the bug")
	planShot(t, h, "plan-hover")
	for _, want := range []string{"Plan, 1 of 3 done", "✓ Read the code", "→ Fix the bug", "○ Run the tests"} {
		if !has(texts, want) {
			t.Errorf("the pill's tooltip doesn't say %q: %q", want, texts)
		}
	}
}

// A plan the agent clears leaves the header, and the built-in hears its
// copy went.
func TestAClearedPlanLeavesThePluginsHeader(t *testing.T) {
	h := planned(t, "plan-cleared", planOf(api.PlanActive, api.PlanPending))
	if by := drawnBy(h, "thread.header.plan"); by != "plan" {
		t.Fatalf("the plan's pill is drawn by %s, want the built-in Plan", by)
	}
	th := h.a.current()
	th.Transcript.Plan = nil
	settlePlugins(t, h.a, h.tt, h.p)
	if s := h.a.plugins.App("plan"); s.PlaceView("plan", th.id().String()) != nil || drawnBy(h, "thread.header.plan") != "none" {
		t.Errorf("the built-in still draws the cleared plan, drawn by %s: %q", drawnBy(h, "thread.header.plan"), h.tt.Texts())
	}
}

// A plan at a long step gives the header's controls their room: its pill
// keeps to its count, then goes, before any control is cut off; wide
// again, it says its step.
func TestALongPlanGivesTheHeaderWay(t *testing.T) {
	const title = "Header fixture for the narrow window"
	label := "0 of 1 · " + strings.Repeat("全", 40)
	h := narrowHeader(t, title, false, nil, provider.Plan{Steps: []api.PlanStep{{Text: strings.Repeat("全", 40), Status: api.PlanActive}}})
	h.at(1600)
	if _, ok := h.inHeader(label); !ok {
		t.Fatalf("at 1600 points the header doesn't say the plan's step: %q", h.tt.Texts())
	}
	for _, w := range []int{1100, 900, 760, 700, 600} {
		h.at(w)
		h.fitsHeader(w, append([]string{title, "Pull request #12"}, headerControls...)...)
		if w == 760 {
			planShot(t, h, "plan-long-760")
		}
		if _, ok := h.inHeader("0/1"); !ok {
			t.Logf("at %d points the plan's pill went: %q", w, h.tt.Texts())
		}
	}
	h.at(1600)
	if _, ok := h.inHeader(label); !ok {
		t.Errorf("wide again, the plan's pill keeps to its count: %q", h.tt.Texts())
	}
}

// A run that ends with every step of its plan done takes the plan out of
// the header; its checklist stays in the timeline.
func TestADonePlanLeavesTheHeader(t *testing.T) {
	h := planned(t, "plan-done",
		planOf(api.PlanActive, api.PlanPending, api.PlanPending),
		planOf(api.PlanDone, api.PlanDone, api.PlanDone),
		provider.TextDelta{Text: "All done."},
	)
	if slices.Contains(h.tt.Texts(), "3 of 3") || drawnBy(h, "thread.header.plan") != "none" {
		t.Errorf("a done plan still shows in the header: %q", h.tt.Texts())
	}
	if !h.tt.HasText("3 of 3 done") {
		t.Errorf("the timeline's checklist is gone: %q", h.tt.Texts())
	}
}

// The built-in Plan's main.js and its native copy draw the same pill,
// node for node, whatever the plan.
func TestTheBuiltInPlanIsItsNativeCopy(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "plan")
	src, err := fs.Sub(bundled.FS, "plan")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(folder, src); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(folder, "signature.json"))
	key, err := pack.LoadKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := pack.Sign(folder, key); err != nil {
		t.Fatal(err)
	}
	q := make(chan func(), 1024)
	h := plugins.Open(t.TempDir(), plugins.Options{Post: func(fn func()) { q <- fn }, Bundled: os.DirFS(root), ReleaseKeys: []string{key.Public()}})
	t.Cleanup(h.Close)
	s := h.App("plan")
	if s == nil {
		t.Fatalf("the built-in didn't load: %v", h.Failed("plan"))
	}
	step := func(text, status string) headerPlanStep { return headerPlanStep{Text: text, Status: status} }
	for i, p := range []headerPlan{
		{Steps: []headerPlanStep{step("Read the code", "active"), step("Fix the bug", "pending")}},
		{Steps: []headerPlanStep{step("Read the code", "done"), step("Fix the bug", "pending"), step("Run the tests", "active")}},
		{Narrow: true, Steps: []headerPlanStep{step("Read the code", "done"), step("Fix the bug", "active")}},
		{Steps: []headerPlanStep{step("Read", "done"), step("Fix", "done"), step("Test", "done")}},
		{Steps: []headerPlanStep{step("a", "done"), step("b", "pending"), step("c", "pending")}},
		{Steps: []headerPlanStep{step("Rewrite the parser so that\n\tit  reads every token of the stream once, and no more", "active")}},
		{Steps: []headerPlanStep{step("Café ✓ 日本語のテキストを四十文字より長くするための余分な文字列です、まだまだ続きます", "pending")}},
		{Steps: []headerPlanStep{step("Wait for review", "blocked"), step("Inherit", "constructor"), step("Prototype", "__proto__")}},
	} {
		key := string(rune('a' + i))
		s.SetItems("plan", []plugins.PlaceItem{{Key: key, Version: key, Context: p}})
		deadline := time.After(5 * time.Second)
		for s.PlaceView("plan", key) == nil && s.Err == nil {
			select {
			case fn := <-q:
				fn()
			case <-deadline:
				t.Fatalf("the built-in drew nothing for %+v: %v", p, s.Err)
			}
		}
		if got, want := s.PlaceView("plan", key), planBadge(p); !reflect.DeepEqual(got, want) {
			g, _ := json.Marshal(got)
			w, _ := json.Marshal(want)
			t.Errorf("for %+v main.js drew %s, its native copy %s", p, g, w)
		}
	}
}
