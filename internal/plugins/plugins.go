// Package plugins hosts the plugins installed in Glitched, with
// github.com/yxxbc/mygo-plugin. A plugin is a plugin.json and a JavaScript
// or TypeScript module, run in a runtime of its own. It contributes pages
// as data, which the app draws with its own components, and commands that
// the pages' items run.
package plugins

import (
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	plugin "github.com/yxxbc/mygo-plugin"
)

// Contract is the app contract Glitched provides: plugin.json's contract
// and contractVersion. A plugin that names another contract, or a newer
// version, does not load. One that names none loads.
const (
	Contract        = "glitched"
	ContractVersion = 1
)

// Options are what the app gives its plugins.
type Options struct {
	// AppVersion is Glitched's version, which a plugin's minAppVersion is
	// checked against.
	AppVersion string
	// Language is the user's language, as a BCP 47 tag.
	Language func() string
	// OpenURL opens a web page in the user's browser.
	OpenURL func(string) error
	// Logf logs a plugin that doesn't load, and what plugins log.
	Logf func(format string, args ...any)
}

// Host is the plugins installed under one state directory. It doesn't
// change once open, so its methods may be called from any goroutine.
type Host struct {
	plugins []*plugin.Plugin
	byID    map[string]*plugin.Plugin
	openURL func(string) error
}

// Dir is where the plugins are installed, one folder each.
func Dir(stateDir string) string { return filepath.Join(stateDir, "plugins") }

// DataDir is where the plugins keep their storage, one folder per id.
func DataDir(stateDir string) string { return filepath.Join(stateDir, "plugin-data") }

// Open loads the plugins installed under stateDir, without running them.
// One that doesn't load is logged and left out, and so is a second folder
// with an id already loaded, which would share the first's data.
func Open(stateDir string, o Options) *Host {
	if o.Logf == nil {
		o.Logf = log.Printf
	}
	h := &Host{byID: map[string]*plugin.Plugin{}, openURL: o.OpenURL}
	opts := plugin.Options{
		AppVersion:      o.AppVersion,
		DataDir:         DataDir(stateDir),
		Contract:        Contract,
		ContractVersion: ContractVersion,
		Host: plugin.Host{
			Language: o.Language,
			OpenURL:  o.OpenURL,
			Logf:     o.Logf,
		},
	}
	entries, err := os.ReadDir(Dir(stateDir))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		o.Logf("plugins: %v", err)
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		p, err := plugin.Load(filepath.Join(Dir(stateDir), e.Name()), opts)
		if err != nil {
			o.Logf("plugins: %v", err)
			continue
		}
		if _, dup := h.byID[p.ID()]; dup {
			o.Logf("plugins: %s: id %q is already loaded", e.Name(), p.ID())
			p.Close()
			continue
		}
		h.plugins = append(h.plugins, p)
		h.byID[p.ID()] = p
	}
	return h
}

// Entry is a page a plugin contributes, as the sidebar lists it.
type Entry struct {
	Plugin, Page string
	Title, Icon  string
}

// Entries are the pages the plugins contribute, plugin by plugin in the
// order of their folders, each plugin's in the order it lists them.
func (h *Host) Entries() []Entry {
	var out []Entry
	for _, p := range h.plugins {
		for _, n := range p.Manifest().Pages() {
			if n.Page == "" {
				continue // a builtin page of MyGo Music's, which Glitched doesn't have
			}
			out = append(out, Entry{Plugin: p.ID(), Page: n.Page, Title: n.Title, Icon: n.Icon})
		}
	}
	return out
}

// Name is the plugin's name, or its id if it gives none.
func (h *Host) Name(id string) string {
	if p := h.byID[id]; p != nil && p.Manifest().Name != "" {
		return p.Manifest().Name
	}
	return id
}

// Page asks the plugin id for its page. It runs the plugin's code, up to
// its call timeout, so it belongs off the UI thread.
func (h *Host) Page(id, page string) (*plugin.Page, error) {
	p, err := h.plugin(id)
	if err != nil {
		return nil, err
	}
	return p.Page(page)
}

// Act does what an action of an item on the plugin id's page asks: a
// command runs the plugin's command named in its meta.command, with its
// meta as arguments, as mygo-plugin's reference host does, and a url opens
// a web page. A command runs the plugin's code, so Act belongs off the UI
// thread.
func (h *Host) Act(id string, a plugin.Action) error {
	switch a.Kind {
	case "command":
		p, err := h.plugin(id)
		if err != nil {
			return err
		}
		name, _ := a.Meta["command"].(string)
		if name == "" {
			return fmt.Errorf("action %q names no command", a.ID)
		}
		return p.RunCommand(name, a.Meta)
	case "url":
		if !webURL(a.URL) {
			return fmt.Errorf("action %q: %q is not a web address", a.ID, a.URL)
		}
		if h.openURL == nil {
			return errors.New("this app opens no web pages")
		}
		return h.openURL(a.URL)
	}
	return fmt.Errorf("action %q is of a kind Glitched doesn't know: %q", a.ID, a.Kind)
}

// Close stops every plugin's runtime.
func (h *Host) Close() {
	for _, p := range h.plugins {
		p.Close()
	}
}

func (h *Host) plugin(id string) (*plugin.Plugin, error) {
	if p := h.byID[id]; p != nil {
		return p, nil
	}
	return nil, fmt.Errorf("no plugin %q is installed", id)
}

// webURL reports whether s is an http or https address. A plugin must not
// hand the system a file: address or another app's scheme.
func webURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}
