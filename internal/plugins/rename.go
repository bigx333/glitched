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

// migrateRenamedPrefs moves the choices that are the renamed built-in's
// own: a capability revoked from it always, and its off switch when no
// plugin of the user's has either id. A copy's grants, settings and consent
// stay under the old id; Plan declares no settings of its own.
func migrateRenamedPrefs(stateDir string, logf func(string, ...any)) {
	var current prefs
	if err := jsonfile.Load(prefsPath(stateDir), &current); err != nil {
		return // Open logs the file it can't read.
	}
	entries, _ := os.ReadDir(Dir(stateDir))
	ids := userPluginIDs(stateDir, entries)
	if !renameMigrationNeeded(current, ids) {
		return
	}
	if _, err := jsonfile.Edit(prefsPath(stateDir), func(p *prefs) error {
		if !renameMigrationNeeded(*p, ids) {
			return errNoRenameMigration
		}
		for newID, oldID := range renamedBuiltins {
			migrateRenamedPref(p, oldID, newID, !ids[oldID] && !ids[newID])
		}
		return nil
	}); err != nil && !errors.Is(err, errNoRenameMigration) {
		logf("plugins: migrating renamed built-ins: %v", err)
	}
}

func renameMigrationNeeded(p prefs, ids map[string]bool) bool {
	for newID, oldID := range renamedBuiltins {
		if _, ok := p.Revoked[oldID]; ok {
			return true
		}
		if !ids[oldID] && !ids[newID] && slices.Contains(p.Disabled, oldID) {
			return true
		}
	}
	return false
}

func migrateRenamedPref(p *prefs, oldID, newID string, migrateSwitch bool) {
	moveRevoked(p.Revoked, oldID, newID)
	if migrateSwitch && slices.Contains(p.Disabled, oldID) {
		p.Disabled = slices.DeleteFunc(p.Disabled, func(id string) bool { return id == oldID })
		if !slices.Contains(p.Disabled, newID) {
			p.Disabled = append(p.Disabled, newID)
		}
	}
}

func moveRevoked(m map[string][]string, oldID, newID string) {
	old, ok := m[oldID]
	if !ok {
		return
	}
	next := slices.Clone(m[newID])
	for _, capability := range old {
		if !slices.Contains(next, capability) {
			next = append(next, capability)
		}
	}
	if len(next) == 0 {
		delete(m, newID)
	} else {
		m[newID] = next
	}
	delete(m, oldID)
}

// userPluginIDs are the manifest ids of the plugins installed under the
// user's plugins folder, whether or not they load.
func userPluginIDs(stateDir string, entries []os.DirEntry) map[string]bool {
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
	return ids
}

// shadowRenames are the old ids under which the user's plugins shadow
// renamed built-ins, by new id. A plugin already installed under the new
// id wins, as one under the built-in's id does without a rename.
func shadowRenames(stateDir string, entries []os.DirEntry) map[string]string {
	ids := userPluginIDs(stateDir, entries)
	out := map[string]string{}
	for newID, oldID := range renamedBuiltins {
		if ids[oldID] && !ids[newID] {
			out[newID] = oldID
		}
	}
	return out
}
