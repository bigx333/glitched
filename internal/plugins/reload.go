package plugins

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// fingerprint is what a plugin's folder holds, as names, sizes and times:
// a change to any file changes it. It skips what isn't the plugin's code.
func fingerprint(folder string) string {
	h := sha256.New()
	filepath.WalkDir(folder, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if fi, err := d.Info(); err == nil && !d.IsDir() {
			fmt.Fprintf(h, "%s\x00%d\x00%d\x00", path, fi.Size(), fi.ModTime().UnixNano())
		}
		return nil
	})
	return fmt.Sprintf("%x", h.Sum(nil))
}

// Watch reloads a plugin as its files change, each every while, until
// stop: a change to its code swaps it in place, keeping what its pages
// hold; a change to plugin.json reloads it whole. A plugin whose new code
// fails to load keeps running the old, and says why.
func (h *Host) Watch(every time.Duration) (stop func()) {
	done := make(chan struct{})
	go func() {
		seen := map[string]string{}
		tick := time.NewTicker(every)
		defer tick.Stop()
		for {
			entries, _ := os.ReadDir(Dir(h.state))
			now := map[string]string{}
			for _, e := range entries {
				folder := filepath.Join(Dir(h.state), e.Name())
				now[folder] = fingerprint(folder)
			}
			for folder, fp := range now {
				if old, ok := seen[folder]; ok && old != fp {
					h.opts.Post(func() { h.hotReload(folder) })
				}
			}
			if len(seen) > 0 && len(now) != len(seen) {
				h.opts.Post(h.Reload) // a plugin came or went
			}
			seen = now
			select {
			case <-done:
				return
			case <-tick.C:
			}
		}
	}()
	return func() { close(done) }
}

// hotReload reloads the plugin in folder. It runs on the UI thread.
func (h *Host) hotReload(folder string) {
	var in *installed
	for _, i := range h.list() {
		if i.folder == folder {
			in = i
		}
	}
	if in == nil || in.p == nil {
		h.Reload() // not running: maybe it loads now
		return
	}
	manifest, err := os.ReadFile(filepath.Join(folder, "plugin.json"))
	if err != nil || !bytes.Equal(manifest, in.raw) {
		h.Reload()
		return
	}
	next := &installed{folder: folder, manifest: in.manifest, enabled: true, raw: in.raw}
	p, err := h.load(next)
	if err != nil {
		h.mu.Lock()
		in.reloadErr = err
		h.mu.Unlock()
		h.opts.Logf("plugins: %s: kept the running code: %v", filepath.Base(folder), err)
		return
	}
	h.mu.Lock()
	old := in.p
	in.p, in.ext, in.reloadErr = p, next.ext, nil
	h.mu.Unlock()
	for k, s := range h.sessions {
		if k.plugin == in.manifest.ID {
			s.swap(p)
		}
	}
	go old.Close()
}

func (h *Host) list() []*installed {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.installed
}
