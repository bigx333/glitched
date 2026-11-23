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
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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
	// Path is the agent's binary. Empty looks for Profile.Binaries on
	// PATH, then in ~/.local/bin.
	Path string

	once sync.Once
	pool *park.Pool[*proc]

	// catalog is Describe's last answer: each probe opens a session of the
	// agent's, so a picker that asks again does not start one each time.
	cmu       sync.Mutex
	catalog   provider.Info
	catalogOf error
	until     time.Time
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

// Binary is the agent's program, or the Profile's install instructions
// when there is none.
func (a *Adapter) Binary() (string, error) {
	if a.Path != "" {
		// The agent starts in the run's workspace, where a relative path
		// names another file.
		if p, err := exec.LookPath(a.Path); err == nil {
			return filepath.Abs(p)
		}
		return "", a.notInstalled()
	}
	for _, name := range a.Profile.Binaries {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		for _, name := range a.Profile.Binaries {
			if p := filepath.Join(home, ".local", "bin", name); isExecutable(p) {
				return p, nil
			}
		}
	}
	return "", a.notInstalled()
}

func (a *Adapter) notInstalled() error {
	return provider.Needs{Kind: provider.ErrNotInstalled, Help: a.Profile.Install}
}

// Forget has the next Describe probe the agent again, as after its user
// signed in or out.
func (a *Adapter) Forget() {
	a.cmu.Lock()
	a.until = time.Time{}
	a.cmu.Unlock()
}

func isExecutable(path string) bool {
	_, err := exec.LookPath(path)
	return err == nil
}

// Describe is the models the agent offers the user's account, with the
// efforts each runs at, as a new session of the agent's lists them. It
// sends no prompt. It answers from the last probe while that stands.
func (a *Adapter) Describe(ctx context.Context) (provider.Info, error) {
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
	bin, err := a.Binary()
	if err != nil {
		return provider.Info{}, err
	}
	home, _ := os.UserHomeDir()
	p, err := launch(ctx, a.Profile, bin, home, "probe-"+api.NewID().String())
	if err != nil {
		return provider.Info{}, err
	}
	defer p.discard()
	if _, err := p.open(ctx, home, nil, ""); err != nil {
		return provider.Info{}, err
	}
	models, err := p.models(ctx)
	if err != nil {
		return provider.Info{}, err
	}
	if p.caps.close {
		p.conn.CloseSession(ctx, acpsdk.CloseSessionRequest{SessionId: p.session})
	}
	return provider.Info{Name: a.Profile.Name, Version: p.version, Models: models, Path: bin}, nil
}

// Open runs the run on the thread's agent: the one its last run left,
// when that one was started as this run would start one, or else a new
// one with a new session. The run's model and effort are set on the
// session before Open returns.
func (a *Adapter) Open(ctx context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	bin, err := a.Binary()
	if err != nil {
		return nil, err
	}
	// What an agent and its session are started with is fixed for their
	// life: the MCP servers, with the access glitched mcp acts at, go to
	// session/new and can't change after.
	key := launchKey(bin, spec)
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
	if ok {
		err = p.configure(ctx, spec.Model)
		if p.halted.Load() {
			p.discard()
			ok = false
		} else if err != nil {
			return nil, a.refused(p, keep, err)
		}
	}
	lost := false
	if !ok {
		if p, err = launch(ctx, a.Profile, bin, spec.Workspace, spec.Thread.String()+"-"+api.NewID().String()); err != nil {
			return nil, err
		}
		if lost, err = p.open(ctx, spec.Workspace, spec.MCP, spec.Session); err != nil {
			p.discard()
			return nil, err
		}
		if err := p.configure(ctx, spec.Model); err != nil {
			return nil, a.refused(p, keep, err)
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

func launchKey(bin string, spec provider.SessionSpec) string {
	servers, _ := json.Marshal(spec.MCP)
	sum := sha256.Sum256(servers)
	return strings.Join([]string{bin, spec.Workspace, strconv.Itoa(int(spec.Access)), hex.EncodeToString(sum[:])}, "\x00")
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
