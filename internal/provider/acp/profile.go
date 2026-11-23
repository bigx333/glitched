package acp

import (
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
}

// Antigravity is Google's Antigravity CLI, agy, from the user's own
// install, with the user's own login and rules.
var Antigravity = Profile{
	Provider: "antigravity",
	Name:     "Antigravity",
	Binaries: []string{"agy"},
	Args:     []string{"acp", "--disable-telemetry"},
	// A stale login must never open a browser tab in the middle of a turn.
	Env:     map[string]string{"BROWSER": "true"},
	Install: "Antigravity is not installed.\nInstall its CLI with: curl -fsSL https://antigravity.google/cli/install.sh | bash\nor set agy_path in engine.json.",
	SignIn:  "Antigravity is signed out. Run agy in Terminal to sign in.",
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

// Profiles are the ACP agents Glitched knows.
var Profiles = []Profile{Antigravity}
