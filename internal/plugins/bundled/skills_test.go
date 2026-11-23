package bundled_test

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/plugins"
	"glitched/internal/plugins/widget"
)

// skillsHeader is the built-in Skills plugin on its own host, showing a
// thread's header, whose engine is played by the test: it answers
// skills.list later, as the app does, with list or err.
type skillsHeader struct {
	t   *testing.T
	h   *plugins.Host
	s   *plugins.Session
	q   chan func()
	ctx map[string]any

	mu    sync.Mutex
	list  api.SkillList
	err   error
	asked []string // the threads skills.list was asked for
}

func openSkills(t *testing.T, list api.SkillList, compact bool) *skillsHeader {
	t.Helper()
	k := &skillsHeader{t: t, q: make(chan func(), 1024), list: list}
	fsys, release := signedCopy(t, "skills")
	k.h = plugins.Open(t.TempDir(), plugins.Options{
		Post:        func(fn func()) { k.q <- fn },
		Logf:        t.Logf,
		Bundled:     fsys,
		ReleaseKeys: []string{release},
		CallTimeout: callTimeout,
		Do:          k.do,
	})
	t.Cleanup(k.h.Close)
	k.s = k.h.App("skills")
	if k.s == nil {
		t.Fatalf("Skills doesn't run: %+v", k.h.Installed())
	}
	k.ctx = map[string]any{"id": "t1", "title": "Fix the flaky test", "workspace": t.TempDir(), "local": true}
	k.s.SetItems("skills", []plugins.PlaceItem{{Key: "t1", Version: "1", Context: k.ctx, Compact: compact}})
	k.settle()
	return k
}

func (k *skillsHeader) do(_ string, e plugins.Effect) (any, error) {
	if e.Type != "skills.list" {
		k.t.Errorf("the plugin asked the app for %s", e.Type)
		return nil, nil
	}
	var q struct{ Thread string }
	json.Unmarshal(e.Args, &q)
	k.mu.Lock()
	k.asked = append(k.asked, q.Thread)
	list, err := k.list, k.err
	k.mu.Unlock()
	return plugins.Pending(func(done func(any, error)) {
		k.q <- func() { done(list, err) }
	}), nil
}

func (k *skillsHeader) answer(list api.SkillList, err error) {
	k.mu.Lock()
	k.list, k.err = list, err
	k.mu.Unlock()
}

func (k *skillsHeader) settle() {
	k.t.Helper()
	deadline := time.After(30 * time.Second)
	for quiet := 0; quiet < 3; {
		select {
		case fn := <-k.q:
			fn()
			quiet = 0
		case <-time.After(20 * time.Millisecond):
			if k.h.Idle() && k.s.Answered("skills", "t1") != "" {
				quiet++
			}
		case <-deadline:
			k.t.Fatalf("timed out; failed %v", k.h.Failed("skills"))
		}
	}
	if err := k.h.Failed("skills"); err != nil {
		k.t.Fatalf("the plugin failed: %v", err)
	}
}

func (k *skillsHeader) view() *widget.View {
	if n := k.s.PlaceView("skills", "t1"); n != nil {
		return &widget.View{Body: n}
	}
	return nil
}

// words is every text n holds, its popover too.
func words(n *widget.Node) string {
	var b strings.Builder
	n.Walk(func(n *widget.Node) {
		if s := n.Text + n.Label; s != "" {
			b.WriteString(s + "|")
		}
	})
	return b.String()
}

func (k *skillsHeader) text() string {
	if v := k.view(); v != nil {
		return words(v.Body)
	}
	return ""
}

func (k *skillsHeader) click(handler string) {
	k.t.Helper()
	ref := &plugins.PlaceRef{ID: "skills", At: "thread.header", Key: "t1", Context: k.ctx}
	k.s.Dispatch(plugins.UIEvent{Type: "click", Handler: handler, Node: "skills", Place: ref}, nil)
	k.settle()
}

func (k *skillsHeader) focus() {
	k.t.Helper()
	k.s.Notify(plugins.AppEvent{Kind: "app", Type: "window.focused", Data: map[string]bool{"focused": true}})
	k.settle()
}

var clash = api.SkillList{
	Skills: []api.Skill{
		{Name: "deploy", Description: "Ship the app to staging.", Source: "home", Shown: "~/.glitched/skills/deploy"},
		{Name: "review", Description: "Review the diff as this project does.", Source: "project", Shown: ".glitched/skills/review", Overrides: true},
	},
	Delivery: "Claude Code: as /glitched:<name>",
}

// The pill counts the thread's skills, and its popover, shut till a click,
// says where each is from, which one replaces the user's own, and how the
// thread's agent gets them.
func TestTheSkillsPillCountsTheThreadsSkillsAndSaysWhereEachIsFrom(t *testing.T) {
	k := openSkills(t, clash, false)
	v := k.view()
	if v == nil || v.Body.Label != "Skills 2" || v.Body.Open || v.Body.Popover == nil {
		t.Fatalf("the pill: %q", k.text())
	}
	if c := v.Body.Children; len(c) != 2 || c[0].Name != "magic-stick-3" || c[0].Color != "$text.muted" || c[1].Text != "Skills 2" {
		t.Errorf("the pill's children: %+v", c)
	}
	if len(k.asked) != 1 || k.asked[0] != "t1" {
		t.Errorf("asked for the skills of %q", k.asked)
	}
	k.click("popover.toggle")
	v = k.view()
	if !v.Body.Open {
		t.Fatal("a click didn't open the popover")
	}
	deploy, review := v.Find("skill:home:deploy"), v.Find("skill:project:review")
	if deploy == nil || review == nil {
		t.Fatalf("the popover: %q", k.text())
	}
	for _, want := range []string{"deploy|", "Yours|", "Ship the app to staging.|", "~/.glitched/skills/deploy|"} {
		if !strings.Contains(words(deploy), want) {
			t.Errorf("deploy's row doesn't say %q: %s", want, words(deploy))
		}
	}
	if strings.Contains(words(deploy), "replaces yours") {
		t.Errorf("the user's own skill says it replaces theirs: %s", words(deploy))
	}
	for _, want := range []string{"review|", "Project|", "replaces yours|", ".glitched/skills/review|"} {
		if !strings.Contains(words(review), want) {
			t.Errorf("review's row doesn't say %q: %s", want, words(review))
		}
	}
	if d := v.Find("delivery"); d == nil || d.Text != "Claude Code: as /glitched:<name>" {
		t.Errorf("the delivery line: %+v", d)
	}
	k.click("popover.close")
	if k.view().Body.Open {
		t.Error("the popover stayed open")
	}
}

// none is the engine's answer for a thread with no skills.
var none = api.SkillList{Skills: []api.Skill{}}

// A thread with no skills, or whose engine can't say, as one too old for
// the call, shows no pill, and the plugin goes on.
func TestNoSkillsShowNoPill(t *testing.T) {
	k := openSkills(t, none, false)
	if k.view() != nil || !k.s.AnsweredNull("skills", "t1") {
		t.Errorf("a thread with no skills: %q", k.text())
	}
	k.answer(none, errors.New("the engine is too old to say"))
	k.focus()
	if k.view() != nil {
		t.Errorf("a thread whose engine can't say: %q", k.text())
	}
}

// A skill no agent gets tints the pill and says why, under Skipped, even
// where it is the thread's only one.
func TestASkippedSkillTintsThePillAndSaysWhy(t *testing.T) {
	k := openSkills(t, api.SkillList{
		Skills:   []api.Skill{},
		Skipped:  []api.SkippedSkill{{Source: "project", Name: "broken", Why: "SKILL.md has no description"}},
		Delivery: "Codex: as glitched:<name> in its skill list",
	}, false)
	v := k.view()
	if v == nil || v.Body.Label != "Skills 0" || v.Body.Children[0].Color != "$warning" || !strings.Contains(v.Body.Tooltip, "1 skipped") {
		t.Fatalf("the pill of a skipped skill: %q", k.text())
	}
	k.click("popover.toggle")
	text := k.text()
	for _, want := range []string{"None for this thread.|", "Skipped|", "broken|", "SKILL.md has no description|", "Project|"} {
		if !strings.Contains(text, want) {
			t.Errorf("the popover doesn't say %q: %s", want, text)
		}
	}
	if k.view().Find("skipped:project:broken") == nil {
		t.Errorf("no row for the skipped skill: %s", text)
	}
}

// Coming back to the window reads the skills again, as a skill added
// while away; a read that fails keeps what the pill showed.
func TestFocusingTheWindowReadsTheSkillsAgain(t *testing.T) {
	one := api.SkillList{Skills: clash.Skills[:1], Delivery: clash.Delivery}
	k := openSkills(t, one, false)
	if v := k.view(); v == nil || v.Body.Label != "Skills 1" {
		t.Fatalf("the pill: %q", k.text())
	}
	k.answer(clash, nil)
	k.focus()
	if v := k.view(); v == nil || v.Body.Label != "Skills 2" {
		t.Errorf("after the window came back: %q", k.text())
	}
	k.answer(none, errors.New("the engine is gone"))
	k.focus()
	if v := k.view(); v == nil || v.Body.Label != "Skills 2" {
		t.Errorf("after a read that failed: %q", k.text())
	}
	if len(k.asked) != 3 {
		t.Errorf("asked %d times, want once shown and once each focus", len(k.asked))
	}
}

// A narrow header keeps the pill to its icon, named by its label.
func TestACompactHeaderKeepsTheSkillsPillToItsIcon(t *testing.T) {
	k := openSkills(t, clash, true)
	v := k.view()
	if v == nil || v.Body.Label != "Skills 2" || len(v.Body.Children) != 1 || v.Body.Children[0].Name != "magic-stick-3" {
		t.Errorf("the compact pill: %q", k.text())
	}
}
