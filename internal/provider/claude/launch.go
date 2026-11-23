package claude

import (
	"context"
	"strings"

	"glitched/internal/api"
	"glitched/internal/provider"
)

var _ provider.Placed = (*Adapter)(nil)

// placement is how claude takes a provider.Launch. The flags it reserves
// are those argv, Ask, initialize and Open set: the stream protocol, the
// permission mode and prompt tool the access level chooses, the MCP
// config, the session, and what each thread picks of its own.
var placement = provider.Placement{
	Names:       []string{"claude" + exeSuffix},
	HomeVar:     "CLAUDE_CONFIG_DIR",
	DefaultHome: "~/.claude",
	Reserved: []provider.Reserved{
		{Flags: []string{"-p", "--print", "--input-format", "--output-format", "--include-partial-messages", "--verbose"}},
		{Flags: []string{"--permission-mode", "--permission-prompt-tool", "--dangerously-skip-permissions", "--allow-dangerously-skip-permissions", "--allowedTools", "--allowed-tools"}},
		{Flags: []string{"--mcp-config", "--strict-mcp-config"}},
		{Flags: []string{"--session-id", "-r", "--resume", "-c", "--continue", "--fork-session", "--no-session-persistence"}},
		{Flags: []string{"--model", "--effort", "--system-prompt", "--append-system-prompt"}},
	},
}

func (a *Adapter) Placement() provider.Placement { return placement }

// Place has the next claude start as l does. A parked claude started
// otherwise is stopped at its thread's next run, which starts another.
func (a *Adapter) Place(l provider.Launch) {
	a.mu.Lock()
	a.Path, a.Home, a.Args = l.Path, l.Home, l.Args
	a.mu.Unlock()
}

// Version asks prog, in its folder, without the user's arguments, which
// are for a session.
func (a *Adapter) Version(ctx context.Context, prog provider.Program) (string, error) {
	cmd := Command(ctx, prog.Path, "--version")
	cmd.Env = prog.Environ(cmd.Env)
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	return strings.TrimSpace(string(out)), err
}

// Command is how a new thread at Ask access starts its claude, before
// the model, effort and MCP servers it adds.
func (a *Adapter) Command(prog provider.Program) []string {
	args := a.argv(provider.SessionSpec{Access: api.AccessAsk})
	return append(append([]string{prog.Path}, prog.Args...), append(args, "--session-id", "<thread>")...)
}
