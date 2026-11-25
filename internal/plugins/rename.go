package plugins

import (
	"errors"
	"os"
	"path/filepath"
	"slices"

	plugin "github.com/yxxbc/mygo-plugin"

	"glitched/internal/jsonfile"
)

// renamedBuiltins are the ids built-in plugins had, by the id they have
// now. A plugin of the user's with the old id shadowed the built-in before
// its rename, so it still does: its files, grants and settings keep the id
// it had. The built-in's own choices move to its new id.
var renamedBuiltins = map[string]string{
	"checklist": "plan",
}

var errNoRenameMigration = errors.New("no renamed built-in preferences to migrate")

// migrateRenamedPrefs moves what the user chose for a renamed built-in to
// its new id. While one of the user's plugins shadows it under the old id,
// the old id's enabled state, grants, settings and consent are that copy's
// and stay; a revoked capability is the built-in's and moves either way.
func migrateRenamedPrefs(stateDir string, logf func(string, ...any)) {
	var current prefs
	if err := jsonfile.Load(prefsPath(stateDir), &current); err != nil {
		return // Open logs the file it can't read.
	}
	entries, _ := os.ReadDir(Dir(stateDir))
	shadows := shadowRenames(stateDir, entries)
	if !renameMigrationNeeded(current, shadows) {
		return
	}
	if _, err := jsonfile.Edit(prefsPath(stateDir), func(p *prefs) error {
		if !renameMigrationNeeded(*p, shadows) {
			return errNoRenameMigration
		}
		for newID, oldID := range renamedBuiltins {
			migrateRenamedPref(p, oldID, newID, shadows[newID] == oldID)
		}
		return nil
	}); err != nil && !errors.Is(err, errNoRenameMigration) {
		logf("plugins: migrating renamed built-ins: %v", err)
	}
}

func renameMigrationNeeded(p prefs, shadows map[string]string) bool {
	for newID, oldID := range renamedBuiltins {
		if shadows[newID] == oldID {
			_, ok := p.Revoked[oldID]
			if ok {
				return true
			}
		} else if hasPref(p, oldID) {
			return true
		}
	}
	return false
}

func hasPref(p prefs, id string) bool {
	if slices.Contains(p.Disabled, id) {
		return true
	}
	for _, m := range []map[string][]string{p.Grants, p.Revoked} {
		if _, ok := m[id]; ok {
			return true
		}
	}
	if _, ok := p.Settings[id]; ok {
		return true
	}
	_, ok := p.Consent[id]
	return ok
}

func migrateRenamedPref(p *prefs, oldID, newID string, copyShadows bool) {
	if !copyShadows {
		if slices.Contains(p.Disabled, oldID) {
			p.Disabled = slices.DeleteFunc(p.Disabled, func(id string) bool { return id == oldID })
			if !slices.Contains(p.Disabled, newID) {
				p.Disabled = append(p.Disabled, newID)
			}
		}
		movePref(p.Grants, oldID, newID)
		movePref(p.Settings, oldID, newID)
		movePref(p.Consent, oldID, newID)
	}
	movePref(p.Revoked, oldID, newID)
}

func movePref[V any](m map[string]V, oldID, newID string) {
	v, ok := m[oldID]
	if !ok {
		return
	}
	if _, ok := m[newID]; !ok {
		m[newID] = v
	}
	delete(m, oldID)
}

// shadowRenames are the old ids under which the user's plugins shadow
// renamed built-ins, by new id. A plugin already installed under the new
// id wins, as one under the built-in's id does without a rename.
func shadowRenames(stateDir string, entries []os.DirEntry) map[string]string {
	ids := map[string]bool{}
	for _, e := range entries {
		folder := filepath.Join(Dir(stateDir), e.Name())
		if fi, err := os.Stat(folder); err != nil || !fi.IsDir() || len(e.Name()) == 0 || e.Name()[0] == '.' {
			continue
		}
		if m, err := plugin.ReadManifest(folder); err == nil {
			ids[m.ID] = true
		}
	}
	out := map[string]string{}
	for newID, oldID := range renamedBuiltins {
		if ids[oldID] && !ids[newID] {
			out[newID] = oldID
		}
	}
	return out
}
