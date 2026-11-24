// Package acp runs agents that speak the Agent Client Protocol, one
// process per thread kept between its runs, on stdio. A Profile says what
// one agent needs beyond the protocol; Antigravity is the first.
package acp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/park"
)

// Adapter runs the agent a Profile describes with the user's own login
// and settings, as the agent in a terminal would. Glitched holds no token.
type Adapter struct {
	Profile Profile
	// Path, Home and Args are how the agent starts, as provider.Launch
	// has them: set them before the first run, or with Place. No Profile
	// takes a Home, so one that is set stops the agent starting.
	Path, Home, Args string

	mu sync.Mutex // guards Path, Home and Args against Place

	once sync.Once
	pool *park.Pool[*proc]

	// catalog is Describe's last answer: each probe opens a session of the
	// agent's, so a picker that asks again does not start one each time.
	cmu       sync.Mutex
	catalog   provider.Info
	catalogOf error
	until     time.Time

	// noAuto is why the agent's session last didn't offer the mode an
	// access level runs in, "" since one did.
	noAuto atomic.Pointer[string]
}

// keepIdle and keepMax are how long a thread's agent waits for its next
// run, and how many wait at once. A parked agy holds about 173 MB, and a
// new one costs about 2.2 s before its first prompt.
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
	_ interface{ Close() error } = (*Adapter)(nil)
)

func (a *Adapter) parked() *park.Pool[*proc] {
	a.once.Do(func() { a.pool = park.New[*proc](keepIdle, keepMax) })
	return a.pool
}

// Close stops every agent waiting for its thread's next run.
func (a *Adapter) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return a.parked().Close(ctx)
}

// program is the agent a launch starts now, or what stops one starting.
func (a *Adapter) program() (provider.Program, error) {
	a.mu.Lock()
	l := provider.Launch{Path: a.Path, Home: a.Home, Args: a.Args}
	a.mu.Unlock()
	prog, err := a.Placement().Program(l)
	if err != nil {
		return provider.Program{}, provider.Unplaced(err, a.Profile.Name, a.Profile.Install)
	}
	return prog, nil
}

// Forget has the next Describe probe the agent again, as after its user
// signed in or out.
func (a *Adapter) Forget() {
	a.cmu.Lock()
	a.until = time.Time{}
	a.cmu.Unlock()
}

// Describe is the models the agent offers the user's account, with the
// efforts each runs at, as a new session of the agent's lists them. It
// sends no prompt. It answers from the last probe while that stands.
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
	p, err := launch(ctx, a.Profile, prog, home, "probe-"+api.NewID().String(), nil, nil)
	if err != nil {
		return provider.Info{}, err
	}
	defer p.discard()
	if _, err := p.open(ctx, home, nil, "", false); err != nil {
		return provider.Info{}, err
	}
	models, err := p.models(ctx)
	if err != nil {
		return provider.Info{}, err
	}
	if p.caps.close {
		p.conn.CloseSession(ctx, acpsdk.CloseSessionRequest{SessionId: p.session})
	}
	return provider.Info{Name: a.Profile.Name, Version: p.version, Models: models, Path: prog.Path}, nil
}

// Open runs the run on the thread's agent: the one its last run left,
// when that one was started as this run would start one, or else a new
// one with a new session. The run's model and effort are set on the
// session before Open returns.
func (a *Adapter) Open(ctx context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	if spec.ResumeOnly && spec.Session == "" {
		return nil, fmt.Errorf("%s has no session to resume", a.Profile.Name)
	}
	prog, err := a.program()
	if err != nil {
		return nil, err
	}
	// What an agent and its session are started with is fixed for their
	// life: the MCP servers, with the access glitched mcp acts at, go to
	// session/new and can't change after, and its first prompt lists the
	// skills.
	key := launchKey(prog, spec)
	keep := func(p *proc) { a.parked().Park(spec.Thread, key, p) }
	p, ok, err := a.parked().Take(ctx, spec.Thread, key)
	if err != nil {
		return nil, fmt.Errorf("%s: the thread's earlier agent may still run: %w", a.Profile.Name, err)
	}
	// A woken agent stays parked until the run takes it, so one that
	// speaks while its model is set is stopped, and a new one starts. One
	// on another session than the thread's is the thread's no longer.
	if ok && spec.Session != "" && p.sessionID() != spec.Session {
		p.discard()
		ok = false
	}
	grant := a.Profile.Access[spec.Access]
	configure := func() error {
		refused, err := p.configure(ctx, spec.Model, grant)
		if err == nil && grant.Else != "" {
			a.noAuto.Store(&refused)
		}
		return err
	}
	if ok {
		err = configure()
		if p.halted.Load() {
			p.discard()
			ok = false
		} else if err != nil {
			return nil, a.refused(p, keep, err)
		}
	}
	lost := false
	if !ok {
		if p, err = launch(ctx, a.Profile, prog, spec.Workspace, spec.Thread.String()+"-"+api.NewID().String(), a.Profile.Access[spec.Access].Args, spec.Tap); err != nil {
			return nil, err
		}
		if lost, err = p.open(ctx, spec.Workspace, spec.MCP, spec.Session, spec.ResumeOnly); err != nil {
			p.discard()
			return nil, err
		}
		if err := configure(); err != nil {
			return nil, a.refused(p, keep, err)
		}
	}
	// A compaction goes only to a session that takes /compact, on a model
	// it applies to: agy lists its commands after the session opens, so
	// they are waited for here, before the host's last look at the thread.
	if spec.ResumeOnly {
		p.mu.Lock()
		m, _ := option(p.options, acpsdk.SessionConfigOptionCategoryModel)
		p.mu.Unlock()
		if offered, err := p.offers(ctx, "compact"); err != nil || !offered || a.Profile.IdleCompacts == nil || !a.Profile.IdleCompacts(string(m.CurrentValue)) {
			return nil, a.refused(p, keep, provider.ErrDeclined)
		}
	}
	g := newAgent(spec, p, keep)
	if lost {
		g.notice(a.Profile.Name + " no longer has this thread's earlier conversation, so this turn starts a new one.")
	}
	return g, nil
}

// refused is Open's error when the session would not take the run's model
// or effort. An agent that refused and still runs keeps the thread's
// conversation warm for its next run; any other is stopped.
func (a *Adapter) refused(p *proc, keep func(*proc), err error) error {
	select {
	case <-p.reaped:
	case <-p.conn.Done():
	default:
		if !p.halted.Load() && !errors.Is(err, provider.ErrSignedOut) {
			p.park()
			keep(p)
			return err
		}
	}
	p.discard()
	return err
}

func launchKey(prog provider.Program, spec provider.SessionSpec) string {
	servers, _ := json.Marshal(struct {
		Program provider.Program
		MCP     []provider.MCPServer
		Skills  string
	}{prog, spec.MCP, spec.Skills.Root})
	sum := sha256.Sum256(servers)
	return strings.Join([]string{prog.Path, spec.Workspace, strconv.Itoa(int(spec.Access)), hex.EncodeToString(sum[:])}, "\x00")
}

// SkillDelivery is how the agent gets Glitched's skills: listed in the
// first prompt of each agent Glitched starts, whose SKILL.md files it
// reads itself.
func (a *Adapter) SkillDelivery() string {
	return a.Profile.Name + ": listed in the first message each time Glitched starts it"
}

// Reconcile stops what is left of the run's thread: nothing can reattach
// to an agent whose pipes died with the engine that started it. The run
// is Lost. The host reconciles a run only while no other run of its
// thread is live here, so every agent of the thread goes.
func (a *Adapter) Reconcile(ctx context.Context, id provider.RuntimeIdentity) (provider.Recovery, error) {
	if err := a.parked().Drop(ctx, id.Thread); err != nil {
		return nil, err
	}
	found, err := marked.Stop(ctx, id.Thread.String()+"-", nil)
	switch {
	case err != nil:
		return nil, err
	case found:
		return provider.Lost{Reason: a.Profile.Name + " was stopped: the engine that ran it is gone."}, nil
	}
	return provider.Lost{Reason: a.Profile.Name + " no longer runs."}, nil
}
