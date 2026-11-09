package plugins

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
)

// ThreadRecord is a thread as a plugin sees it. A field the plugin wasn't
// granted is absent, never an error: without threads:read, a record holds
// its id alone.
type ThreadRecord struct {
	ID        string `json:"id"`
	Title     string `json:"title,omitempty"`
	Workspace string `json:"workspace,omitempty"`
	Status    string `json:"status,omitempty"`
	Machine   string `json:"machine,omitempty"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

// Record is the thread as the plugin id may see it.
func (h *Host) Record(id string, full ThreadRecord) ThreadRecord {
	if h.Allowed(id, "threads:read") {
		return full
	}
	return ThreadRecord{ID: full.ID}
}

// Allowed reports whether the running plugin id declared the capability
// and, if it needs one, has the user's grant.
func (h *Host) Allowed(id, capability string) bool {
	in := h.running1(id)
	if in == nil {
		return false
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return slices.Contains(in.manifest.Capabilities, capability) && h.allowed(id, capability)
}

// Emit hands an app event to every running plugin that hears it and may:
// data gives what each plugin sees of it, or nil to tell that one
// nothing.
func (h *Host) Emit(event string, data func(plugin string) any) {
	need := Events[event]
	for _, in := range h.running() {
		id := in.manifest.ID
		if !slices.Contains(in.ext.Events, event) || need != "" && !h.Allowed(id, need) {
			continue
		}
		d := data(id)
		if d == nil {
			continue
		}
		if s := h.App(id); s != nil {
			s.Notify(AppEvent{Kind: "app", Type: event, Data: d})
		}
	}
}

// Ext is the running plugin id's glitched block.
func (h *Host) Ext(id string) Extension {
	if in := h.running1(id); in != nil {
		return in.ext
	}
	return Extension{}
}

// SettingState is one of a plugin's settings, as Settings › Plugins draws
// it: what it is, its value now, and what the plugin says of it.
type SettingState struct {
	Setting
	Value  any
	Status *SettingStatus
}

// Settings are the plugin id's settings, in the order it declares them.
func (h *Host) Settings(id string) []SettingState {
	in := h.running1(id)
	if in == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	var out []SettingState
	for _, s := range in.ext.Settings {
		st := SettingState{Setting: s, Value: s.Value(h.prefs.Settings[id][s.ID])}
		if v, ok := h.statuses[id][s.ID]; ok {
			st.Status = &v
		}
		out = append(out, st)
	}
	return out
}

// SetSetting sets the plugin id's setting, as only the user does, and
// tells the plugin if it hears setting.changed.
func (h *Host) SetSetting(id, key string, value any) error {
	in := h.running1(id)
	if in == nil {
		return fmt.Errorf("no plugin %q is running", id)
	}
	i := slices.IndexFunc(in.ext.Settings, func(s Setting) bool { return s.ID == key })
	if i < 0 {
		return fmt.Errorf("plugin %s has no setting %q", id, key)
	}
	s := in.ext.Settings[i]
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	v, err := s.Parse(raw)
	if err != nil {
		return err
	}
	was := h.settingValue(id, s)
	raw, _ = json.Marshal(v)
	if err := h.save(func(p *prefs) {
		if p.Settings == nil {
			p.Settings = map[string]map[string]json.RawMessage{}
		}
		if p.Settings[id] == nil {
			p.Settings[id] = map[string]json.RawMessage{}
		}
		p.Settings[id][key] = raw
	}); err != nil {
		return err
	}
	h.Emit("setting.changed", func(p string) any {
		if p != id {
			return nil
		}
		return map[string]any{"id": key, "value": v, "was": was}
	})
	return nil
}

func (h *Host) settingValue(id string, s Setting) any {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return s.Value(h.prefs.Settings[id][s.ID])
}

// settingText is host.setting(key): the value as text, "" for a setting
// the plugin doesn't declare.
func (h *Host) settingText(id, key string) string {
	in := h.running1(id)
	if in == nil {
		return ""
	}
	i := slices.IndexFunc(in.ext.Settings, func(s Setting) bool { return s.ID == key })
	if i < 0 {
		return ""
	}
	switch v := h.settingValue(id, in.ext.Settings[i]).(type) {
	case string:
		return v
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

// SettingValues are every setting of the plugin id, by id.
func (h *Host) SettingValues(id string) map[string]any {
	out := map[string]any{}
	for _, s := range h.Settings(id) {
		out[s.ID] = s.Value
	}
	return out
}

// setStatus is the setting.status effect: what the plugin says of how a
// setting stands, or nil to clear it.
func (h *Host) setStatus(id string, args json.RawMessage) error {
	var a struct {
		Setting string         `json:"setting"`
		Status  *SettingStatus `json:"status"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return err
	}
	in := h.running1(id)
	if in == nil || !slices.ContainsFunc(in.ext.Settings, func(s Setting) bool { return s.ID == a.Setting }) {
		return fmt.Errorf("no setting %q", a.Setting)
	}
	if a.Status != nil && (!slices.Contains(tones, a.Status.Tone) || len(a.Status.Badge) > 40 || len(a.Status.Text) > 400) {
		return fmt.Errorf("a status is a badge of up to 40 bytes in tone %v, and up to 400 bytes of text", tones[1:])
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.statuses == nil {
		h.statuses = map[string]map[string]SettingStatus{}
	}
	if h.statuses[id] == nil {
		h.statuses[id] = map[string]SettingStatus{}
	}
	if a.Status == nil {
		delete(h.statuses[id], a.Setting)
	} else {
		h.statuses[id][a.Setting] = *a.Status
	}
	return nil
}

// PlaceAt is a place of a running plugin's at a slot.
type PlaceAt struct {
	Plugin string
	Place  Place
}

// PlacesAt are the running plugins' places at the slot, in plugin order.
func (h *Host) PlacesAt(at string) []PlaceAt {
	var out []PlaceAt
	for _, in := range h.running() {
		for _, p := range in.ext.Places {
			if p.At == at {
				out = append(out, PlaceAt{in.manifest.ID, p})
			}
		}
	}
	return out
}

// ShortcutCommand is a command a running plugin binds to a key.
type ShortcutCommand struct {
	Plugin, PluginName, Command string
	Key                         Key
}

// Shortcuts are the running plugins' shortcuts, in plugin order.
func (h *Host) Shortcuts() []ShortcutCommand {
	var out []ShortcutCommand
	for _, in := range h.running() {
		for _, s := range in.ext.Shortcuts {
			k, err := parseKey(s.Key)
			if err != nil {
				continue
			}
			out = append(out, ShortcutCommand{in.manifest.ID, in.manifest.Name, s.Command, k})
		}
	}
	return out
}

// Folder is the folder the running plugin id runs from: the read-only
// snapshot of its signed files. "" if it doesn't run.
func (h *Host) Folder(id string) string {
	if in := h.running1(id); in != nil {
		return in.code
	}
	return ""
}

// Fetcher is what reaches the network for the running plugin id, as its
// fetch does: nil without the network grant.
func (h *Host) Fetcher(id string) *http.Client {
	in := h.running1(id)
	if in == nil || len(in.manifest.Permissions.Network) == 0 {
		return nil
	}
	h.mu.RLock()
	ok := h.granted(id, Network)
	h.mu.RUnlock()
	if !ok {
		return nil
	}
	c, err := httpClient(in.manifest.Permissions.Network)
	if err != nil {
		return nil
	}
	return c
}
