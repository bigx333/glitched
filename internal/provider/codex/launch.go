package codex

import (
	"context"
	"os/exec"
	"strings"
	"time"

	"glitched/internal/provider"
)

var _ provider.Placed = (*Adapter)(nil)

// placement is how codex takes a provider.Launch. Glitched sends each
// turn's approval policy and sandbox, and each thread's MCP servers, so
// overrides of those are reserved, with the app-server flags that move
// it off stdio, sign out, or exit before serving.
var placement = provider.Placement{
	Names:       []string{"codex"},
	HomeVar:     "CODEX_HOME",
	DefaultHome: "~/.codex",
	Reserved: []provider.Reserved{
		{Flags: []string{"-c", "--config"}, Keys: []string{"approval_policy", "sandbox_mode", "sandbox_workspace_write", "mcp_servers"}},
		{Flags: []string{"--listen", "--print-startup-config", "--startup-sign-out"}},
	},
}

func (a *Adapter) Placement() provider.Placement { return placement }

// Place has the next app-server start as l does, and the next Describe
// probe one so. A parked app-server started otherwise is stopped at its
// thread's next run, which starts another.
func (a *Adapter) Place(l provider.Launch) {
	a.mu.Lock()
	a.Path, a.Home, a.Args = l.Path, l.Home, l.Args
	a.mu.Unlock()
	a.Forget()
}

func (a *Adapter) Version(ctx context.Context, prog provider.Program) (string, error) {
	if err := guard(prog.Path); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, prog.Path, "--version")
	cmd.Env = prog.Environ(cmd.Environ())
	// A helper the program leaves holding stdout can't hold the answer
	// past ctx.
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	return strings.TrimSpace(string(out)), err
}

func (a *Adapter) Command(prog provider.Program) []string {
	return append([]string{prog.Path, "app-server"}, prog.Args...)
}
