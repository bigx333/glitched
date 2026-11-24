package bundled_test

import (
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/plugins"
	"glitched/internal/plugins/widget"
)

// skillsHeader is the built-in Skills plugin on its own host, showing a
// thread's header whose record holds the skills its engine said.
type skillsHeader struct {
	t       *testing.T
	h       *plugins.Host
	s       *plugins.Session
	q       chan func()
	ctx     map[string]any
	version int
	compact bool
	effects []string // the app calls the plugin made, by type
}

// openSkills shows the plugin a thread's header whose engine said list,
// or nothing yet with nil.
func openSkills(t *testing.T, list *api.SkillList, compact bool) *skillsHeader {
	t.Helper()
	k := &skillsHeader{t: t, q: make(chan func(), 1024), compact: compact}
	fsys, release := signedCopy(t, "skills")
	k.h = plugins.Open(t.TempDir(), plugins.Options{
		Post:        func(fn func()) { k.q <- fn },
		Logf:        t.Logf,
		Bundled:     fsys,
		ReleaseKeys: []string{release},
		CallTimeout: callTimeout,
		Do: func(_ string, e plugins.Effect) (any, error) {
			k.effects = append(k.effects, e.Type)
			return nil, nil
		},
	})
	t.Cleanup(k.h.Close)
	k.s = k.h.App("skills")
	if k.s == nil {
		t.Fatalf("Skills doesn't run: %+v", k.h.Installed())
	}
	k.show(list)
	return k
}

// show has the header's record say list, as the engine's answer does.
func (k *skillsHeader) show(list *api.SkillList) {
	k.t.Helper()
	k.version++
	k.ctx = map[string]any{"id": "t1", "title": "Fix the flaky test", "workspace": "/work/widgets", "local": true}
	if list != nil {
		k.ctx["skills"] = list
	}
	k.s.SetItems("skills", []plugins.PlaceItem{{Key: "t1", Version: string(rune('0' + k.version)), Context: k.ctx, Compact: k.compact}})
	k.settle()
}

func (k *skillsHeader) settle() {
	k.t.Helper()
	deadline := time.After(30 * time.Second)
	want := string(rune('0' + k.version))
	for quiet := 0; quiet < 3; {
		select {
		case fn := <-k.q:
			fn()
			quiet = 0
		case <-time.After(20 * time.Millisecond):
			if k.h.Idle() && k.s.Answered("skills", "t1") == want {
				quiet++
			}
		case <-deadline:
			k.t.Fatalf("timed out; failed %v", k.h.Failed("skills"))
		}
	}
	if err := k.h.Failed("skills"); err != nil {
		k.t.Fatalf("the plugin failed: %v", err)
	}
	if len(k.effects) > 0 {
		k.t.Fatalf("the plugin asked the app for %q: what it draws is in the header's record", k.effects)
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

var clash = &api.SkillList{
	Skills: []api.Skill{
		{Name: "deploy", Description: "Ship the app to staging.", Source: "home", Shown: "~/.glitched/skills/deploy"},
		{Name: "review", Description: "Review the diff as this project does.", Source: "project", Shown: "widgets/.glitched/skills/review", Replaces: "home"},
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
	for _, want := range []string{"review|", "Project|", "replaces yours|", "widgets/.glitched/skills/review|"} {
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

// A thread whose engine hasn't said, or says it has no skills, shows no
// pill.
func TestNoSkillsShowNoPill(t *testing.T) {
	k := openSkills(t, nil, false)
	if k.view() != nil || !k.s.AnsweredNull("skills", "t1") {
		t.Errorf("a thread whose engine hasn't said: %q", k.text())
	}
	k.show(&api.SkillList{Skills: []api.Skill{}})
	if k.view() != nil || !k.s.AnsweredNull("skills", "t1") {
		t.Errorf("a thread with no skills: %q", k.text())
	}
}

// A skill no agent gets tints the pill and says why, under Skipped, even
// where it is the thread's only one and the answer's skills are null.
func TestASkippedSkillTintsThePillAndSaysWhy(t *testing.T) {
	k := openSkills(t, &api.SkillList{
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

// The pill follows the header's record as the engine says more.
func TestThePillFollowsWhatTheEngineSays(t *testing.T) {
	k := openSkills(t, &api.SkillList{Skills: clash.Skills[:1], Delivery: clash.Delivery}, false)
	if v := k.view(); v == nil || v.Body.Label != "Skills 1" {
		t.Fatalf("the pill: %q", k.text())
	}
	k.show(clash)
	if v := k.view(); v == nil || v.Body.Label != "Skills 2" {
		t.Errorf("after the engine said more: %q", k.text())
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

// A synced skill says the machine it came from and what it replaces, a
// project's that replaces it names that machine, and the popover says how
// long ago that machine last synced them.
func TestSyncedSkillsSayTheirMachineWhatTheyReplaceAndHowOld(t *testing.T) {
	k := openSkills(t, &api.SkillList{
		Skills: []api.Skill{
			{Name: "deploy", Description: "The project's deploy.", Source: "project", Shown: "widgets/.glitched/skills/deploy", Replaces: "synced"},
			{Name: "hello", Description: "Praetor's hello.", Source: "synced", Shown: "/state/skills-synced/sets/x/hello", From: "Praetor", Replaces: "home"},
			{Name: "notes", Description: "This machine's notes.", Source: "home", Shown: "~/.glitched/skills/notes"},
		},
		Synced: &api.SyncedFrom{Name: "Praetor", Pulled: time.Now().Add(-3*time.Hour - time.Minute)},
	}, false)
	k.click("popover.toggle")
	v := k.view()
	if s := v.Find("synced"); s == nil || s.Text != "Synced from Praetor 3h ago" {
		t.Errorf("the synced line: %+v", s)
	}
	rows := map[string][]string{
		"skill:synced:hello":   {"From Praetor|", "replaces yours|", "Praetor's hello.|"},
		"skill:project:deploy": {"Project|", "replaces Praetor's|"},
		"skill:home:notes":     {"Yours|", "~/.glitched/skills/notes|"},
	}
	for id, wants := range rows {
		row := v.Find(id)
		if row == nil {
			t.Fatalf("no row %s: %q", id, k.text())
		}
		for _, want := range wants {
			if !strings.Contains(words(row), want) {
				t.Errorf("%s doesn't say %q: %s", id, want, words(row))
			}
		}
	}
	if strings.Contains(words(v.Find("skill:synced:hello")), "skills-synced") {
		t.Errorf("the synced row shows where the engine keeps it: %s", words(v.Find("skill:synced:hello")))
	}
	if strings.Contains(words(v.Find("skill:home:notes")), "replaces") {
		t.Errorf("a skill that replaces none says it does: %s", words(v.Find("skill:home:notes")))
	}
}
