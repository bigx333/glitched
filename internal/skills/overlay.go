package skills

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// plugin is the manifest of the Claude Code plugin an overlay is.
const plugin = `{"name":"glitched","version":"1.0.0","description":"Skills Glitched gives its agents"}`

// Runs is the folder of the overlays under stateDir.
func Runs(stateDir string) string { return filepath.Join(stateDir, "run-skills") }

// SkillFile is where the SKILL.md of the skill name is in the overlay
// root.
func SkillFile(root, name string) string { return filepath.Join(root, "skills", name, "SKILL.md") }

// Build is the overlay of set's skills, what an agent gets: a Claude Code
// plugin folder, <stateDir>/run-skills/<set's hash>, with a copy of each
// skill in skills/<name>, its files read-only. One there already is the same one,
// and stays; no skills build none, and Build returns "". A skill that
// changed since Find read it fails the build.
func Build(stateDir string, set Set) (string, error) {
	if len(set.Skills) == 0 {
		return "", nil
	}
	runs := Runs(stateDir)
	root := filepath.Join(runs, set.Hash())
	if _, err := os.Lstat(root); err == nil {
		return root, nil
	}
	if err := os.MkdirAll(runs, 0o700); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(runs, ".build-")
	if err != nil {
		return "", err
	}
	if err := fill(tmp, set); err != nil {
		os.RemoveAll(tmp)
		return "", err
	}
	if err := os.Rename(tmp, root); err != nil {
		os.RemoveAll(tmp)
		if _, statErr := os.Lstat(root); statErr == nil {
			return root, nil
		}
		return "", err
	}
	return root, nil
}

// fill copies set's skills into dir, with the plugin's manifest.
func fill(dir string, set Set) error {
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "plugin.json"), []byte(plugin+"\n"), 0o444); err != nil {
		return err
	}
	for _, sk := range set.Skills {
		to := filepath.Join(dir, "skills", sk.Name)
		hash, err := walk(sk.Dir, func(rel string, exec bool, data []byte) error {
			p := filepath.Join(to, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			mode := fs.FileMode(0o444)
			if exec {
				mode = 0o555
			}
			return os.WriteFile(p, data, mode)
		})
		if err != nil {
			return fmt.Errorf("skills: copying %s: %w", sk.Name, err)
		}
		if hash != sk.Hash {
			return fmt.Errorf("%w: %s", errChanged, sk.Name)
		}
	}
	return nil
}

// Sweep deletes the overlays under stateDir but those keep names.
func Sweep(stateDir string, keep []string) error {
	runs := Runs(stateDir)
	entries, err := os.ReadDir(runs)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range entries {
		p := filepath.Join(runs, e.Name())
		if !strings.HasPrefix(e.Name(), ".") && !slices.Contains(keep, p) {
			errs = append(errs, os.RemoveAll(p))
		}
	}
	return errors.Join(errs...)
}

// Clear deletes every overlay under stateDir, as an engine starting does:
// no agent of the engine before it still runs.
func Clear(stateDir string) error { return os.RemoveAll(Runs(stateDir)) }
