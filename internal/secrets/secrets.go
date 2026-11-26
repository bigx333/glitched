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
	"sync/atomic"

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

	mu      sync.Mutex
	loaded  map[api.ID]map[string]string
	ever    map[api.ID]map[string][]string // every value each thread held while the vault was in use, by name
	deleted map[api.ID]bool                // threads Delete deleted, which hold nothing more
	unsure  map[api.ID]bool                // threads whose values the vault can't all tell
	scanned bool                           // the vault has read every thread's folder
	scan    sync.Once
	held    atomic.Pointer[Held] // read without mu
}

// Held is what the vault's threads have held, as the provider log checks
// lines against: every value any of them held while the vault was in
// use, those since replaced or forgotten too, as an agent may still hold
// one, or find it in a workspace another thread's was written to.
// Complete is that the vault has read every thread's folder, can read all
// of its values, and found its environment script agrees, and is writing
// none. Values from before the vault was in use can't be known.
type Held struct {
	Values   []string
	Complete bool
}

// ErrDeleted is a deleted thread's, which the vault keeps nothing of.
var ErrDeleted = errors.New("secrets: the thread is deleted")

// Under is the vault's folder in an engine's state directory.
func Under(stateDir string) string { return filepath.Join(stateDir, "secrets") }

func New(dir string) *Vault {
	return &Vault{dir: dir, loaded: map[api.ID]map[string]string{}, ever: map[api.ID]map[string][]string{}, deleted: map[api.ID]bool{}, unsure: map[api.ID]bool{}}
}

// Dir is thread's folder.
func (v *Vault) Dir(thread api.ID) string { return filepath.Join(v.dir, thread.String()) }

// Prepare makes thread's folder with an empty EnvScript and ClaudeEnv, if
// it has none, and returns the folder.
func (v *Vault) Prepare(thread api.ID) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.prepareLocked(thread)
}

func (v *Vault) prepareLocked(thread api.ID) (string, error) {
	if v.deleted[thread] {
		return "", ErrDeleted
	}
	dir := v.Dir(thread)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", err
	}
	for name, content := range map[string]string{EnvScript: "", ClaudeEnv: ". " + quote(filepath.Join(dir, EnvScript)) + "\n"} {
		err := create(filepath.Join(dir, name), content)
		if errors.Is(err, fs.ErrExist) {
			err = regular(filepath.Join(dir, name))
		}
		if err != nil {
			return "", err
		}
	}
	return dir, nil
}

// Put keeps values for thread, each replacing what the thread held under
// its name, and rewrites EnvScript with all of them.
func (v *Vault) Put(thread api.ID, values map[string]string) (err error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	dir, err := v.prepareLocked(thread)
	if err != nil {
		return err
	}
	all, err := v.valuesLocked(thread)
	if err != nil {
		return err
	}
	all = maps.Clone(all)
	if h := v.held.Load(); h != nil {
		v.held.Store(&Held{Values: h.Values})
	}
	defer func() {
		if err != nil {
			delete(v.loaded, thread)
		}
		v.checkLocked(thread)
		v.publishLocked()
	}()
	for name, value := range values {
		if !api.ValidName(name) {
			return fmt.Errorf("secrets: %q is not an environment variable's name", name)
		}
		v.everLocked(thread, name, value)
		if err := replace(filepath.Join(dir, name), value); err != nil {
			return err
		}
		all[name] = value
	}
	if err := replace(filepath.Join(dir, EnvScript), script(all)); err != nil {
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

// RedactValues returns every value thread currently holds or previously
// held while the vault was in use (including rotated or forgotten values),
// for output redaction.
func (v *Vault) RedactValues(thread api.ID) map[string]string {
	v.mu.Lock()
	defer v.mu.Unlock()
	cur, _ := v.valuesLocked(thread)
	ever := v.ever[thread]
	if len(cur) == 0 && len(ever) == 0 {
		return nil
	}
	out := make(map[string]string, len(cur)+len(ever))
	for k, val := range cur {
		out[k] = val
	}
	for k, vals := range ever {
		for i, val := range vals {
			if existing, ok := out[k]; !ok {
				out[k] = val
			} else if existing != val {
				out[fmt.Sprintf("%s#%d", k, i+1)] = val
			}
		}
	}
	return out
}

func (v *Vault) valuesLocked(thread api.ID) (map[string]string, error) {
	if v.deleted[thread] {
		return nil, ErrDeleted
	}
	if all, ok := v.loaded[thread]; ok {
		return all, nil
	}
	all := map[string]string{}
	entries, err := os.ReadDir(v.Dir(thread))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	envInfo, _ := os.Lstat(filepath.Join(v.Dir(thread), EnvScript))
	for _, e := range entries {
		if !e.Type().IsRegular() || e.Name() == EnvScript {
			continue
		}
		if !api.ValidName(e.Name()) {
			// A vault written by an earlier build on a case-sensitive volume
			// may hold a legacy ENV file distinct from EnvScript; keep reading
			// it so redaction never drops existing secrets on upgrade.
			if !strings.EqualFold(e.Name(), EnvScript) {
				continue
			}
			info, err := e.Info()
			if err != nil || (envInfo != nil && os.SameFile(info, envInfo)) {
				continue
			}
		}
		b, err := os.ReadFile(filepath.Join(v.Dir(thread), e.Name()))
		if err != nil {
			return nil, err
		}
		all[e.Name()] = string(b)
		v.everLocked(thread, e.Name(), string(b))
	}
	v.loaded[thread] = all
	return all, nil
}

// Held is what the vault's threads have held, as last known, which it
// reads without waiting on the vault: none, incomplete, until the vault
// has read every thread's folder, which the first call starts.
func (v *Vault) Held() Held {
	v.scan.Do(func() { go v.scanAll() })
	if h := v.held.Load(); h != nil {
		return *h
	}
	return Held{}
}

// scanAll reads every thread's folder, for Held.
func (v *Vault) scanAll() {
	v.mu.Lock()
	defer v.mu.Unlock()
	defer v.publishLocked()
	entries, err := os.ReadDir(v.dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return
	}
	for _, e := range entries {
		if thread, err := api.ParseID(e.Name()); err == nil && e.IsDir() && !v.deleted[thread] {
			v.checkLocked(thread)
		}
	}
	v.scanned = true
}

// checkLocked has the vault note whether it can tell all thread's values:
// whether it can read them, and its environment script exports them.
func (v *Vault) checkLocked(thread api.ID) {
	all, err := v.valuesLocked(thread)
	if err == nil {
		path := filepath.Join(v.Dir(thread), EnvScript)
		var b []byte
		if err = regular(path); err == nil {
			b, err = os.ReadFile(path)
		}
		if errors.Is(err, fs.ErrNotExist) && len(all) == 0 {
			err = nil
		} else if err == nil && string(b) != script(all) {
			err = errors.New("secrets: the environment script disagrees")
		}
	}
	if err != nil {
		v.unsure[thread] = true
	} else {
		delete(v.unsure, thread)
	}
}

// publishLocked has Held say what the vault knows now.
func (v *Vault) publishLocked() {
	h := Held{Complete: v.scanned && len(v.unsure) == 0}
	for _, held := range v.ever {
		for _, values := range held {
			h.Values = append(h.Values, values...)
		}
	}
	v.held.Store(&h)
}

// script is EnvScript exporting all.
func script(all map[string]string) string {
	var env strings.Builder
	for _, name := range slices.Sorted(maps.Keys(all)) {
		fmt.Fprintf(&env, "export %s=%s\n", name, quote(all[name]))
	}
	return env.String()
}

// everLocked adds value to what thread has held under name.
func (v *Vault) everLocked(thread api.ID, name, value string) {
	held := v.ever[thread]
	if held == nil {
		held = map[string][]string{}
		v.ever[thread] = held
	}
	if !slices.Contains(held[name], value) {
		held[name] = append(held[name], value)
	}
}

// Forget deletes thread's folder and everything in it, after recording any
// on-disk values in v.ever so active consumers still redact them after a
// restart. If reading the folder fails, it returns the error without
// deleting the folder so no unread secret is lost.
func (v *Vault) Forget(thread api.ID) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	defer v.publishLocked()
	defer v.checkLocked(thread)
	if _, err := v.valuesLocked(thread); err != nil && !errors.Is(err, ErrDeleted) {
		return err
	}
	delete(v.loaded, thread)
	return os.RemoveAll(v.Dir(thread))
}

// Delete forgets thread, and every value it has held, for good: for a
// thread that is deleted, which nothing speaks for anymore. The vault
// keeps nothing more of it.
func (v *Vault) Delete(thread api.ID) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	defer v.publishLocked()
	v.deleted[thread] = true
	delete(v.loaded, thread)
	delete(v.ever, thread)
	delete(v.unsure, thread)
	return os.RemoveAll(v.Dir(thread))
}

// quote is s as one word of a POSIX shell.
func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// regular fails where path is anything but a regular file, as a FIFO
// whose reading would wait.
func regular(path string) error {
	fi, err := os.Lstat(path)
	if err == nil && !fi.Mode().IsRegular() {
		err = fmt.Errorf("secrets: %s is no regular file", path)
	}
	return err
}

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
