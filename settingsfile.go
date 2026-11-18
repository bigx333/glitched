package main

import (
	"encoding/json"
	"log"
	"maps"
	"path/filepath"
	"reflect"
	"slices"
	"sync"

	"glitched/internal/jsonfile"
)

// settingsFile is settings.json in the state directory: what the Settings
// pages edit, by name. The default model and access are composer.json's,
// the servers gui.json's and This machine's engine.json's.
type settingsFile struct {
	Theme         string   `json:"theme"`
	Accent        string   `json:"accent"`
	Glass         bool     `json:"glass"`
	GlassTint     float64  `json:"glassTint"`
	TintPanels    bool     `json:"tintPanels"`
	Wind          bool     `json:"wind"`
	FontSize      float64  `json:"fontSize"`
	Timestamps    string   `json:"timestamps"`
	ConfirmDelete bool     `json:"confirmDelete"`
	Streaming     bool     `json:"streaming"`
	NewThread     string   `json:"newThread"`
	Notifications bool     `json:"notifications"`
	ClaudeEnabled bool     `json:"claudeEnabled"`
	ClaudePath    string   `json:"claudePath"`
	ClaudeModel   string   `json:"claudeModel"`
	ClaudeCustom  []string `json:"claudeCustom"`
	CodexEnabled  bool     `json:"codexEnabled"`
	CodexPath     string   `json:"codexPath"`
	CodexModel    string   `json:"codexModel"`
	CodexCustom   []string `json:"codexCustom"`
}

var (
	timeFormats    = []string{"Relative", "12-hour", "24-hour"}
	newThreadModes = []string{"Local", "New worktree"}
	claudeModels   = []string{"Claude Opus 5.5", "Claude Sonnet 5.5", "Claude Haiku 5.5"}
	codexModels    = []string{"GPT-5.5 Codex", "GPT-5.5", "GPT-5.5 mini"}
)

// fileOf is s as settings.json holds it. Its custom models are s's own.
func fileOf(s settings) settingsFile {
	return settingsFile{
		Theme:         s.theme,
		Accent:        accents[s.accent].name,
		Glass:         s.glass,
		GlassTint:     s.vibrancy,
		TintPanels:    s.tintPanels,
		Wind:          s.wind,
		FontSize:      s.fontSize,
		Timestamps:    s.timeFormat,
		ConfirmDelete: s.confirmDelete,
		Streaming:     s.streaming,
		NewThread:     newThreadModes[s.newThreadMode],
		Notifications: s.notifications,
		ClaudeEnabled: s.claudeEnabled,
		ClaudePath:    s.claudePath,
		ClaudeModel:   s.claudeModel,
		ClaudeCustom:  s.claudeCustom,
		CodexEnabled:  s.codexEnabled,
		CodexPath:     s.codexPath,
		CodexModel:    s.codexModel,
		CodexCustom:   s.codexCustom,
	}
}

// settingsFrom is s with what f holds that the pages could have set: a
// value they don't offer, as a theme since gone or a size off the slider,
// keeps s's.
func settingsFrom(s settings, f settingsFile) settings {
	pick := func(v *string, names []string, name string) {
		if slices.Contains(names, name) {
			*v = name
		}
	}
	within := func(v *float64, lo, hi, x float64) {
		if x >= lo && x <= hi {
			*v = x
		}
	}
	pick(&s.theme, themeNames(), f.Theme)
	for i, acc := range accents {
		if acc.name == f.Accent {
			s.accent = i
		}
	}
	s.glass = f.Glass
	within(&s.vibrancy, 0, 1, f.GlassTint)
	s.tintPanels = f.TintPanels
	s.wind = f.Wind
	within(&s.fontSize, 11, 16, f.FontSize)
	pick(&s.timeFormat, timeFormats, f.Timestamps)
	s.confirmDelete = f.ConfirmDelete
	s.streaming = f.Streaming
	if i := slices.Index(newThreadModes, f.NewThread); i >= 0 {
		s.newThreadMode = i
	}
	s.notifications = f.Notifications
	s.claudeEnabled, s.claudePath, s.claudeCustom = f.ClaudeEnabled, f.ClaudePath, f.ClaudeCustom
	pick(&s.claudeModel, claudeModels, f.ClaudeModel)
	s.codexEnabled, s.codexPath, s.codexCustom = f.CodexEnabled, f.CodexPath, f.CodexCustom
	pick(&s.codexModel, codexModels, f.CodexModel)
	return s
}

func (a *app) settingsPath() string { return filepath.Join(a.dir, "settings.json") }

// loadSettings takes the settings an app last saved. A setting the file
// lacks, as an older file does, or holds as the wrong kind of value keeps
// its default; a file that doesn't parse leaves them all.
func (a *app) loadSettings() {
	f := fileOf(a.settings)
	if err := jsonfile.Load(a.settingsPath(), &f); err != nil {
		log.Printf("glitched: settings: %v", err)
	}
	a.settings = settingsFrom(a.settings, f)
	if f.Theme != "" && f.Theme != a.settings.theme {
		a.pendingTheme = f.Theme
	}
	a.keepSaved()
}

// keepSaved notes the settings as saved, apart from the slices the pages
// edit in place.
func (a *app) keepSaved() {
	a.savedSettings = fileOf(a.settings)
	a.savedSettings.ClaudeCustom = slices.Clone(a.savedSettings.ClaudeCustom)
	a.savedSettings.CodexCustom = slices.Clone(a.savedSettings.CodexCustom)
}

// saveSettings hands what changed since the last save to the writer,
// which keeps it for the next start off the UI thread: its fsync would
// stall the frame.
func (a *app) saveSettings() {
	now := fileOf(a.settings)
	if reflect.DeepEqual(now, a.savedSettings) {
		return
	}
	had, all := fields(a.savedSettings), fields(now)
	changed := maps.Clone(all)
	maps.DeleteFunc(changed, func(k string, v json.RawMessage) bool { return string(had[k]) == string(v) })
	a.keepSaved()
	a.settingsOut.save(a.settingsPath(), changed, all)
}

// fields is f as settings.json's keys and their values.
func fields(f settingsFile) map[string]json.RawMessage {
	b, err := json.Marshal(f)
	var m map[string]json.RawMessage
	if err == nil {
		err = json.Unmarshal(b, &m)
	}
	if err != nil {
		panic(err) // a settingsFile holds nothing JSON can't
	}
	return m
}

// settingsWriter writes settings.json on a goroutine of its own, in the
// order the saves came. Under the file's lock it puts only the settings
// that changed here onto what the file holds, so another window on the
// state directory keeps what it chose, and a key this app doesn't know,
// as a newer one's, stays. A file that doesn't parse, or none, takes every
// setting. A write that fails is tried again with the next save.
type settingsWriter struct {
	wg sync.WaitGroup

	mu      sync.Mutex
	path    string
	changed map[string]json.RawMessage // what no write has kept yet
	all     map[string]json.RawMessage // every setting, as last saved
	running bool
}

func (w *settingsWriter) save(path string, changed, all map[string]json.RawMessage) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.path = path
	w.changed = merged(w.changed, changed)
	w.all = all
	if !w.running {
		w.running = true
		w.wg.Go(w.drain)
	}
}

// merged is newer put onto older.
func merged(older, newer map[string]json.RawMessage) map[string]json.RawMessage {
	if older == nil {
		older = map[string]json.RawMessage{}
	}
	maps.Copy(older, newer)
	return older
}

func (w *settingsWriter) drain() {
	for {
		w.mu.Lock()
		path, changed, all := w.path, w.changed, w.all
		w.changed = nil
		if changed == nil {
			w.running = false
			w.mu.Unlock()
			return
		}
		w.mu.Unlock()
		if err := writeSettings(path, changed, all); err != nil {
			log.Printf("glitched: settings: %v", err)
			w.mu.Lock()
			w.changed = merged(changed, w.changed)
			w.running = false
			w.mu.Unlock()
			return
		}
	}
}

func writeSettings(path string, changed, all map[string]json.RawMessage) error {
	_, err := jsonfile.EditOrReset(path, func(f *map[string]json.RawMessage) error {
		if *f == nil {
			*f = maps.Clone(all)
		}
		*f = merged(*f, changed)
		return nil
	})
	return err
}

// wait returns once every save so far is written, or its write failed.
func (w *settingsWriter) wait() { w.wg.Wait() }
