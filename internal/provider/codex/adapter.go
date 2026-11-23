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
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/park"
)

// Provider is Codex's key, as a thread's api.ModelRef and the engine's
// provider.Mux name it.
const Provider = "codex"

// install is what Open says when no codex is found.
const install = "Codex is not installed.\nInstall it with: npm install -g @openai/codex\nor set codex_path in engine.json."

// Adapter runs Codex threads.
type Adapter struct {
	// Path is the codex binary. Empty looks for codex on PATH, then in
	// ~/.local/bin.
	Path string

	once sync.Once
	pool *park.Pool[*proc]

	// catalog is Describe's last answer: each probe starts an app-server,
	// so a picker that asks again does not start one each time.
	cmu       sync.Mutex
	catalog   provider.Info
	catalogOf error
	until     time.Time
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
	_ interface{ Close() error } = (*Adapter)(nil)
)

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

// Binary is the codex program, or how to install it when there is none.
func (a *Adapter) Binary() (string, error) {
	if a.Path != "" {
		// app-server starts in the run's workspace, where a relative path
		// names another file.
		if p, err := exec.LookPath(a.Path); err == nil {
			return filepath.Abs(p)
		}
		return "", errors.New(install)
	}
	if p, err := exec.LookPath("codex"); err == nil {
		return p, nil
	}
	if home, err := os.UserHomeDir(); err == nil {
		if p, err := exec.LookPath(filepath.Join(home, ".local", "bin", "codex")); err == nil {
			return p, nil
		}
	}
	return "", errors.New(install)
}

// Describe is the models Codex offers the user's account, with the
// efforts each runs at. It sends no prompt. It answers from the last
// probe while that stands.
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
	p, err := launch(ctx, bin, home, "probe-"+api.NewID().String())
	if err != nil {
		return provider.Info{}, err
	}
	defer p.discard()
	info := provider.Info{Name: "Codex", Version: p.version}
	if err := p.signedIn(ctx); err != nil {
		return info, err
	}
	if info.Models, err = p.models(ctx); err != nil {
		return provider.Info{Name: "Codex", Version: p.version}, err
	}
	return info, nil
}

// Open runs the run on the thread's app-server: the one its last run
// left, when that one was started as this run would start one, or else a
// new one with a new Codex thread.
func (a *Adapter) Open(ctx context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	bin, err := a.Binary()
	if err != nil {
		return nil, err
	}
	// The MCP servers, with the access glitched mcp acts at, go to
	// thread/start and stay with the thread for its life here.
	key := launchKey(bin, spec)
	keep := func(p *proc) { a.parked().Park(spec.Thread, key, p) }
	p, ok, err := a.parked().Take(ctx, spec.Thread, key)
	if err != nil {
		return nil, fmt.Errorf("Codex: the thread's earlier app-server may still run: %w", err)
	}
	if !ok {
		if p, err = launch(ctx, bin, spec.Workspace, spec.Thread.String()+"-"+api.NewID().String()); err != nil {
			return nil, err
		}
		err = p.signedIn(ctx)
		if err == nil {
			err = p.startThread(ctx, spec)
		}
		if err != nil {
			p.discard()
			return nil, err
		}
	}
	return newAgent(spec, p, keep), nil
}

func launchKey(bin string, spec provider.SessionSpec) string {
	servers, _ := json.Marshal(spec.MCP)
	sum := sha256.Sum256(servers)
	return strings.Join([]string{bin, spec.Workspace, strconv.Itoa(int(spec.Access)), hex.EncodeToString(sum[:])}, "\x00")
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
