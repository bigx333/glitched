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

func TestARenamedBuiltInMergesRevokedGrants(t *testing.T) {
	state := t.TempDir()
	bundled, release := checklistBuiltIn(t)
	writePluginPrefs(t, state, prefs{Revoked: map[string][]string{
		"plan":      {"ui.replace"},
		"checklist": {"messages:read"},
	}})
	openBuiltIn(t, state, bundled, release)

	p := readPluginPrefs(t, state)
	if !slices.Contains(p.Revoked["checklist"], "ui.replace") || !slices.Contains(p.Revoked["checklist"], "messages:read") {
		t.Fatalf("revoked grants after the migration: %+v", p.Revoked)
	}
	if _, ok := p.Revoked["plan"]; ok {
		t.Fatalf("the old id's revoked grants stayed: %+v", p.Revoked)
	}
}

func TestARenamedBuiltInLeavesACopysOwnChoices(t *testing.T) {
	state := t.TempDir()
	bundled, release := checklistBuiltIn(t)
	// As a reverted copy leaves them: grants and settings under plan were
	// never the built-in's, so the migration keeps them where a copy restored
	// from .reverted finds them.
	writePluginPrefs(t, state, prefs{
		Grants:   map[string][]string{"plan": {"ui.replace"}},
		Settings: map[string]map[string]json.RawMessage{"plan": {"setting": json.RawMessage(`"value"`)}},
	})
	openBuiltIn(t, state, bundled, release)

	p := readPluginPrefs(t, state)
	if !slices.Contains(p.Grants["plan"], "ui.replace") || len(p.Settings["plan"]) != 1 {
		t.Fatalf("a copy's own choices moved: %+v", p)
	}
	if _, ok := p.Grants["checklist"]; ok {
		t.Fatalf("a copy's grants moved: %+v", p.Grants)
	}
	if _, ok := p.Settings["checklist"]; ok {
		t.Fatalf("a copy's settings moved: %+v", p.Settings)
	}
}

func TestARenamedBuiltInsSwitchDoesntMoveToAPluginOfItsNewID(t *testing.T) {
	state := t.TempDir()
	bundled, release := checklistBuiltIn(t)
	dst := filepath.Join(Dir(state), "checklist")
	writeReplacer(t, dst, replacerManifest("checklist", "thread.header.actions", "2"), actionsModule)
	signAsOwn(t, state, dst)
	writePluginPrefs(t, state, prefs{Disabled: []string{"plan"}})
	openBuiltIn(t, state, bundled, release)

	p := readPluginPrefs(t, state)
	if !slices.Contains(p.Disabled, "plan") || slices.Contains(p.Disabled, "checklist") {
		t.Fatalf("the off switch moved onto the installed plugin: %v", p.Disabled)
	}
}

func TestRevertOfARenamedBuiltInKeepsAnInstalledCopysSwitch(t *testing.T) {
	state := t.TempDir()
	bundled, release := checklistBuiltIn(t)
	for _, id := range []string{"checklist", "plan"} {
		dst := filepath.Join(Dir(state), id)
		writeReplacer(t, dst, replacerManifest(id, "thread.header.actions", "2"), actionsModule)
		signAsOwn(t, state, dst)
	}
	writePluginPrefs(t, state, prefs{Disabled: []string{"plan"}})
	h := openBuiltIn(t, state, bundled, release)

	if err := h.Revert("checklist"); err != nil {
		t.Fatal(err)
	}
	if p := readPluginPrefs(t, state); !slices.Contains(p.Disabled, "plan") {
		t.Fatalf("revert turned the installed copy back on: %v", p.Disabled)
	}
	if p := installedAs(h, filepath.Join(Dir(state), "plan")); p.Enabled {
		t.Fatalf("the installed copy is on after the revert: %+v", p)
	}
}

func TestRevertOfARenamedBuiltInClearsItsOldSwitch(t *testing.T) {
	state := t.TempDir()
	bundled, release := checklistBuiltIn(t)
	dst := filepath.Join(Dir(state), "checklist")
	writeReplacer(t, dst, replacerManifest("checklist", "thread.header.actions", "2"), actionsModule)
	signAsOwn(t, state, dst)
	writePluginPrefs(t, state, prefs{Disabled: []string{"plan"}})
	h := openBuiltIn(t, state, bundled, release)

	if err := h.Revert("checklist"); err != nil {
		t.Fatal(err)
	}
	if p := readPluginPrefs(t, state); slices.Contains(p.Disabled, "plan") || slices.Contains(p.Disabled, "checklist") {
		t.Fatalf("revert left the built-in's old off switch: %v", p.Disabled)
	}
	h.Close()
	h2 := openBuiltIn(t, state, bundled, release)
	if b := installedAs(h2, filepath.Join(BundledDir(state), "checklist")); !b.Enabled {
		t.Fatalf("after the restart, the reverted built-in is off: %+v", b)
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
