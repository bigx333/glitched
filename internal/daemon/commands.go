package daemon

import (
	"context"
	"sync"
	"time"

	"glitched/internal/provider"
)

// commandsFresh is how long one listing of a provider's slash commands
// answers for its workspace. Claude Code's starts a claude, which runs the
// user's SessionStart hooks, for several seconds; a skill installed since
// shows once it's stale, or after a restart.
const commandsFresh = 5 * time.Minute

// commandLists serves the providers' slash commands, listing each provider
// and workspace's once while it's fresh. Asks that come while it lists wait
// for that listing.
type commandLists struct {
	ctx   context.Context // the engine's: a listing ends with it
	mux   provider.Mux
	now   func() time.Time
	mu    sync.Mutex
	lists map[listKey]*listing
}

type listKey struct{ provider, workspace string }

type listing struct {
	done chan struct{} // closed once the rest is set
	at   time.Time
	cmds []provider.Command
	err  error
}

func newCommandLists(ctx context.Context, mux provider.Mux) *commandLists {
	return &commandLists{ctx: ctx, mux: mux, now: time.Now, lists: map[listKey]*listing{}}
}

func (c *commandLists) Commands(ctx context.Context, name, workspace string) ([]provider.Command, error) {
	k := listKey{name, workspace}
	c.mu.Lock()
	l := c.lists[k]
	if l == nil || c.stale(l) {
		l = &listing{done: make(chan struct{})}
		c.lists[k] = l
		// Not the asker's ctx: one who gives up leaves the listing to the
		// others waiting and to the next ask.
		go c.list(k, l)
	}
	c.mu.Unlock()
	select {
	case <-l.done:
		return l.cmds, l.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// stale is whether l answers no more: done, and failed or too old. A
// failure is asked again at once, as when claude wasn't installed yet.
func (c *commandLists) stale(l *listing) bool {
	select {
	case <-l.done:
		return l.err != nil || c.now().Sub(l.at) >= commandsFresh
	default:
		return false
	}
}

func (c *commandLists) Ask(ctx context.Context, name string, spec provider.AskSpec) (provider.AskResult, error) {
	return c.mux.Ask(ctx, name, spec)
}

// forget has the next ask for name's commands list them again, as its
// program now starts: a listing that is under way answers only those
// already waiting on it.
func (c *commandLists) forget(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.lists {
		if k.provider == name {
			delete(c.lists, k)
		}
	}
}

func (c *commandLists) list(k listKey, l *listing) {
	l.cmds, l.err = c.mux.Commands(c.ctx, k.provider, k.workspace)
	l.at = c.now()
	close(l.done)
}
