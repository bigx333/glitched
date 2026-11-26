package acp

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"glitched/internal/api"
	"glitched/internal/provider"
)

// Profile is what one ACP agent needs that the protocol does not say:
// how to start it, what Glitched calls it, and what it prints that ACP
// has no message for.
type Profile struct {
	// Provider is the agent's key, as a thread's api.ModelRef and the
	// engine's provider.Mux name it.
	Provider string
	// Name is what Glitched calls the agent.
	Name string
	// Binaries are looked for on PATH, then in ~/.local/bin, in order.
	Binaries []string
	// Args start the agent's ACP server on stdio.
	Args []string
	// Env is added to the engine's environment for the agent.
	Env map[string]string
	// Install is what Open says when no binary is found.
	Install string
	// Reserved are the flags Glitched starts the agent with itself, which
	// the user's own launch arguments may not set.
	Reserved []provider.Reserved
	// SignIn is what a turn says when the agent needs its user to sign in.
	SignIn string
	// SignedOut are words the agent prints, outside ACP, when its login
	// went stale.
	SignedOut []string
	// Access is how each of Glitched's access levels reaches the agent:
	// a session mode of its own, or arguments it starts with.
	Access provider.Grants
	// TurnUsage is whether the prompt's answer says what that turn alone
	// used, as seen of the agent on the wire. ACP's usage field may count
	// the whole session, so an agent not seen to report a turn's own
	// reports none.
	TurnUsage bool
	// IdleCompacts is whether idle compaction applies to a session on the
	// model; nil applies it to none.
	IdleCompacts func(model string) bool
	// CleanupProbe, where set, removes the session a probe of the agent's
	// models opened, once the probe's agent is gone, for an agent that
	// keeps every session in its history.
	CleanupProbe func(ctx context.Context, prog provider.Program, session string)
}

// Antigravity is Google's Antigravity CLI, agy, from the user's own
// install, with the user's own login and rules.
var Antigravity = Profile{
	Provider: "antigravity",
	// Its cache rule is Gemini's: a Claude model on agy never compacts.
	IdleCompacts: func(model string) bool { return strings.HasPrefix(strings.ToLower(model), "gemini") },
	Name:         "Antigravity",
	Binaries:     []string{"agy"},
	Args:         []string{"acp", "--disable-telemetry"},
	// A stale login must never open a browser tab in the middle of a turn.
	Env:     map[string]string{"BROWSER": "true"},
	Install: "Antigravity is not installed.\nInstall its CLI with: curl -fsSL https://antigravity.google/cli/install.sh | bash\nor set its path in Settings › Providers.",
	// The access level chooses the mode and the skip flag, and the ACP
	// server is on stdio; --login signs in and exits.
	Reserved: []provider.Reserved{{Flags: []string{"--dangerously-skip-permissions", "--mode", "--login", "--stdio"}}},
	SignIn:   "Antigravity is signed out. Run agy in Terminal to sign in.",
	// agy's own modes, as `session/new` lists them; it refuses yolo, and
	// skips its asking only for a process started so. Approve for me is
	// its auto mode, which approves edits and safe commands and asks
	// before irreversible ones; a session that doesn't list auto runs in
	// accept-edits, which approves only edits.
	Access: provider.Grants{
		api.AccessAsk:       {Mode: "default"},
		api.AccessAutoEdits: {Mode: "auto", Else: "accept-edits"},
		api.AccessFull:      {Args: []string{"--dangerously-skip-permissions"}},
	},
	// agy 1.5.14 answered two turns of a session with 22225 and 11858.
	TurnUsage: true,
	SignedOut: []string{
		"Authentication required",
		"Please sign in",
		"Please log in",
		"Log in with Google",
	},
}

// Gemini is Google's Gemini CLI running its ACP stdio server.
var Gemini = Profile{
	Provider:     "gemini",
	IdleCompacts: func(model string) bool { return strings.HasPrefix(strings.ToLower(model), "gemini") },
	Name:         "Gemini CLI",
	Binaries:     []string{"gemini"},
	Args:         []string{"--experimental-acp"},
	Env:          map[string]string{"BROWSER": "true"},
	Install:      "Gemini CLI is not installed.\nInstall it with: npm install -g @google/gemini-cli\nor set its path in Settings › Providers.",
	Reserved:     []provider.Reserved{{Flags: []string{"--experimental-acp", "--yolo", "--approval-mode"}}},
	SignIn:       "Gemini CLI is signed out. Run gemini in Terminal to sign in.",
	Access: provider.Grants{
		api.AccessAsk:       {Mode: "default"},
		api.AccessAutoEdits: {Mode: "autoEdit", Else: "default"},
		api.AccessFull:      {Args: []string{"--yolo"}},
	},
	SignedOut: []string{
		"Authentication required",
		"Please sign in",
		"Please log in",
		"API_KEY",
	},
}

// OpenCode is OpenCode's ACP agent server.
var OpenCode = Profile{
	Provider: "opencode",
	Name:     "OpenCode",
	Binaries: []string{"opencode"},
	Args:     []string{"acp"},
	Env:      map[string]string{"BROWSER": "true"},
	Install:  "OpenCode is not installed.\nInstall it from https://opencode.ai or brew install anomalyco/tap/opencode\nor set its path in Settings › Providers.",
	SignIn:   "OpenCode needs a model provider configured. Run opencode in Terminal to sign in.",
	Access: provider.Grants{
		api.AccessAsk:       {Mode: "default"},
		api.AccessAutoEdits: {Mode: "auto", Else: "default"},
		api.AccessFull:      {Mode: "yolo", Else: "auto"},
	},
	SignedOut: []string{
		"Authentication required",
		"Please sign in",
		"No API key",
	},
}

// Profiles are the built-in ACP agents Glitched knows.
var Profiles = []Profile{Antigravity, Gemini, OpenCode}

// CustomProfile describes a user-configured ACP CLI in engine.json.
type CustomProfile struct {
	Provider string            `json:"provider"`
	Name     string            `json:"name,omitempty"`
	Binary   string            `json:"binary"`
	Args     []string          `json:"args,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
}

var customProviderIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

var reservedProviderIDs = map[string]bool{
	"claude":      true,
	"codex":       true,
	"antigravity": true,
	"gemini":      true,
	"opencode":    true,
	"fake":        true,
}

// ToProfile validates c and converts it into a runnable ACP Profile.
func (c CustomProfile) ToProfile() (Profile, error) {
	id := strings.TrimSpace(c.Provider)
	if !customProviderIDRe.MatchString(id) {
		return Profile{}, fmt.Errorf("acp: provider id %q must be 1-32 lowercase letters, digits, '-' or '_'", c.Provider)
	}
	if reservedProviderIDs[id] {
		return Profile{}, fmt.Errorf("acp: provider id %q is reserved for a built-in provider", id)
	}
	bin := strings.TrimSpace(c.Binary)
	if bin == "" {
		return Profile{}, errors.New("acp: binary command or path is required")
	}
	name := strings.TrimSpace(c.Name)
	if name == "" {
		name = id
	}
	args := slices.Clone(c.Args)
	if len(args) == 0 {
		args = []string{"acp"}
	}
	env := maps.Clone(c.Env)
	if env == nil {
		env = map[string]string{}
	}
	if _, ok := env["BROWSER"]; !ok {
		env["BROWSER"] = "true"
	}
	return Profile{
		Provider: id,
		Name:     name,
		Binaries: []string{bin},
		Args:     args,
		Env:      env,
		Install:  fmt.Sprintf("%s is not installed.\nCheck its binary path (%s) in Settings › Providers.", name, bin),
		SignIn:   fmt.Sprintf("%s requires authentication. Run %s in Terminal to sign in.", name, bin),
		Access: provider.Grants{
			api.AccessAsk:       {Mode: "default"},
			api.AccessAutoEdits: {Mode: "auto", Else: "default"},
			api.AccessFull:      {Mode: "bypassPermissions", Else: "auto"},
		},
		SignedOut: []string{
			"Authentication required",
			"Please sign in",
			"Please log in",
		},
	}, nil
}
