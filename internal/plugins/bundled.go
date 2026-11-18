package plugins

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	plugin "github.com/yxxbc/mygo-plugin"

	"glitched/internal/plugins/pack"
)

// FallbackSuffix ends the id a built-in plugin runs under while a copy of
// the user's shadows it: it draws what the copy fails to.
const FallbackSuffix = ".builtin"

// BundledDir is where the built-in plugins are written out to be verified
// and run, one folder each, as the app holds them.
func BundledDir(stateDir string) string { return filepath.Join(stateDir, "plugin-bundled") }

// revertedDir keeps the copies Revert took away: a dot folder, which
// Reload passes over.
func revertedDir(stateDir string) string { return filepath.Join(Dir(stateDir), ".reverted") }

// writeBundled writes each built-in plugin out under BundledDir, as the
// app holds it, and returns their folders in name order. One that can't be
// written is logged and left out.
func (h *Host) writeBundled() []string {
	if h.opts.Bundled == nil {
		return nil
	}
	entries, err := fs.ReadDir(h.opts.Bundled, ".")
	if err != nil {
		h.opts.Logf("plugins: built-in: %v", err)
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		folder := filepath.Join(BundledDir(h.state), e.Name())
		if err := writeTree(h.opts.Bundled, e.Name(), folder); err != nil {
			h.opts.Logf("plugins: built-in %s: %v", e.Name(), err)
			continue
		}
		out = append(out, folder)
	}
	return out
}

// writeTree makes folder hold exactly the files under dir of src: it is
// written again whole when any file differs, and left be otherwise.
func writeTree(src fs.FS, dir, folder string) error {
	sub, err := fs.Sub(src, dir)
	if err != nil {
		return err
	}
	files := map[string][]byte{}
	err = fs.WalkDir(sub, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(sub, p)
		files[p] = b
		return err
	})
	if err != nil {
		return err
	}
	same := true
	count := 0
	filepath.WalkDir(folder, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		count++
		rel, _ := filepath.Rel(folder, p)
		have, _ := os.ReadFile(p)
		if want, ok := files[filepath.ToSlash(rel)]; !ok || !bytes.Equal(have, want) {
			same = false
		}
		return nil
	})
	if same && count == len(files) {
		return nil
	}
	if err := os.RemoveAll(folder); err != nil {
		return err
	}
	for rel, b := range files {
		p := filepath.Join(folder, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// builtinCaps are what a built-in plugin has without asking: the
// capabilities it declares, and the network if it names hosts.
func builtinCaps(m plugin.Manifest) []string {
	caps := slices.Clone(m.Capabilities)
	if len(m.Permissions.Network) > 0 {
		caps = append(caps, Network)
	}
	return caps
}

// releaseSigned reports whether a release key signed what keyID names.
func (h *Host) releaseSigned(keyID string) bool {
	return slices.ContainsFunc(h.opts.ReleaseKeys, func(pub string) bool { return pack.KeyID(pub) == keyID })
}

// shadowing is the user's folder that runs in place of the built-in
// plugin id, nil if none does.
func (h *Host) shadowing(id string) *installed {
	list := h.list()
	for _, b := range list {
		if b.bundled && b.manifest.ID == id+FallbackSuffix {
			for _, in := range list {
				if in.folder == b.shadowedBy {
					return in
				}
			}
		}
	}
	return nil
}

// Revert runs the built-in plugin id again in place of the user's copy:
// the copy's folder moves aside, under the plugins folder's .reverted,
// so nothing the user wrote is lost, and the plugin is turned on.
func (h *Host) Revert(id string) error {
	in := h.shadowing(id)
	if in == nil {
		return fmt.Errorf("no copy of yours runs in place of the built-in %s", id)
	}
	h.closeAll()
	h.stopping.Wait()
	if err := os.MkdirAll(revertedDir(h.state), 0o755); err != nil {
		return err
	}
	to := filepath.Join(revertedDir(h.state), id+"-"+strconv.FormatInt(time.Now().Unix(), 10))
	if err := os.Rename(in.folder, to); err != nil {
		h.Reload()
		return err
	}
	return h.edit(func(p *prefs) {
		p.Disabled = slices.DeleteFunc(p.Disabled, func(s string) bool { return s == id })
	})
}

// Copy makes the built-in plugin id the user's to change: its files go to
// a folder of the plugins folder, signed with this device's key, which
// then runs in place of the built-in, and reloads as the user edits it.
// It starts with the grants the built-in has now, which are then the
// user's to change; a folder of the same id made by hand starts with none.
func (h *Host) Copy(id string) (string, error) {
	var from string
	var grants []string
	for _, in := range h.list() {
		if in.bundled && in.manifest.ID == id && in.shadowedBy == "" {
			from = in.folder
			h.mu.RLock()
			grants = slices.DeleteFunc(builtinCaps(in.manifest), func(c string) bool { return !h.granted(id, c) })
			h.mu.RUnlock()
		}
	}
	if from == "" {
		return "", fmt.Errorf("no built-in plugin %s to copy", id)
	}
	to := filepath.Join(Dir(h.state), id)
	if _, err := os.Stat(to); !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("%s is already there", to)
	}
	err := filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(to, rel), 0o755)
		}
		if rel == pack.SignatureFile {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(to, rel), b, 0o644)
	})
	if err == nil {
		var key *pack.Key
		if key, err = pack.LoadKey(h.state); err == nil {
			err = pack.Sign(to, key)
		}
	}
	if err == nil {
		err = h.save(func(p *prefs) {
			if p.Grants == nil {
				p.Grants = map[string][]string{}
			}
			p.Grants[id] = grants
		})
	}
	if err != nil {
		os.RemoveAll(to)
		return "", err
	}
	h.Reload()
	return to, nil
}
