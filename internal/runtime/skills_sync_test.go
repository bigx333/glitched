package runtime

import (
	"context"
	"path/filepath"
	"testing"

	"glitched/internal/api"
	"glitched/internal/skills"
	"glitched/internal/skills/skillstest"
)

// A sync whose call ended before it reached the store takes nothing, as
// after its machine's drop: the drop isn't undone.
func TestACanceledSyncCommitsNothingAfterADrop(t *testing.T) {
	src, node := t.TempDir(), t.TempDir()
	skillstest.Write(t, filepath.Join(src, "skills"), "one", "First app's skill.")
	h := &Host{}
	WithSkills(node)(h)
	bg := context.Background()
	empty, err := skills.Exported(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	initial := api.SkillSync{Source: api.NewID(), Name: "Second app", Manifest: empty.Manifest}
	if _, err := h.SyncSkills(bg, initial); err != nil {
		t.Fatal(err)
	}
	e, err := skills.Exported(src)
	if err != nil {
		t.Fatal(err)
	}
	q := api.SkillSync{Source: api.NewID(), Name: "First app", Manifest: e.Manifest}
	f, err := e.Files("one")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.PutSkill(bg, api.SkillPut{Name: "one", Tree: e.Manifest.Skills[0].Tree, Files: f}); err != nil {
		t.Fatal(err)
	}
	if err := h.DropSkills(bg, api.SkillDrop{Source: q.Source}); err != nil {
		t.Fatal(err)
	}
	if got := skills.Find(node, "", "").Synced; got == nil || got.Source != initial.Source {
		t.Fatalf("drop should leave other app's current origin alone; got %+v", got)
	}
	ctx, cancel := context.WithCancel(bg)
	cancel()
	_, err = h.SyncSkills(ctx, q)
	got := skills.Find(node, "", "")
	if got.Synced != nil && got.Synced.Source == q.Source {
		t.Fatalf("canceled sync replaced second app after the first app's successful drop; skills=%d, error=%v", len(got.Skills), err)
	}
}
