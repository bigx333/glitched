// Package codex runs OpenAI's Codex through its app-server, JSON-RPC on
// stdio, one app-server per thread kept between its runs, with the user's
// own install, login and settings, as codex in a terminal would. Glitched
// holds no token.
package codex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/park"
)

// Provider is Codex's key, as a thread's api.ModelRef and the engine's
// provider.Mux name it.
const Provider = "codex"

// errNotInstalled is what Describe and Open say when no codex is found.
var errNotInstalled error = provider.Needs{Kind: provider.ErrNotInstalled, Help: "Codex is not installed.\nInstall it with: npm install -g @openai/codex\nor set its path in Settings › Providers."}

// Adapter runs Codex threads.
type Adapter struct {
	// Path, Home and Args are how codex starts, as provider.Launch has
	// them: set them before the first run, or with Place.
	Path, Home, Args string

	mu sync.Mutex // guards Path, Home and Args against Place

	once sync.Once
	pool *park.Pool[*proc]

	// catalog is Describe's last answer: each probe starts an app-server,
	// so a picker that asks again does not start one each time.
	cmu       sync.Mutex
	catalog   provider.Info
	catalogOf error
	until     time.Time

	// noAuto is why Codex last kept a thread's approvals with the user at
	// Approve for me, "" since one took its auto-review.
	noAuto atomic.Pointer[string]
}

// keepIdle and keepMax are how long a thread's app-server waits for its
// next run, and how many wait at once.
var (
	keepIdle = 10 * time.Minute
	keepMax  = 4
)

// catalogFor and catalogRetry are how long Describe's answer stands, and
// how long a failed probe does.
var (
	catalogFor   = 10 * time.Minute
	catalogRetry = time.Minute
)

var (
	_ provider.Adapter           = (*Adapter)(nil)
	_ provider.Forgetter         = (*Adapter)(nil)
	_ interface{ Close() error } = (*Adapter)(nil)
)

// Forget has the next Describe ask Codex again, as after the user signed
// in.
func (a *Adapter) Forget() {
	a.cmu.Lock()
	a.until = time.Time{}
	a.cmu.Unlock()
}

func (a *Adapter) parked() *park.Pool[*proc] {
	a.once.Do(func() { a.pool = park.New[*proc](keepIdle, keepMax) })
	return a.pool
}

// Close stops every app-server waiting for its thread's next run.
func (a *Adapter) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return a.parked().Close(ctx)
}

// program is the codex a launch starts now, or what stops one starting.
func (a *Adapter) program() (provider.Program, error) {
	a.mu.Lock()
	l := provider.Launch{Path: a.Path, Home: a.Home, Args: a.Args}
	a.mu.Unlock()
	prog, err := placement.Program(l)
	if err != nil {
		return provider.Program{}, provider.Unplaced(err, "Codex", errNotInstalled.Error())
	}
	return prog, nil
}

// Describe is the models Codex offers the user's account, with the
// efforts each runs at. It sends no prompt. It answers from the last
// probe while that stands.
func (a *Adapter) Describe(ctx context.Context) (provider.Info, error) {
	info, err := a.described(ctx)
	if s := a.noAuto.Load(); s != nil && err == nil {
		info.NoAuto = *s
	}
	return info, err
}

func (a *Adapter) described(ctx context.Context) (provider.Info, error) {
	a.cmu.Lock()
	defer a.cmu.Unlock()
	if time.Now().Before(a.until) {
		return a.catalog, a.catalogOf
	}
	info, err := a.probe(ctx)
	if ctx.Err() != nil {
		return info, err // the caller gave up; the next one probes again
	}
	a.catalog, a.catalogOf = info, err
	a.until = time.Now().Add(map[bool]time.Duration{true: catalogFor, false: catalogRetry}[err == nil])
	return info, err
}

func (a *Adapter) probe(ctx context.Context) (provider.Info, error) {
	prog, err := a.program()
	if err != nil {
		return provider.Info{}, err
	}
	home, _ := os.UserHomeDir()
	p, err := launch(ctx, prog, home, "probe-"+api.NewID().String())
	if err != nil {
		return provider.Info{}, err
	}
	defer p.discard()
	info := provider.Info{Name: "Codex", Version: p.version, Path: prog.Path}
	if err := p.signedIn(ctx); err != nil {
		return info, err
	}
	if info.Models, err = p.models(ctx); err != nil {
		return provider.Info{Name: "Codex", Version: p.version, Path: prog.Path}, err
	}
	return info, nil
}

// Open runs the run on the thread's app-server: the one its last run
// left, when that one was started as this run would start one, or else a
// new one with a new Codex thread.
func (a *Adapter) Open(ctx context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	prog, err := a.program()
	if err != nil {
		return nil, err
	}
	// The MCP servers, with the access glitched mcp acts at, go to
	// thread/start and stay with the thread for its life here.
	key := launchKey(prog, spec)
	keep := func(p *proc) { a.parked().Park(spec.Thread, key, p) }
	p, ok, err := a.parked().Take(ctx, spec.Thread, key)
	if err != nil {
		return nil, fmt.Errorf("Codex: the thread's earlier app-server may still run: %w", err)
	}
	// One on another Codex thread than the thread's is the thread's no
	// longer.
	if ok && spec.Session != "" && p.threadID() != spec.Session {
		if err := p.Close(ctx); err != nil {
			return nil, fmt.Errorf("Codex: the thread's earlier app-server may still run: %w", err)
		}
		ok = false
	}
	lost := false
	if !ok {
		if p, err = launch(ctx, prog, spec.Workspace, spec.Thread.String()+"-"+api.NewID().String()); err != nil {
			return nil, err
		}
		err = p.signedIn(ctx)
		if err == nil {
			lost, err = p.startThread(ctx, spec)
		}
		if err != nil {
			p.discard()
			return nil, err
		}
	}
	// What the run's app-server said of auto-review is Codex's latest word
	// on it, at a level that asks for it.
	if levels[spec.Access].reviewer == autoReview {
		refused := ""
		if p.reviewer != autoReview {
			refused = noAuto(p.version)
		}
		a.noAuto.Store(&refused)
	}
	g := newAgent(spec, p, keep)
	// What a kept app-server said before this run is confirmed for it only
	// where it is what the run asks.
	if ok {
		_, g.since = p.settled()
	}
	if lost {
		g.mu.Lock()
		g.noticeLocked("Codex no longer has this thread's earlier conversation, so this turn starts a new one.")
		g.mu.Unlock()
	}
	return g, nil
}

// noAuto is why a thread of Codex version's at Approve for me asks the
// user what auto-review would decide.
func noAuto(version string) string {
	return "Codex " + version + " kept the thread's approvals with you, not its auto-review"
}

func launchKey(prog provider.Program, spec provider.SessionSpec) string {
	servers, _ := json.Marshal(struct {
		Program provider.Program
		MCP     []provider.MCPServer
	}{prog, spec.MCP})
	sum := sha256.Sum256(servers)
	return strings.Join([]string{prog.Path, spec.Workspace, strconv.Itoa(int(spec.Access)), hex.EncodeToString(sum[:])}, "\x00")
}

// Reconcile stops what is left of the run's thread: nothing can reattach
// to an app-server whose stdin died with the engine that started it. The
// run is Lost. The host reconciles a run only while no other run of its
// thread is live here, so every app-server of the thread goes.
func (a *Adapter) Reconcile(ctx context.Context, id provider.RuntimeIdentity) (provider.Recovery, error) {
	if err := a.parked().Drop(ctx, id.Thread); err != nil {
		return nil, err
	}
	found, err := marked.Stop(ctx, id.Thread.String()+"-", nil)
	switch {
	case err != nil:
		return nil, err
	case found:
		return provider.Lost{Reason: "Codex was stopped: the engine that ran it is gone."}, nil
	}
	return provider.Lost{Reason: "Codex no longer runs."}, nil
}
