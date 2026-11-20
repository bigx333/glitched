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
	{"threads:write", true, "Start threads and send messages to them, which runs agents, and change the model they run at"},
	{"threads:access", true, "Set what agents may do in a thread, up to Full access to your computer and the internet, and draw the control that sets it"},
	{"messages:read", true, "Read the messages of the thread you have open"},
	{"ui.place", false, "Put widgets in Glitched's own views: thread rows, headers, panel tabs"},
	{"ui.replace", true, "Draw a part of Glitched's own UI in place of the built-in one"},
	{"tools", false, "Offer tools to Glitched's agents"},
	{"programs", true, "Run the programs it names, as you"},
	{"service", true, "Run in the background, with no window"},
	{"settings:write", true, "Change Glitched's settings: the theme, the accent, defaults for new threads and how the app behaves"},
}

// Network is the grant that lets a plugin reach the hosts its
// permissions.network names.
const Network = "network"

// Effects maps each of the app's calls to the capability it needs, "" for
// none.
var Effects = map[string]string{
	"toast":                  "",
	"url.open":               "",
	"page.open":              "",
	"setting.status":         "",
	"tab.open":               "ui.place",
	"panels.toggle":          "ui.place",
	"panels.state":           "ui.place",
	"open.in":                "threads:read",
	"app.checkForUpdates":    "",
	"app.settings.get":       "",
	"app.settings.set":       "settings:write",
	"diff.set":               "ui.place",
	"diff.setReviewed":       "threads:read",
	"diff.openFile":          "threads:read",
	"clipboard.write":        "clipboard",
	"notify":                 "notify",
	"composer.draft":         "composer",
	"threads.list":           "threads:read",
	"threads.open":           "threads:read",
	"threads.create":         "threads:write",
	"threads.send":           "threads:write",
	"threads.rename":         "threads:write",
	"threads.delete":         "threads:write",
	"threads.setDisposition": "threads:write",
	"threads.markRead":       "threads:write",
	"threads.interrupt":      "threads:write",
	"threads.configure":      "threads:write",
	"threads.setAccess":      "threads:access",
	"composer.submit":        "threads:write",
	"models.list":            "",
	"projects.remove":        "threads:write",
	"projects.list":          "threads:read",
	"projects.filter":        "threads:read",
	"projects.add":           "threads:write",
}

// Effect is something a plugin asks the app to do, which only the app
// can, as glitched's calls ask it: Type names it, and Args are its fields.
type Effect struct {
	Type string
	Args json.RawMessage
}

// UnmarshalJSON reads an effect as one object: {"type": ..., and its own
// fields}.
func (e *Effect) UnmarshalJSON(b []byte) error {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(b, &head); err != nil {
		return err
	}
	e.Type, e.Args = head.Type, append(json.RawMessage(nil), b...)
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
