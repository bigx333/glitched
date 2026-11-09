package plugins

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

// Capability is a part of Glitched a plugin may use. A plugin declares the
// ones it uses in plugin.json's capabilities; it gets none it doesn't
// declare, and those that Grant marks only once the user grants them in
// Settings › Plugins.
type Capability struct {
	Name  string
	Grant bool
	About string
}

// Capabilities are every capability of this contract version. A plugin
// that declares another doesn't load.
var Capabilities = []Capability{
	{"clipboard", false, "Copy text to the clipboard"},
	{"notify", false, "Show system notifications"},
	{"composer", false, "Open a new thread's composer with a draft, for you to send"},
	{"threads:read", true, "Read your threads' titles, workspaces and status, and show a thread"},
	{"threads:write", true, "Start threads and send messages to them, which runs agents"},
}

// Network is the grant that lets a plugin reach the hosts its
// permissions.network names.
const Network = "network"

// Effects maps each effect to the capability it needs, "" for none.
var Effects = map[string]string{
	"toast":           "",
	"url.open":        "",
	"page.open":       "",
	"clipboard.write": "clipboard",
	"notify":          "notify",
	"composer.draft":  "composer",
	"threads.list":    "threads:read",
	"threads.open":    "threads:read",
	"threads.create":  "threads:write",
	"threads.send":    "threads:write",
}

// MaxEffects is how many effects one update or command may ask for.
const MaxEffects = 16

// Effect is something a plugin asks the app to do, which only the app
// can: Type names it, ID, when set, names the answer that comes back to
// the plugin's update, and Args are the rest of its fields.
type Effect struct {
	Type string
	ID   string
	Args json.RawMessage
}

// UnmarshalJSON reads an effect as one object: {"type": ..., "id": ...,
// and its own fields}.
func (e *Effect) UnmarshalJSON(b []byte) error {
	var head struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	}
	if err := json.Unmarshal(b, &head); err != nil {
		return err
	}
	if len(head.ID) > 128 {
		return errors.New("an effect's id is at most 128 bytes")
	}
	e.Type, e.ID, e.Args = head.Type, head.ID, append(json.RawMessage(nil), b...)
	return nil
}

// ErrDenied is wrapped by the error of an effect the plugin may not ask
// for.
var ErrDenied = errors.New("permission denied")

func capability(name string) (Capability, bool) {
	i := slices.IndexFunc(Capabilities, func(c Capability) bool { return c.Name == name })
	if i < 0 {
		return Capability{}, false
	}
	return Capabilities[i], true
}

// checkDeclared refuses a manifest that declares a capability this
// contract doesn't have: an unknown one is an error, not ignored.
func checkDeclared(declared []string) error {
	for _, name := range declared {
		if name == Network {
			continue
		}
		if _, ok := capability(name); !ok {
			return fmt.Errorf("unknown capability %q", name)
		}
	}
	return nil
}
