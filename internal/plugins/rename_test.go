package plugins

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"glitched/internal/plugins/pack"
)

func checklistBuiltIn(t *testing.T) (fs.FS, string) {
	t.Helper()
	root := t.TempDir()
	key, err := pack.LoadKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(root, "checklist")
	writeReplacer(t, dst, replacerManifest("checklist", "thread.header.actions", "2"), actionsModule)
	if err := pack.Sign(dst, key); err != nil {
		t.Fatal(err)
	}
	return os.DirFS(root), key.Public()
}

func writePluginPrefs(t *testing.T, state string, p prefs) {
	t.Helper()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(prefsPath(state), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readPluginPrefs(t *testing.T, state string) prefs {
	t.Helper()
	b, err := os.ReadFile(prefsPath(state))
	if err != nil {
		t.Fatal(err)
	}
	var p prefs
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestARenamedBuiltInKeepsItsOwnChoices(t *testing.T) {
	state := t.TempDir()
	bundled, release := checklistBuiltIn(t)
	writePluginPrefs(t, state, prefs{
		Disabled: []string{"plan"},
		Revoked:  map[string][]string{"plan": {"ui.replace"}},
	})
	h := openBuiltIn(t, state, bundled, release)

	if b := installedAs(h, filepath.Join(BundledDir(state), "checklist")); b.Enabled {
		t.Fatalf("the renamed built-in lost its off switch: %+v", b)
	}
	p := readPluginPrefs(t, state)
	if slices.Contains(p.Disabled, "plan") || !slices.Contains(p.Disabled, "checklist") {
		t.Fatalf("disabled plugins after the migration: %v", p.Disabled)
	}
	if !slices.Contains(p.Revoked["checklist"], "ui.replace") {
		t.Fatalf("revoked grants after the migration: %+v", p.Revoked)
	}
	if _, ok := p.Revoked["plan"]; ok {
		t.Fatalf("the old id's revoked grants stayed: %+v", p.Revoked)
	}
}

func TestACopyOfARenamedBuiltInStillShadowsItAndReverts(t *testing.T) {
	state := t.TempDir()
	bundled, release := checklistBuiltIn(t)
	dst := filepath.Join(Dir(state), "plan")
	writeReplacer(t, dst, replacerManifest("plan", "thread.header.actions", "2"), actionsModule)
	signAsOwn(t, state, dst)
	writePluginPrefs(t, state, prefs{Grants: map[string][]string{"plan": {"ui.replace"}}})
	h := openBuiltIn(t, state, bundled, release)

	mine := installedAs(h, dst)
	if mine.ID != "plan" || !mine.Shadows || mine.Err != nil {
		t.Fatalf("the copy made before the rename: %+v", mine)
	}
	if b := installedAs(h, filepath.Join(BundledDir(state), "checklist")); b.ShadowedBy != dst || b.Enabled {
		t.Fatalf("the renamed built-in beside the copy: %+v", b)
	}
	if got := replacers(h); !slices.Equal(got, []string{"plan", "checklist" + FallbackSuffix}) {
		t.Fatalf("%v draw the header; the copy, then its built-in fallback, should", got)
	}
	if p := readPluginPrefs(t, state); !slices.Contains(p.Grants["plan"], "ui.replace") {
		t.Fatalf("the copy's grants moved: %+v", p.Grants)
	} else if _, ok := p.Grants["checklist"]; ok {
		t.Fatalf("the copy's grants moved: %+v", p.Grants)
	}

	if err := h.Revert("plan"); err != nil {
		t.Fatal(err)
	}
	if in := h.running1("checklist"); in == nil || !in.bundled {
		t.Fatal("revert didn't bring the renamed built-in back")
	}
	kept, _ := filepath.Glob(filepath.Join(revertedDir(state), "checklist-*", "index.js"))
	if len(kept) != 1 {
		t.Fatalf("the copy's files went: %v", kept)
	}
}
