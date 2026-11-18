package plugins

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"time"

	plugin "github.com/yxxbc/mygo-plugin"
)

// fingerprint is what a plugin's folder holds, as names, sizes and times:
// a change to any file changes it. It skips what isn't the plugin's code.
func fingerprint(folder string) string {
	h := sha256.New()
	// A linked plugin folder is fingerprinted by what it links to.
	if real, err := filepath.EvalSymlinks(folder); err == nil {
		folder = real
	}
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
		var w watcher
		tick := time.NewTicker(every)
		defer tick.Stop()
		for {
			w.scan(h)
			select {
			case <-done:
				return
			case <-tick.C:
			}
		}
	}()
	return func() { close(done) }
}

// watcher is what Watch saw: each folder's fingerprint as loaded, and as
// the last scan found it.
type watcher struct {
	loaded, last map[string]string
}

// scan reloads the plugins whose files changed since they loaded and then
// held still a scan: an editor may empty a file before it writes it, and
// a scan between the two would load an empty plugin in place of the one
// running. A plugin that came or went reloads them all, once every folder
// held still a scan.
func (w *watcher) scan(h *Host) {
	entries, _ := os.ReadDir(Dir(h.state))
	now := map[string]string{}
	for _, e := range entries {
		folder := filepath.Join(Dir(h.state), e.Name())
		now[folder] = fingerprint(folder)
	}
	defer func() { w.last = now }()
	if w.loaded == nil {
		w.loaded = now
		return
	}
	if !sameFolders(now, w.loaded) {
		if maps.Equal(now, w.last) {
			h.opts.Post(h.Reload)
			w.loaded = now
		}
		return
	}
	loaded := map[string]string{}
	for folder, fp := range now {
		loaded[folder] = fp
		switch old := w.loaded[folder]; {
		case old == fp:
		case w.last[folder] != fp:
			loaded[folder] = old
		default:
			h.hotReload(folder) // here, off the UI thread
		}
	}
	w.loaded = loaded
}

func sameFolders(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for folder := range a {
		if _, ok := b[folder]; !ok {
			return false
		}
	}
	return true
}

// hotReload reloads the plugin in folder. It verifies, signs and
// compiles the new code here, off the UI thread, and swaps it in there.
func (h *Host) hotReload(folder string) {
	var in *installed
	for _, i := range h.list() {
		if i.folder == folder {
			in = i
		}
	}
	h.mu.RLock()
	running := in != nil && in.p != nil
	h.mu.RUnlock()
	manifest, err := os.ReadFile(filepath.Join(folder, "plugin.json"))
	if !running || err != nil || !bytes.Equal(manifest, in.raw) {
		h.opts.Post(h.Reload) // not running, or its manifest changed: whole
		return
	}
	next := &installed{folder: folder, manifest: in.manifest, enabled: true, raw: in.raw}
	h.mu.RLock()
	p, err := h.load(next)
	h.mu.RUnlock()
	h.opts.Post(func() { h.swapIn(in, next, p, err) })
}

// swapIn puts a reloaded plugin in its place, on the UI thread: its
// sessions keep their state. One that failed to load leaves the running
// one be, and says why.
func (h *Host) swapIn(in, next *installed, p *plugin.Plugin, err error) {
	h.mu.Lock()
	current := in.p != nil && slices.Contains(h.installed, in)
	if err != nil && current {
		in.reloadErr = err
	}
	h.mu.Unlock()
	if err != nil || !current {
		if p != nil {
			h.stopping.Go(p.Shutdown) // a Reload replaced it meanwhile
		}
		if err != nil {
			h.opts.Logf("plugins: %s: kept the running code: %v", filepath.Base(in.folder), err)
		}
		return
	}
	h.mu.Lock()
	old, oldDB := in.p, in.db
	in.alive.Store(false) // the old code's calls waiting on the UI thread do nothing
	in.p, in.ext, in.reloadErr, in.code, in.db, in.alive, in.awaiting = p, next.ext, nil, next.code, next.db, next.alive, next.awaiting
	h.mu.Unlock()
	for k, s := range h.sessions {
		if k.plugin == in.manifest.ID {
			s.swap(p)
		}
	}
	h.stopping.Go(func() {
		old.Shutdown()
		oldDB.close()
	})
	if h.opts.Reloaded != nil {
		h.opts.Reloaded(in.manifest.ID)
	}
}

func (h *Host) list() []*installed {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.installed
}
