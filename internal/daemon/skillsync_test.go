package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"glitched/internal/api"
	"glitched/internal/skills"
	"glitched/internal/skills/skillstest"
)

// A paired client gives an engine skills over the tailnet, as a GUI syncs
// its machine's, and the engine takes only a skill's own folder of plain
// files whose hashes are its own: a hostile one is refused and changes
// nothing its threads get.
func TestAPairedEngineTakesOnlySoundSkills(t *testing.T) {
	ctx := context.Background()
	dir, src := filepath.Join(stateDir(t), "state"), filepath.Join(stateDir(t), "state")
	skillstest.Write(t, filepath.Join(src, "skills"), "deploy", "The laptop's deploy.")
	token, err := Pair(dir, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	d := start(t, Config{Dir: dir, Listen: []string{"127.0.0.1:0"}})
	c := remote(t, d, token)
	thread := api.NewID()
	if _, err := c.Submit(ctx, api.Command{ID: api.NewID(), Target: thread, Body: api.ThreadCreate{Workspace: stateDir(t), Title: "t", Model: api.ModelRef{Provider: "fake", Model: "fake"}}}); err != nil {
		t.Fatal(err)
	}
	laptop := api.NewID()
	if _, err := skills.Push(ctx, src, laptop, "laptop", c); err != nil {
		t.Fatal(err)
	}
	before, err := c.Skills(ctx, api.SkillQuery{Thread: thread})
	if err != nil || len(before.Skills) != 1 || before.Skills[0].Source != "synced" || before.Skills[0].From != "laptop" || before.Synced == nil {
		t.Fatalf("after a sync the engine says %+v, %v", before, err)
	}

	file := func(path, mode string, data []byte) api.SkillContent {
		return api.SkillContent{Path: path, Mode: mode, Data: data}
	}
	md := func(name string) api.SkillContent {
		return file("SKILL.md", "644", []byte("---\nname: "+name+"\ndescription: d\n---\n"))
	}
	hostile := map[string]api.SkillPut{
		"its files don't hash to its tree": {Name: "evil", Tree: strings.Repeat("ab", 32), Files: []api.SkillContent{md("evil")}},
		"isn't a path in a skill's folder": tree("evil", md("evil"), file("../../escape", "644", []byte("x"))),
		"a synced skill holds only files":  tree("evil", md("evil"), file("link", "120000", []byte("/etc/passwd"))),
		"holds more than 8 MiB":            tree("evil", md("evil"), file("big", "644", make([]byte, 9<<20))),
		"isn't a skill name":               tree("Evil--One", md("Evil--One")),
		"its tree isn't a hash":            {Name: "evil", Tree: "../../../tmp", Files: []api.SkillContent{md("evil")}},
	}
	if err := c.PutSkill(ctx, tree("fine", md("fine"))); err != nil {
		t.Fatalf("the engine refused a sound skill: %v", err)
	}
	for why, p := range hostile {
		if err := c.PutSkill(ctx, p); err == nil || !strings.Contains(err.Error(), why) {
			t.Errorf("a put of %s was answered %v, want that it %s", p.Name, err, why)
		}
		m := api.SkillManifest{Skills: []api.SkillTree{{Name: p.Name, Tree: p.Tree}}}
		for _, f := range p.Files {
			sum := sha256.Sum256(f.Data)
			m.Skills[0].Files = append(m.Skills[0].Files, api.SkillFile{Path: f.Path, Mode: f.Mode, Size: int64(len(f.Data)), SHA256: hex.EncodeToString(sum[:])})
		}
		set := sha256.Sum256([]byte(p.Name + "\x00" + p.Tree + "\n"))
		m.Set = hex.EncodeToString(set[:16])
		if got, err := c.SyncSkills(ctx, api.SkillSync{Source: laptop, Name: "laptop", Manifest: m}); err == nil || !strings.Contains(err.Error(), why) {
			t.Errorf("a set of %s was answered %+v, %v, want that it %s", p.Name, got, err, why)
		}
	}
	after, err := c.Skills(ctx, api.SkillQuery{Thread: thread})
	if err != nil || !slices.Equal(after.Skills, before.Skills) || *after.Synced != *before.Synced {
		t.Errorf("after hostile offers the engine says %+v, %v; was %+v", after, err, before)
	}
}

// tree is the SkillPut of name with files, its tree their own hash.
func tree(name string, files ...api.SkillContent) api.SkillPut {
	h := sha256.New()
	slices.SortFunc(files, func(a, b api.SkillContent) int { return strings.Compare(a.Path, b.Path) })
	for _, f := range files {
		sum := sha256.Sum256(f.Data)
		h.Write([]byte(f.Path + "\x00" + f.Mode + "\x00" + strconv.Itoa(len(f.Data)) + "\x00" + hex.EncodeToString(sum[:]) + "\n"))
	}
	return api.SkillPut{Name: name, Tree: hex.EncodeToString(h.Sum(nil)), Files: files}
}
