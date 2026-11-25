// Package plugins hosts the plugins installed in Glitched, with
// github.com/yxxbc/mygo-plugin. A plugin is a plugin.json and a JavaScript
// or TypeScript module, run in a runtime of its own. It contributes pages
// and commands. A page is either a stock mygo-plugin page (a page export
// answering sections and items) or a native view: a widget tree (package
// widget) that the plugin's view export answers from state the host keeps,
// and its update export changes as the user acts on it. What a plugin may
// do in the app beyond that, it asks for with glitched's calls, which the
// host checks against the capabilities it declared and the user granted.
//
// docs/plugins.md is the contract.
package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
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
	ContractVersion = 2
)

// ReplaceVersion is the contract version that brought replacing
// components: a plugin that replaces one names it or later.
const ReplaceVersion = 2

// Options are what the app gives its plugins.
type Options struct {
	// AppVersion is Glitched's version, which a plugin's minAppVersion is
	// checked against.
	AppVersion string

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
	// IdleTimeout stops a runtime nothing called for that long; its next
	// call starts it again. Zero keeps it.
	IdleTimeout time.Duration
	// WaitToStart leaves a plugin with timers asleep until Start: the
	// window sets the language first, which it can only once it runs.
	WaitToStart bool
	// SignOwn signs the user's own plugins again as they change. The
	// window's host does; the engine's leaves it to the window, so the two
	// never sign one folder at once.
	SignOwn bool
	// Reloaded is told, on the UI thread, after a plugin's code is
	// reloaded, so that what the app keeps of it goes.
	Reloaded func(id string)
	// Run runs a program the plugin id may, as glitched.run asks: its
	// manifest's programs are allowed, and the denied folders are kept
	// from it. Nil runs it with RunProgram.
	Run func(ctx context.Context, id string, allowed, denied []string, args json.RawMessage) (any, error)
	// ServiceCall calls a method of the plugin id's service, as
	// glitched.service.call asks. Nil reaches none.
	ServiceCall func(ctx context.Context, id, method string, args json.RawMessage) (any, error)
	// Ask asks a model for the plugin id, as glitched.models.ask does,
	// with the host's limits checked. Nil answers none.
	Ask func(ctx context.Context, id string, q AskRequest) (AskAnswer, error)
	// Bundled holds the plugins built into the app, a folder each at its
	// root. Each is verified as a plugin in the plugins folder is, and
	// runs when signed by one of ReleaseKeys (base64 public keys), with
	// the capabilities it declares granted till the user takes one back.
	// A folder of the user's with the same id is run in its place.
	Bundled     fs.FS
	ReleaseKeys []string
	// BundledDir is where the built-ins are written out, BundledDir(state)
	// when "": the engine's services write theirs apart from the window's,
	// which may write at the same time.
	BundledDir string

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
	// commands counts the commands under way, till each one's done runs.
	commands atomic.Int32

	mu        sync.RWMutex // guards installed and prefs, which a session's worker reads
	installed []*installed
	prefs     prefs
	// builtins are the capabilities each running built-in plugin
	// declares, which it has unless revoked.
	builtins map[string][]string
	// failed is OnFailure's.
	failed   atomic.Pointer[func(plugin string, builtin bool, err error)]
	statuses map[string]map[string]SettingStatus

	sessions map[pageKey]*Session
	// started is set once the app has started its plugins (Start): what
	// they read of the app, as the language, is set by then.
	started  bool
	language atomic.Value // the user's language, a string, as the app last set it
	// stopping are the runtimes and databases on their way to stop, which
	// Close and Remove wait for.
	stopping sync.WaitGroup
	// asking holds the plugins with an ask under way, by id: one each.
	asking sync.Map
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
	alive      *atomic.Bool // the running runtime's, cleared as it goes
	// awaiting counts the running runtime's app calls under way, which
	// wait on the UI thread.
	awaiting *atomic.Int32

	// bundled is built into the app; shadowedBy, a bundled one's, is the
	// user's folder with its id, which runs in its place.
	bundled    bool
	shadowedBy string
	// fallback is a built-in that a copy shadows, run only to draw the
	// components the copy can't: no pages, commands, slots or events.
	fallback bool
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
	// Revoked are the capabilities the user took back from a built-in
	// plugin, which has the rest it declares without asking.
	Revoked map[string][]string `json:"revoked,omitempty"`
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
	h.language.Store("en")
	h.started = !o.WaitToStart
	migrateRenamedPrefs(stateDir, o.Logf)
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
	seen := map[string]*installed{}
	builtins := map[string][]string{}
	shadows := shadowRenames(h.state, entries)
	h.mu.Lock()
	h.builtins = builtins // what load asks of a built-in's grants, as it loads
	h.mu.Unlock()
	add := func(in *installed) {
		list = append(list, in)
		m, err := plugin.ReadManifest(in.folder)
		if err != nil {
			in.err = err
			return
		}
		in.manifest = *m
		in.raw, _ = os.ReadFile(filepath.Join(in.folder, "plugin.json"))
		in.enabled = !slices.Contains(h.prefs.Disabled, m.ID)
		first := seen[m.ID]
		shadow := first
		if shadow == nil && in.bundled {
			if oldID := shadows[m.ID]; oldID != "" {
				shadow = seen[oldID]
			}
		}
		switch {
		case shadow != nil && in.bundled:
			// The user's copy runs in its place; it still loads, under an id
			// of its own, to draw the components the copy fails to.
			in.shadowedBy, in.enabled, in.fallback = shadow.folder, false, true
			in.manifest.ID += FallbackSuffix
			h.mu.Lock()
			builtins[in.manifest.ID] = builtinCaps(*m)
			h.mu.Unlock()
			if in.p, in.err = h.load(in); errors.Is(in.err, errNotHere) {
				in.err = nil
			}
			return
		case first != nil:
			in.err = fmt.Errorf("the id %q is already installed", m.ID)
			in.enabled = false
			return
		}
		seen[m.ID] = in
		if in.bundled {
			h.mu.Lock()
			builtins[m.ID] = builtinCaps(*m)
			h.mu.Unlock()
		}
		if in.enabled {
			in.p, in.err = h.load(in)
			if errors.Is(in.err, errNotHere) {
				in.err = nil
			}
		}
	}
	for _, e := range entries {
		folder := filepath.Join(Dir(h.state), e.Name())
		if fi, err := os.Stat(folder); err != nil || !fi.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue // a folder, or a link to one, as while developing
		}
		add(&installed{folder: folder})
	}
	for _, folder := range h.writeBundled() {
		add(&installed{folder: folder, bundled: true})
	}
	for _, in := range list {
		if in.err != nil {
			h.opts.Logf("plugins: %s: %v", filepath.Base(in.folder), in.err)
		}
	}
	h.mu.Lock()
	h.installed, h.builtins = list, builtins
	h.mu.Unlock()
	// A window's plugin with timers wakes on them though it has no place
	// shown yet: its app session starts now, once the app started plugins.
	if h.opts.Main == nil && h.started {
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
	if x.replaces() && m.ContractVersion < ReplaceVersion {
		return nil, fmt.Errorf("it replaces a component, which needs contractVersion %d", ReplaceVersion)
	}
	for _, mn := range x.Menus {
		if !slices.ContainsFunc(m.Commands(), func(c plugin.CommandContribution) bool { return c.ID == mn.Command }) {
			return nil, fmt.Errorf("glitched.menus: %q is none of its commands", mn.Command)
		}
	}
	for _, id := range x.SettingsPages {
		if !slices.ContainsFunc(m.Pages(), func(p plugin.Navigation) bool { return p.Page == id }) {
			return nil, fmt.Errorf("glitched.settingsPages: %q is none of its pages", id)
		}
	}
	in.ext = x
	code, err := h.verify(in)
	if err != nil {
		return nil, err
	}
	in.code = code
	main := ""
	if h.opts.Main != nil {
		if main = h.opts.Main(m, x); main == "" {
			return nil, errNotHere
		}
		// The service module runs only once service is granted; till then
		// a plugin with tools answers them from its main module.
		if main == x.Service && !h.allowed(m.ID, "service") {
			// A main module that is the service runs nothing till then:
			// its tools wait with it.
			if len(x.Tools) == 0 || sameFile(in.folder, x.Service, m.Main) {
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
		// Asked live: a grant holds at once, with no restart.
		// By the id the host knows it as, which a built-in shadowed by a
		// copy has of its own.
		Capability: func(_, name string) bool {
			if name == Network {
				return len(m.Permissions.Network) > 0 && h.grantedNow(m.ID, Network)
			}
			return slices.Contains(m.Capabilities, name) && h.allowedNow(m.ID, name)
		},
		CallTimeout: h.opts.CallTimeout,
		IdleTimeout: h.opts.IdleTimeout,
		MemoryLimit: h.opts.MemoryLimit,
		Allowed:     func(string, string) bool { return h.grantedNow(m.ID, Network) },
		// The runtime's own callbacks run on its loop: none waits on the UI
		// thread, which may itself be waiting for the runtime to stop.
		Host: plugin.Host{
			Language: func() string { return h.language.Load().(string) },
			OpenURL: func(u string) error {
				if h.opts.OpenURL == nil {
					return errors.New("this app opens no web pages")
				}
				go h.opts.OpenURL(u)
				return nil
			},
			Logf:    h.opts.Logf,
			Setting: h.settingText,
		},
	})
}

// granted reports whether the user granted the plugin id the capability:
// a built-in plugin has those it declares, till the user takes one back.
func (h *Host) granted(id, capability string) bool {
	if declared, ok := h.builtins[id]; ok {
		return slices.Contains(declared, capability) && !slices.Contains(h.prefs.Revoked[strings.TrimSuffix(id, FallbackSuffix)], capability)
	}
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
	// Builtin is built into the app. ShadowedBy, a built-in one's, is the
	// folder of the user's copy that runs in its place; Shadows says this
	// is such a copy, which Revert takes away.
	Builtin    bool
	ShadowedBy string
	Shadows    bool
	// Replaces are the components it draws in place of the built-in.
	Replaces []string
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
		p.Builtin, p.ShadowedBy = in.bundled, in.shadowedBy
		p.Shadows = !in.bundled && slices.ContainsFunc(h.installed, func(b *installed) bool { return b.shadowedBy == in.folder })
		for _, pl := range in.ext.Places {
			if pl.Replaces {
				p.Replaces = append(p.Replaces, pl.At)
			}
		}
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
//
// A grant holds at once: a plugin's runtime asks its grants as it uses
// them, so it goes on, with what its pages hold.
func (h *Host) SetGranted(id, capability string, on bool) error {
	defer h.tellGrant(id, capability, on)
	h.mu.RLock()
	_, builtin := h.builtins[id]
	h.mu.RUnlock()
	if builtin {
		return h.save(func(p *prefs) {
			r := slices.DeleteFunc(slices.Clone(p.Revoked[id]), func(s string) bool { return s == capability })
			if !on {
				r = append(r, capability)
			}
			if p.Revoked == nil {
				p.Revoked = map[string][]string{}
			}
			p.Revoked[id] = r
			if len(r) == 0 {
				delete(p.Revoked, id)
			}
		})
	}
	return h.save(func(p *prefs) {
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

// tellGrant tells the plugin id's pages, tabs and app session that a
// grant of its changed, so that what they show follows at once.
func (h *Host) tellGrant(id, capability string, on bool) {
	for k, s := range h.sessions {
		if k.plugin == id {
			s.Notify(AppEvent{Kind: "app", Type: "grant.changed", Data: map[string]any{"capability": capability, "granted": on}})
		}
	}
}

// Start wakes the plugins with timers, for a host opened to wait to.
func (h *Host) Start() {
	if !h.started {
		h.started = true
		h.Reload()
	}
}

// SetLanguage sets the user's language, a BCP 47 tag, which plugins read
// with host.language(). The app sets it on the UI thread, where asking it
// waits on nothing; a plugin's runtime only reads it.
func (h *Host) SetLanguage(tag string) {
	if tag != "" {
		h.language.Store(tag)
	}
}

// grantedNow and allowedNow are granted and allowed, from a goroutine of
// the plugin's.
func (h *Host) grantedNow(id, capability string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.granted(id, capability)
}

func (h *Host) allowedNow(id, capability string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.allowed(id, capability)
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
	// Settings lists it in Settings' nav.
	Settings bool
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
			out = append(out, Entry{Plugin: in.manifest.ID, Page: n.Page, Title: n.Title, Icon: n.Icon, Settings: slices.Contains(in.ext.SettingsPages, n.Page)})
		}
	}
	return out
}

// MenuItem is a plugin's command in one of Glitched's menus.
type MenuItem struct {
	Plugin, Command, Title, Icon string
	Requires                     []string
}

// MenuItems are the commands the running plugins put in the menu at, in
// the order of their folders.
func (h *Host) MenuItems(at string) []MenuItem {
	var out []MenuItem
	for _, in := range h.running() {
		for _, m := range in.ext.Menus {
			if m.At != at {
				continue
			}
			title := m.Command
			for _, c := range in.manifest.Commands() {
				if c.ID == m.Command && c.Title != "" {
					title = c.Title
				}
			}
			out = append(out, MenuItem{in.manifest.ID, m.Command, title, m.Icon, m.Requires})
		}
	}
	return out
}

// Themes are the looks the running plugins add, in the order of their
// folders.
func (h *Host) Themes() []Theme {
	var out []Theme
	for _, in := range h.running() {
		out = append(out, in.ext.Themes...)
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
	return slices.DeleteFunc(slices.Clone(h.installed), func(in *installed) bool { return in.p == nil || in.fallback })
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

// RunCommand runs the plugin id's command, off the UI thread; what it
// does in the app, it does with glitched's calls. done gets what failed,
// on the UI thread.
func (h *Host) RunCommand(id, command string, args map[string]any, done func(error)) {
	p, err := h.plugin(id)
	if err != nil {
		done(err)
		return
	}
	h.commands.Add(1)
	go func() {
		err := p.Command(command, args, nil)
		h.opts.Post(func() {
			defer h.commands.Add(-1)
			// Its app session and pages hear it ran, to draw what the
			// command changed; its tabs, one per thread, don't.
			for k, s := range h.sessions {
				if err == nil && k.plugin == id && k.scope == "" {
					s.Notify(AppEvent{Kind: "app", Type: "command", Data: map[string]string{"id": command}})
				}
			}
			done(err)
		})
	}()
}

// effect runs what the plugin id asked the app for, if it may, on the UI
// thread, and gives done its answer there. The checks run there too, as
// it runs: a grant taken back while it waited stops it. ctx is the
// runtime's that asked: one that stopped meanwhile, as a plugin turned
// off or reloaded, gets nothing done.
func (h *Host) effect(ctx context.Context, alive *atomic.Bool, id string, e Effect, done func(any, error)) {
	h.opts.Post(func() {
		if ctx.Err() != nil || !alive.Load() {
			done(nil, fmt.Errorf("%w: the plugin's code was replaced or stopped", ErrDenied))
			return
		}
		if err := h.check(id, e); err != nil {
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
		v, err := h.opts.Do(id, e)
		if later, ok := v.(Pending); ok && err == nil {
			later(done)
			return
		}
		done(v, err)
	})
}

// check says why the running plugin id may not ask the app for effect
// e, or nil.
func (h *Host) check(id string, e Effect) error {
	_, known := Effects[e.Type]
	in := h.running1(id)
	h.mu.RLock()
	defer h.mu.RUnlock()
	switch {
	case in == nil:
		return fmt.Errorf("%w: plugin %s stopped", ErrDenied, id)
	case !known:
		return fmt.Errorf("unknown effect %q", e.Type)
	}
	for _, need := range needs(e) {
		switch {
		case need != "" && !slices.Contains(in.manifest.Capabilities, need):
			return fmt.Errorf("%w: %s needs the %s capability, which the plugin doesn't declare", ErrDenied, e.Type, need)
		case need != "" && !h.allowed(id, need):
			return fmt.Errorf("%w: %s needs the %s capability, which isn't granted in Settings › Plugins", ErrDenied, e.Type, need)
		}
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

// Close stops every plugin's pages and runtime, and waits for them, and
// for those reloads stopped: their databases are closed once it returns.
func (h *Host) Close() {
	h.closeAll()
	h.stopping.Wait()
}

// closeAll stops every plugin's pages and runtime, off the UI thread: a
// runtime still starting takes up to its load timeout to stop.
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
		if in.alive != nil {
			in.alive.Store(false) // its calls waiting on the UI thread do nothing
		}
		if in.p != nil {
			h.stopping.Go(func() {
				in.p.Shutdown()
				if in.db != nil {
					in.db.close()
				}
			})
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

// sameFile reports whether two paths of a plugin's folder are one file:
// spelled alike, or alike on a disk that ignores case, or hard links.
func sameFile(folder, a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	fa, err1 := os.Stat(filepath.Join(folder, a))
	fb, err2 := os.Stat(filepath.Join(folder, b))
	return err1 == nil && err2 == nil && os.SameFile(fa, fb)
}
