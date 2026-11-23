package acp

import (
	"context"
	"os/exec"
	"slices"
	"strings"
	"time"

	"glitched/internal/provider"
)

var _ provider.Placed = (*Adapter)(nil)

// Placement is how the agent takes a provider.Launch: no Profile names a
// variable that moves an agent's folder, as agy documents none, so it
// takes no Home.
func (a *Adapter) Placement() provider.Placement {
	return provider.Placement{Names: a.Profile.Binaries, Reserved: a.Profile.Reserved}
}

// Place has the next agent start as l does, and the next Describe probe
// one so. A parked agent started otherwise is stopped at its thread's
// next run, which starts another.
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

// Command is how a new thread at Ask access starts the agent.
func (a *Adapter) Command(prog provider.Program) []string {
	return slices.Concat([]string{prog.Path}, a.Profile.Args, prog.Args)
}
