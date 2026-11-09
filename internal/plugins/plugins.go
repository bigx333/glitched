// Package plugins hosts the plugins installed in Glitched, with
// github.com/yxxbc/mygo-plugin. A plugin is a plugin.json and a JavaScript
// or TypeScript module, run in a runtime of its own. It contributes pages
// and commands. A page is either a stock mygo-plugin page (a page export
// answering sections and items) or a native view: a widget tree (package
// widget) that the plugin's view export answers from state the host keeps,
// and its update export changes as the user acts on it. What a plugin may
// do in the app beyond that, it asks for as effects, which the host checks
// against the capabilities it declared and the user granted.
//
// docs/plugins.md is the contract.
package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	plugin "github.com/yxxbc/mygo-plugin"

	"glitched/internal/jsonfile"
	"glitched/internal/plugins/guard"
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
	// Post runs a function on the UI thread, where sessions publish what
	// they show and effects run.
	Post func(func())
	// Do runs an effect a plugin asked for and was allowed, on the UI
	// thread, and returns its answer.
	Do func(plugin string, e Effect) (any, error)
	// CallTimeout bounds one call into a plugin; zero is mygo-plugin's 30
	// seconds. MemoryLimit bounds one plugin's runtime; zero is 256 MiB.
	CallTimeout time.Duration
	MemoryLimit int64
	// TimerScale shortens timers, in tests.
	TimerScale float64
	// SignOwn signs the user's own plugins again as they change. The
	// window's host does; the engine's leaves it to the window, so the two
	// never sign one folder at once.
	SignOwn bool
	// Reloaded is told, on the UI thread, after a plugin's code is
	// reloaded, so that what the app keeps of it goes.
	Reloaded func(id string)
	// Run runs a program the plugin id may, as glitched.run asks: its
	// manifest's programs are allowed. Nil runs none.
	Run func(ctx context.Context, id string, allowed []string, args json.RawMessage) (any, error)
	// ServiceCall calls a method of the plugin id's service, as
	// glitched.service.call asks. Nil reaches none.
	ServiceCall func(ctx context.Context, id, method string, args json.RawMessage) (any, error)
	// Main, when set, is the module each plugin runs in place of its
	// manifest's main, as the service host runs services: a plugin
	// without one is left out.
	Main func(plugin.Manifest, Extension) string
}

// Host is the plugins installed under one state directory. Its methods
// are called on the UI thread, but for Page and Act, which run a plugin's
// code and belong off it.
type Host struct {
	state string
	opts  Options

	mu        sync.RWMutex // guards installed and prefs, which a session's worker reads
	installed []*installed
	prefs     prefs
	statuses  map[string]map[string]SettingStatus

	sessions map[pageKey]*Session
}

type installed struct {
	folder   string
	manifest plugin.Manifest // zero when plugin.json didn't read
	err      error           // why it isn't loaded, besides being disabled
	enabled  bool
	p        *plugin.Plugin // nil unless loaded
	ext      Extension      // plugin.json's glitched block
	raw      []byte         // plugin.json as loaded
	// reloadErr is why the plugin's changed code didn't load: it runs the
	// code it had.
	reloadErr error
	// code is the read-only snapshot of its signed files it runs from.
	code   string
	trust  Trust
	signer string
	asks   []string // what it asks for, when it waits for consent
	// waitingKey signed it, as it waits for consent.
	waitingKey string
	db         *pluginDB
}

// prefs are what the user chose for the plugins, in plugins.json in the
// state directory.
type prefs struct {
	Disabled []string            `json:"disabled,omitempty"`
	Grants   map[string][]string `json:"grants,omitempty"`
	// Settings are the values the user set, by plugin and setting.
	Settings map[string]map[string]json.RawMessage `json:"settings,omitempty"`
	// Consent is what the user allowed each plugin of another key.
	Consent map[string]consent `json:"consent,omitempty"`
}

// Dir is where the plugins are installed, one folder each.
func Dir(stateDir string) string { return filepath.Join(stateDir, "plugins") }

// DataDir is where the plugins keep their storage, one folder per id.
func DataDir(stateDir string) string { return filepath.Join(stateDir, "plugin-data") }

func prefsPath(stateDir string) string { return filepath.Join(stateDir, "plugins.json") }

// Open loads the plugins installed under stateDir, without running them.
func Open(stateDir string, o Options) *Host {
	if o.Logf == nil {
		o.Logf = log.Printf
	}
	if o.Post == nil {
		o.Post = func(fn func()) { fn() }
	}
	h := &Host{state: stateDir, opts: o, sessions: map[pageKey]*Session{}}
	if err := jsonfile.Load(prefsPath(stateDir), &h.prefs); err != nil && !errors.Is(err, os.ErrNotExist) {
		o.Logf("plugins: %v", err)
	}
	h.Reload()
	return h
}

// Reload reads the plugins folder again: it closes every plugin and its
// pages, and loads what is there now. A plugin that doesn't load is kept,
// with why, and leaves the others be; so is a second folder with an id
// already loaded, which would share the first's data.
func (h *Host) Reload() {
	h.closeAll()
	entries, err := os.ReadDir(Dir(h.state))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		h.opts.Logf("plugins: %v", err)
	}
	var list []*installed
	seen := map[string]bool{}
	for _, e := range entries {
		folder := filepath.Join(Dir(h.state), e.Name())
		if fi, err := os.Stat(folder); err != nil || !fi.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue // a folder, or a link to one, as while developing
		}
		in := &installed{folder: folder}
		list = append(list, in)
		m, err := plugin.ReadManifest(in.folder)
		if err != nil {
			in.err = err
			continue
		}
		in.manifest = *m
		in.raw, _ = os.ReadFile(filepath.Join(in.folder, "plugin.json"))
		in.enabled = !slices.Contains(h.prefs.Disabled, m.ID)
		switch {
		case seen[m.ID]:
			in.err = fmt.Errorf("the id %q is already installed", m.ID)
			in.enabled = false
		case in.enabled:
			in.p, in.err = h.load(in)
			if errors.Is(in.err, errNotHere) {
				in.err = nil
			}
		}
		seen[m.ID] = true
	}
	for _, in := range list {
		if in.err != nil {
			h.opts.Logf("plugins: %s: %v", filepath.Base(in.folder), in.err)
		}
	}
	h.mu.Lock()
	h.installed = list
	h.mu.Unlock()
	// A window's plugin with timers wakes on them though it has no place
	// shown yet: its app session starts now.
	if h.opts.Main == nil {
		for _, in := range h.running() {
			if len(in.ext.Timers) > 0 && in.ext.Service == "" && in.p.Can("update") {
				h.App(in.manifest.ID)
			}
		}
	}
}

func (h *Host) load(in *installed) (*plugin.Plugin, error) {
	m := in.manifest
	if err := checkDeclared(m.Capabilities); err != nil {
		return nil, err
	}
	x, err := readExtension(in.folder)
	if err != nil {
		return nil, err
	}
	for _, c := range x.needs() {
		if !slices.Contains(m.Capabilities, c) {
			return nil, fmt.Errorf("its glitched block needs the %q capability, which it doesn't declare", c)
		}
	}
	in.ext = x
	code, err := h.verify(in)
	if err != nil {
		return nil, err
	}
	in.code = code
	var granted []string
	for _, c := range m.Capabilities {
		if h.allowed(m.ID, c) {
			granted = append(granted, c)
		}
	}
	network := h.granted(m.ID, Network)
	if network && len(m.Permissions.Network) > 0 && !slices.Contains(granted, Network) {
		granted = append(granted, Network)
	}
	main := ""
	if h.opts.Main != nil {
		if main = h.opts.Main(m, x); main == "" {
			return nil, errNotHere
		}
		// The service module runs only once service is granted; till then
		// a plugin with tools answers them from its main module.
		if main == x.Service && !h.allowed(m.ID, "service") {
			if len(x.Tools) == 0 {
				return nil, errNotHere
			}
			main = m.Main
		}
	}
	client, err := httpClient(m.Permissions.Network)
	if err != nil {
		return nil, fmt.Errorf("permissions.network: %w", err)
	}
	return plugin.Load(code, plugin.Options{
		HTTPClient:      client,
		Main:            main,
		Functions:       h.functions(in),
		Prelude:         Prelude,
		LoadTimeout:     LoadTimeout,
		AppVersion:      h.opts.AppVersion,
		DataDir:         DataDir(h.state),
		Contract:        Contract,
		ContractVersion: ContractVersion,
		Capabilities:    granted,
		CallTimeout:     h.opts.CallTimeout,
		MemoryLimit:     h.opts.MemoryLimit,
		Allowed:         func(string, string) bool { return network },
		Host: plugin.Host{
			Language: h.opts.Language,
			OpenURL:  h.opts.OpenURL,
			Logf:     h.opts.Logf,
			Setting:  h.settingText,
		},
	})
}

// granted reports whether the user granted the plugin id the capability.
func (h *Host) granted(id, capability string) bool {
	return slices.Contains(h.prefs.Grants[id], capability)
}

// allowed reports whether the plugin id may use the capability: it
// declared it, as the caller checks, and the user granted it if it needs
// a grant.
func (h *Host) allowed(id, name string) bool {
	c, ok := capability(name)
	if !ok {
		return name == Network && h.granted(id, Network)
	}
	return !c.Grant || h.granted(id, name)
}

// Plugin is an installed plugin, as Settings lists it.
type Plugin struct {
	ID, Name, Version, Description string
	Folder                         string
	Enabled                        bool
	// Err is why the plugin isn't running, if it's enabled.
	Err          error
	Capabilities []CapabilityState
	// Network lists the hosts the plugin asks to reach.
	Network []string
	// Programs are the programs it runs, each with what running it risks.
	Programs []Program
	// Trust is why it may run, or why it waits; Signer the key that signed
	// it; Asks, while it waits for consent, what it asks for in words.
	Trust  Trust
	Signer string
	Asks   []string
}

// Program is a program a plugin names, and what running it risks.
type Program struct{ Name, Risk string }

// CapabilityState is a capability a plugin declares, and whether it has it.
type CapabilityState struct {
	Capability
	Granted bool
}

// Installed lists the installed plugins, in the order of their folders.
func (h *Host) Installed() []Plugin {
	h.mu.RLock()
	defer h.mu.RUnlock()
	var out []Plugin
	for _, in := range h.installed {
		m := in.manifest
		p := Plugin{ID: m.ID, Name: m.Name, Version: m.Version, Description: m.Description, Folder: in.folder, Enabled: in.enabled, Err: in.err, Network: m.Permissions.Network}
		p.Trust, p.Signer = in.trust, in.signer
		for _, a := range in.asks {
			p.Asks = append(p.Asks, describeAsk(a))
		}
		if in.reloadErr != nil {
			p.Err = fmt.Errorf("its changed code didn't load, so it runs the code it had: %w", in.reloadErr)
		}
		if p.Name == "" {
			p.Name = filepath.Base(in.folder)
		}
		for _, name := range m.Capabilities {
			c, ok := capability(name)
			if !ok {
				c = Capability{Name: name, Grant: true}
			}
			p.Capabilities = append(p.Capabilities, CapabilityState{c, h.allowed(m.ID, name)})
		}
		for _, name := range in.ext.Programs {
			p.Programs = append(p.Programs, Program{name, guard.Risk(name)})
		}
		if len(p.Network) > 0 {
			p.Capabilities = append(p.Capabilities, CapabilityState{Capability{Network, true, "Reach " + strings.Join(p.Network, ", ")}, h.granted(m.ID, Network)})
		}
		out = append(out, p)
	}
	return out
}

// SetEnabled turns the plugin id on or off, and reloads the plugins.
func (h *Host) SetEnabled(id string, on bool) error {
	return h.edit(func(p *prefs) {
		p.Disabled = slices.DeleteFunc(p.Disabled, func(s string) bool { return s == id })
		if !on {
			p.Disabled = append(p.Disabled, id)
		}
	})
}

// SetGranted grants the plugin id a capability, or takes it back, and
// reloads the plugins.
func (h *Host) SetGranted(id, capability string, on bool) error {
	return h.edit(func(p *prefs) {
		g := slices.DeleteFunc(slices.Clone(p.Grants[id]), func(s string) bool { return s == capability })
		if on {
			g = append(g, capability)
		}
		if p.Grants == nil {
			p.Grants = map[string][]string{}
		}
		p.Grants[id] = g
		if len(g) == 0 {
			delete(p.Grants, id)
		}
	})
}

func (h *Host) edit(change func(*prefs)) error {
	if err := h.save(change); err != nil {
		return err
	}
	h.Reload()
	return nil
}

// save changes plugins.json and what the host holds of it.
func (h *Host) save(change func(*prefs)) error {
	next, err := jsonfile.Edit(prefsPath(h.state), func(p *prefs) error {
		change(p)
		return nil
	})
	if err != nil {
		return err
	}
	h.mu.Lock()
	h.prefs = next
	h.mu.Unlock()
	return nil
}

// pageID is what a page's id may be: it is a part of the page's route.
var pageID = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9_.-]{0,63}$`)

// Entry is a page a plugin contributes, as the sidebar lists it.
type Entry struct {
	Plugin, Page string
	Title, Icon  string
}

// Entries are the pages the running plugins contribute, plugin by plugin
// in the order of their folders, each plugin's in the order it lists them.
func (h *Host) Entries() []Entry {
	var out []Entry
	for _, in := range h.running() {
		for _, n := range in.manifest.Pages() {
			if !pageID.MatchString(n.Page) {
				continue // a builtin page of MyGo Music's, which Glitched doesn't have, or a name no route takes
			}
			out = append(out, Entry{Plugin: in.manifest.ID, Page: n.Page, Title: n.Title, Icon: n.Icon})
		}
	}
	return out
}

// Command is a command a running plugin contributes, as the command
// palette lists it.
type Command struct {
	Plugin, PluginName string
	ID, Title          string
}

// Commands are the commands the running plugins contribute.
func (h *Host) Commands() []Command {
	var out []Command
	for _, in := range h.running() {
		for _, c := range in.manifest.Commands() {
			title := c.Title
			if title == "" {
				title = c.ID
			}
			out = append(out, Command{Plugin: in.manifest.ID, PluginName: in.manifest.Name, ID: c.ID, Title: title})
		}
	}
	return out
}

func (h *Host) running() []*installed {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return slices.DeleteFunc(slices.Clone(h.installed), func(in *installed) bool { return in.p == nil })
}

// Name is the plugin's name, or its id if it gives none.
func (h *Host) Name(id string) string {
	if p, _ := h.plugin(id); p != nil && p.Manifest().Name != "" {
		return p.Manifest().Name
	}
	return id
}

// HasView reports whether the plugin id draws its pages as native views.
func (h *Host) HasView(id string) bool {
	p, _ := h.plugin(id)
	return p != nil && p.Can("view")
}

// Page asks the plugin id for its stock page. It runs the plugin's code,
// up to its call timeout, so it belongs off the UI thread.
func (h *Host) Page(id, page string) (*plugin.Page, error) {
	p, err := h.plugin(id)
	if err != nil {
		return nil, err
	}
	return p.Page(page)
}

// Act does what an action of an item on the plugin id's stock page asks:
// a command runs the plugin's command named in its meta.command, with its
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
		return h.openURL(a.URL)
	}
	return fmt.Errorf("action %q is of a kind Glitched doesn't know: %q", a.ID, a.Kind)
}

// RunCommand runs the plugin id's command, off the UI thread, and the
// effects it answers, whose answers go nowhere. done gets what failed, on
// the UI thread.
func (h *Host) RunCommand(id, command string, done func(error)) {
	p, err := h.plugin(id)
	if err != nil {
		done(err)
		return
	}
	go func() {
		var out struct {
			Effects []Effect `json:"effects"`
		}
		err := p.Command(command, nil, &out)
		if err == nil && len(out.Effects) > MaxEffects {
			err = fmt.Errorf("command %s asked for %d effects, over %d", command, len(out.Effects), MaxEffects)
		}
		if err != nil {
			h.opts.Post(func() { done(err) })
			return
		}
		for _, e := range out.Effects {
			h.effect(id, p, e, nil, func(_ any, err error) {
				if err != nil {
					done(fmt.Errorf("%s: %w", e.Type, err))
				}
			})
		}
		h.opts.Post(func() {
			// Its app session hears it ran, to draw what the command changed.
			if s := h.sessions[pageKey{plugin: id}]; s != nil {
				s.Notify(AppEvent{Kind: "app", Type: "command", Data: map[string]string{"id": command}})
			}
			done(nil)
		})
	}()
}

// effect runs one effect the plugin id asked for, if it may, on the UI
// thread, and gives done its answer there.
//
// The checks run there too, as the effect runs: a grant taken back, or the
// plugin turned off or reloaded, while it waited, stops it.
//
// live, when set, says whether what asked for it still wants it: a page
// that started over or closed doesn't.
func (h *Host) effect(id string, from *plugin.Plugin, e Effect, live func() bool, done func(any, error)) {
	h.opts.Post(func() {
		if live != nil && !live() {
			return
		}
		if err := h.check(id, from, e.Type); err != nil {
			done(nil, err)
			return
		}
		if e.Type == "setting.status" {
			done(nil, h.setStatus(id, e.Args))
			return
		}
		if e.Type == "url.open" {
			var args struct {
				URL string `json:"url"`
			}
			if err := json.Unmarshal(e.Args, &args); err != nil {
				done(nil, err)
				return
			}
			done(nil, h.openURL(args.URL))
			return
		}
		if h.opts.Do == nil {
			done(nil, errors.New("this app runs no effects"))
			return
		}
		done(h.opts.Do(id, e))
	})
}

// check says why the plugin id's instance from may not run the effect, or
// nil.
func (h *Host) check(id string, from *plugin.Plugin, effect string) error {
	need, known := Effects[effect]
	h.mu.RLock()
	defer h.mu.RUnlock()
	var in *installed
	for _, i := range h.installed {
		if i.p != nil && i.p == from {
			in = i
		}
	}
	switch {
	case in == nil:
		return fmt.Errorf("%w: plugin %s stopped", ErrDenied, id)
	case !known:
		return fmt.Errorf("unknown effect %q", effect)
	case need != "" && !slices.Contains(in.manifest.Capabilities, need):
		return fmt.Errorf("%w: %s needs the %s capability, which the plugin doesn't declare", ErrDenied, effect, need)
	case need != "" && !h.allowed(id, need):
		return fmt.Errorf("%w: %s needs the %s capability, which isn't granted in Settings › Plugins", ErrDenied, effect, need)
	}
	return nil
}

func (h *Host) openURL(u string) error {
	if !webURL(u) {
		return fmt.Errorf("%q is not a web address", u)
	}
	if h.opts.OpenURL == nil {
		return errors.New("this app opens no web pages")
	}
	return h.opts.OpenURL(u)
}

// Close stops every plugin's pages and runtime.
func (h *Host) Close() { h.closeAll() }

func (h *Host) closeAll() {
	for k, s := range h.sessions {
		s.close()
		delete(h.sessions, k)
	}
	h.mu.Lock()
	list := h.installed
	h.installed = nil
	h.mu.Unlock()
	for _, in := range list {
		if in.p != nil {
			// Off the UI thread: Close waits for a runtime that is still
			// starting, up to its load timeout.
			go func() {
				in.p.Close()
				if in.db != nil {
					in.db.close()
				}
			}()
		}
	}
}

// errNotHere is a plugin this host doesn't run: a service host runs only
// plugins with a service or tools.
var errNotHere = errors.New("not run here")

// running1 is the plugin id, if it runs.
func (h *Host) running1(id string) *installed {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, in := range h.installed {
		if in.p != nil && in.manifest.ID == id {
			return in
		}
	}
	return nil
}

func (h *Host) plugin(id string) (*plugin.Plugin, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, in := range h.installed {
		if in.p != nil && in.manifest.ID == id {
			return in.p, nil
		}
	}
	return nil, fmt.Errorf("no plugin %q is running", id)
}

// webURL reports whether s is an http or https address. A plugin must not
// hand the system a file: address or another app's scheme.
func webURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}
