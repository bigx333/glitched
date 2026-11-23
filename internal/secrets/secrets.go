// Package secrets is the vault of credentials users give agents: a folder
// per thread, outside every workspace, on the host that runs the thread.
// Nothing in it is logged, committed or sent anywhere but to the agent's
// own commands, which read its files.
package secrets

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"glitched/internal/api"
)

// EnvScript is the name of the script in a thread's folder that exports
// every value it holds.
const EnvScript = "env"

// ClaudeEnv is the script Claude Code sources before each Bash command, as
// CLAUDE_ENV_FILE: it sources EnvScript. Claude's hooks may append to it,
// so it is written once and never rewritten.
const ClaudeEnv = "claude.env"

// Vault keeps each thread's values in Dir/<thread>.
type Vault struct {
	dir string

	mu     sync.Mutex
	loaded map[api.ID]map[string]string
}

func New(dir string) *Vault {
	return &Vault{dir: dir, loaded: map[api.ID]map[string]string{}}
}

// Dir is thread's folder.
func (v *Vault) Dir(thread api.ID) string { return filepath.Join(v.dir, thread.String()) }

// Prepare makes thread's folder with an empty EnvScript and ClaudeEnv, if
// it has none, and returns the folder.
func (v *Vault) Prepare(thread api.ID) (string, error) {
	dir := v.Dir(thread)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", err
	}
	for name, content := range map[string]string{EnvScript: "", ClaudeEnv: ". " + quote(filepath.Join(dir, EnvScript)) + "\n"} {
		err := create(filepath.Join(dir, name), content)
		if err != nil && !errors.Is(err, fs.ErrExist) {
			return "", err
		}
	}
	return dir, nil
}

// Put keeps values for thread, each replacing what the thread held under
// its name, and rewrites EnvScript with all of them.
func (v *Vault) Put(thread api.ID, values map[string]string) error {
	dir, err := v.Prepare(thread)
	if err != nil {
		return err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	all, err := v.valuesLocked(thread)
	if err != nil {
		return err
	}
	all = maps.Clone(all)
	for name, value := range values {
		if !api.ValidName(name) {
			return fmt.Errorf("secrets: %q is not an environment variable's name", name)
		}
		if err := replace(filepath.Join(dir, name), value); err != nil {
			return err
		}
		all[name] = value
	}
	var env strings.Builder
	for _, name := range slices.Sorted(maps.Keys(all)) {
		fmt.Fprintf(&env, "export %s=%s\n", name, quote(all[name]))
	}
	if err := replace(filepath.Join(dir, EnvScript), env.String()); err != nil {
		return err
	}
	v.loaded[thread] = all
	return nil
}

// Values are thread's values by name; none for a thread that holds none.
// The map is the vault's: callers must not change it.
func (v *Vault) Values(thread api.ID) map[string]string {
	v.mu.Lock()
	defer v.mu.Unlock()
	all, _ := v.valuesLocked(thread)
	return all
}

func (v *Vault) valuesLocked(thread api.ID) (map[string]string, error) {
	if all, ok := v.loaded[thread]; ok {
		return all, nil
	}
	all := map[string]string{}
	entries, err := os.ReadDir(v.Dir(thread))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	for _, e := range entries {
		if !e.Type().IsRegular() || !api.ValidName(e.Name()) || e.Name() == EnvScript {
			continue
		}
		b, err := os.ReadFile(filepath.Join(v.Dir(thread), e.Name()))
		if err != nil {
			return nil, err
		}
		all[e.Name()] = string(b)
	}
	v.loaded[thread] = all
	return all, nil
}

// Forget deletes thread's folder and everything in it.
func (v *Vault) Forget(thread api.ID) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.loaded, thread)
	return os.RemoveAll(v.Dir(thread))
}

// quote is s as one word of a POSIX shell.
func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// create writes a new file only the user can read, and fails if path is
// there already, whatever it is.
func create(path, content string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = f.WriteString(content)
	return errors.Join(err, f.Close())
}

// replace puts content at path through a new file renamed over it, so a
// reader sees the old file or the new one, never a symlink's target.
func replace(path, content string) error {
	b := make([]byte, 8)
	rand.Read(b)
	tmp := path + ".tmp-" + hex.EncodeToString(b)
	if err := create(tmp, content); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
