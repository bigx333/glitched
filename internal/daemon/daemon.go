// Package daemon starts an engine: its lock, store and runtime, the hub role
// or an uploader to another engine's hub, and the sockets that serve them.
// cmd/glitched runs it as a service, and the GUI can embed it.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
	"time"

	"glitched/internal/api"
	"glitched/internal/browsermcp"
	"glitched/internal/engine"
	"glitched/internal/hub"
	"glitched/internal/jsonfile"
	"glitched/internal/mcp"
	"glitched/internal/provider"
	"glitched/internal/provider/claude"
	"glitched/internal/provider/fake"
	"glitched/internal/runtime"
	"glitched/internal/store"
	"glitched/internal/tailnet"
	"glitched/internal/transport"
)

// ErrLocked is Start's error when another engine owns the directory. It
// gets its own exit code so a service manager doesn't restart a second
// engine in a loop.
var ErrLocked = errors.New("another engine holds the lock")

// Config is how an engine serves. engine.json under Dir keeps all but Dir
// and Providers.
type Config struct {
	Dir string `json:"-"` // the lock, the databases, the socket, tokens.json and engine.json
	// Hub runs the hub role in this engine. Otherwise the engine uploads its
	// log to the hub of the one with state in HubDir on this machine, or of
	// the one at HubServer on the tailnet with the token HubToken it paired.
	Hub       bool   `json:"hub"`
	HubDir    string `json:"hub_dir,omitempty"`
	HubServer string `json:"hub_server,omitempty"`
	HubToken  string `json:"hub_token,omitempty"`
	// Listen adds TCP listeners on loopback or Tailscale addresses, where a
	// client must prove it holds a token from tokens.json. The unix socket in Dir
	// is for this machine's user and needs none.
	Listen []string `json:"listen,omitempty"`
	// Tailnet serves clients with a token on the tailnet at Port: on an
	// installed Tailscale's address once it has one, else on the engine's
	// own tsnet node, signed in to Control, a control server such as
	// Headscale's, or Tailscale's when it's "".
	Tailnet   bool         `json:"tailnet,omitempty"`
	Control   string       `json:"control,omitempty"`
	Epoch     uint64       `json:"epoch"` // this host's epoch at the hub
	Providers provider.Mux `json:"-"`
	// MCP are the engine's own MCP servers, which every run's agent gets
	// after glitched mcp, as the app's browser.
	MCP []provider.MCPServer `json:"-"`
	// ClaudePath is the claude binary an engine with WithClaude runs Claude
	// Code threads with, when it is not on PATH or in ~/.local/bin.
	ClaudePath string `json:"claude_path,omitempty"`
	// Children caps the child threads agents start. Its zero fields take
	// engine.DefaultLimits'.
	Children engine.Limits `json:"children,omitzero"`
	// PluginRun runs the programs plugins' services ask for; nil runs none.
	PluginRun func(ctx context.Context, id string, allowed []string, args json.RawMessage) (any, error) `json:"-"`
}

const configFile = "engine.json"

// Defaults is how an engine with state in dir serves unless told otherwise:
// as Save last recorded there, else as its own hub at epoch 1. `glitched
// serve` and the GUI's embedded engine both start from it, as the hub
// fences an epoch below the last one it saw and other machines reach the
// engine where serve listened.
func Defaults(dir string) (Config, error) {
	ad := fake.New(fake.Turn(true))
	ad.Tokens = 1800 // a turn's usage, for the GUI to show
	cfg := Config{Dir: dir, Hub: true, Epoch: 1, Providers: FakeProviders(ad)}
	if err := jsonfile.Load(filepath.Join(dir, configFile), &cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// FakeProviders has ad play fake and every provider the GUI offers, until
// each has an adapter of its own.
func FakeProviders(ad provider.Adapter) provider.Mux {
	m := provider.Mux{}
	for _, name := range []string{"fake", "claude", "codex", "gemini", "grok"} {
		m[name] = ad
	}
	return m
}

// WithClaude has the engine run Claude Code threads with the real claude,
// as `glitched serve` and the GUI's own engine do, and nothing else: the
// fake that plays the other providers is a test's. An engine a test
// starts keeps the fake, as Defaults gives it.
//
// Each run gets the browser of the app on the engine's state directory
// as an MCP server (package browsermcp), as this program's browser-mcp.
func (cfg *Config) WithClaude() {
	cfg.Providers = provider.Mux{"claude": &claude.Adapter{Path: cfg.ClaudePath}}
	if s, ok := browsermcp.Server(cfg.Dir); ok {
		cfg.MCP = []provider.MCPServer{s}
	}
}

// Save records cfg in engine.json under its Dir, for Defaults.
func (cfg Config) Save() error {
	return jsonfile.Write(filepath.Join(cfg.Dir, configFile), cfg)
}

// A Daemon is a started engine. It serves until Start's ctx ends, Close is
// called, a listener fails, or the hub proves the database came back from a
// backup.
type Daemon struct {
	host  *runtime.Host
	store *store.Store
	node  *tailnet.Node // the engine's own, when it needs the tailnet and Tailscale isn't installed
	// nodeServes: the engine listens on its own node, as it does when
	// Tailnet is on, not only to reach a hub.
	nodeServes bool
	stop       context.CancelFunc
	done       chan struct{}
	err        error
	undo       []func() error // run in reverse once serving stops

	mu        sync.Mutex
	addrs     []net.Addr
	tailnetAt string // where it serves an installed Tailscale's tailnet, once it does
}

// listenOn is Tailnet.Listen; a test swaps it to assign an address late.
var listenOn = transport.Tailnet.Listen

// Socket is the unix socket an engine with state in dir serves.
func Socket(dir string) transport.Loopback {
	return transport.Loopback{Network: "unix", Address: filepath.Join(dir, "engine.sock")}
}

// Start returns once every socket listens, so a client may dial at once.
func Start(ctx context.Context, cfg Config) (_ *Daemon, err error) {
	var hubAt transport.Transport
	switch {
	case cfg.HubDir != "" && cfg.HubServer != "", cfg.Hub && (cfg.HubDir != "" || cfg.HubServer != ""):
		return nil, errors.New("daemon: give one hub: this engine's, HubDir's or HubServer's")
	case cfg.HubServer != "":
		hubAt = transport.Tailnet{Address: cfg.HubServer, Token: cfg.HubToken}
	case cfg.HubDir != "":
		hubAt = Socket(cfg.HubDir)
	}
	ctx, stop := context.WithCancel(ctx)
	d := &Daemon{stop: stop, done: make(chan struct{})}
	defer func() {
		if err != nil {
			stop()
			d.release()
		}
	}()

	if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
		return nil, err
	}
	// Nothing authenticates a caller on the unix socket, so file modes are
	// its only gate; a dir created earlier may be wider than MkdirAll's mode.
	if err := os.Chmod(cfg.Dir, 0o700); err != nil {
		return nil, err
	}
	unlock, err := lock(filepath.Join(cfg.Dir, "engine.lock"))
	if err != nil {
		return nil, err
	}
	if unlock != nil {
		d.undo = append(d.undo, unlock)
	}
	sock := Socket(cfg.Dir)
	// The lock is ours, so a socket file left here is from a dead engine.
	os.Remove(sock.Address)

	st, err := store.Open(filepath.Join(cfg.Dir, "engine.db"))
	if err != nil {
		return nil, err
	}
	d.undo = append(d.undo, st.Close)
	st.SetLimits(cfg.Children)
	d.store = st

	// The engine runs its own node when it needs the tailnet, to serve it
	// or to reach a hub on it, and Tailscale isn't installed. A hub on
	// loopback is on this machine, which no node reaches.
	hubOnTailnet := cfg.HubServer != "" && !loopback(cfg.HubServer)
	var installed bool
	if cfg.Tailnet || hubOnTailnet {
		probe, cancel := context.WithTimeout(ctx, time.Second)
		_, err := detect(probe)
		cancel()
		// A missing Tailscale fails at once; one slow to answer is there.
		installed = err == nil || errors.Is(err, context.DeadlineExceeded)
		if !installed {
			node, err := tailnet.Start(filepath.Join(cfg.Dir, nodeDir), hostname(), cfg.Control)
			if err != nil {
				return nil, err
			}
			d.node = node
			d.undo = append(d.undo, node.Close)
			if hubOnTailnet {
				hubAt = transport.Embedded{Node: node, Address: cfg.HubServer, Token: cfg.HubToken}
			}
		}
	}

	var rep *hub.Replica
	var hubc hub.Client
	switch {
	case cfg.Hub:
		if rep, err = hub.Open(filepath.Join(cfg.Dir, "hub.db")); err != nil {
			return nil, err
		}
		d.undo = append(d.undo, rep.Close)
		hubc = rep
	case hubAt != nil:
		c := transport.NewHubClient(hubAt, api.ID{})
		d.undo = append(d.undo, c.Close)
		hubc = c
	}
	var up *hub.Uploader
	if hubc != nil {
		up = hub.NewUploader(st.HostID(), cfg.Epoch, st, hubc)
		// The hub lives outside a backup of this database, so it sees a
		// restore that brought the fence back too.
		check, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := up.Check(check)
		cancel()
		switch {
		case errors.Is(err, hub.ErrRewound):
			slog.Warn("the hub holds more of this log than the database, which came back from a backup; starting a new log", "log", st.LogID())
			if err := st.Rewind(ctx); err != nil {
				return nil, err
			}
		case errors.Is(err, api.ErrProtocol):
			slog.Warn("the hub speaks another protocol, so nothing is uploaded to it until one of the two is updated", "err", err)
		case err != nil:
			slog.Warn("hub unreachable, so a restore the fence came back with goes unnoticed", "err", err)
		}
	}

	var opts []runtime.Option
	// The agent's glitched mcp runs in the agent's workspace, not ours, so it
	// gets the socket's absolute path.
	engineAt, absErr := filepath.Abs(sock.Address)
	if exe, err := os.Executable(); err == nil && absErr == nil {
		opts = append(opts, runtime.WithMCP(mcp.Launcher{Command: exe, Engine: engineAt}))
	} else {
		slog.Warn("agents run without glitched mcp, so they start no child threads", "err", errors.Join(err, absErr))
	}
	opts = append(opts, runtime.WithMCPServers(cfg.MCP...))
	h, err := runtime.New(ctx, st, cfg.Providers, opts...)
	if err != nil {
		return nil, err
	}
	d.undo = append(d.undo, h.Close)
	d.host = h

	var lns []net.Listener
	defer func() {
		if err != nil {
			for _, ln := range lns {
				ln.Close()
			}
		}
	}()
	ln, err := sock.Listen(ctx)
	if err != nil {
		return nil, err
	}
	lns = append(lns, ln)
	if err := os.Chmod(sock.Address, 0o600); err != nil {
		return nil, err
	}
	svc := d.startPlugins(cfg.Dir, cfg.PluginRun)
	d.undo = append(d.undo, func() error { svc.Close(); return nil })
	servers := []*transport.Server{{Transport: sock, Endpoint: endpoint{h, d, false}, Hub: rep, Plugins: pluginServer{svc}, Providers: newCommandLists(ctx, cfg.Providers)}}
	g := &grants{dir: cfg.Dir, base: ctx}
	tailnetServer := func(tn transport.Transport) *transport.Server {
		return &transport.Server{Transport: tn, Endpoint: endpoint{h, d, true}, Hub: rep, Authorize: g.authorize}
	}
	type unbound struct {
		tn  transport.Tailnet
		err error
	}
	var later []unbound
	for _, addr := range cfg.Listen {
		tn := transport.Tailnet{Address: addr}
		ln, err := listenOn(tn, ctx)
		switch {
		case errors.Is(err, syscall.EADDRNOTAVAIL):
			// At boot Tailscale may not have assigned the address yet.
			later = append(later, unbound{tn, err})
			continue
		case err != nil:
			return nil, err
		}
		lns = append(lns, ln)
		d.addrs = append(d.addrs, ln.Addr())
		servers = append(servers, tailnetServer(tn))
	}

	if cfg.Tailnet && d.node != nil {
		tn := transport.Embedded{Node: d.node, Address: ":" + Port}
		ln, err := tn.Listen(ctx)
		if err != nil {
			return nil, fmt.Errorf("daemon: listen on the embedded node: %w", err)
		}
		lns = append(lns, ln)
		servers = append(servers, tailnetServer(tn))
		d.nodeServes = true
	}

	var serving, background sync.WaitGroup
	var failed sync.Once
	fail := func(err error) {
		failed.Do(func() { d.err = err })
		stop()
	}
	for i, srv := range servers {
		serving.Go(func() {
			if err := srv.Serve(ctx, lns[i]); err != nil {
				fail(err)
			}
		})
	}
	for _, u := range later {
		serving.Go(func() {
			ln := d.listenLater(ctx, u.tn, u.err)
			if ln == nil {
				return
			}
			if err := tailnetServer(u.tn).Serve(ctx, ln); err != nil {
				fail(err)
			}
		})
	}
	if up != nil {
		st.OnCommit(func(store.Commit) { up.Notify() })
		background.Go(func() {
			err := up.Run(ctx)
			switch {
			case ctx.Err() != nil:
			case errors.Is(err, hub.ErrRewound):
				// The runtime holds the log open, so the rewind waits for the
				// startup check of the next start.
				fail(fmt.Errorf("daemon: %w; the database came back from a backup, so restart the engine to start a new log", err))
			default:
				slog.Error("uploader stopped", "err", err)
			}
		})
	}
	if installed && cfg.Tailnet {
		serving.Go(func() {
			if err := d.serveInstalled(ctx, cfg.Listen, tailnetServer); err != nil {
				fail(err)
			}
		})
	}
	if installed {
		background.Go(func() { retire(ctx, filepath.Join(cfg.Dir, nodeDir), cfg.Control) })
	}
	if len(cfg.Listen) > 0 || cfg.Tailnet {
		background.Go(func() { g.watch(ctx) })
	}
	go func() {
		serving.Wait()
		stop()
		background.Wait()
		if err := d.release(); d.err == nil {
			d.err = err
		}
		close(d.done)
	}()
	slog.Info("serving", "socket", sock.Address, "listen", d.Addrs(), "host", st.HostID(), "log", st.LogID(), "hub", cfg.Hub)
	return d, nil
}

// listenLater retries tn's address until it binds or ctx ends, logging each
// new error once.
func (d *Daemon) listenLater(ctx context.Context, tn transport.Tailnet, err error) net.Listener {
	failing := err.Error()
	slog.Warn("waiting to listen", "addr", tn.Address, "err", err)
	for delay := 250 * time.Millisecond; ; delay = min(2*delay, 30*time.Second) {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(delay):
		}
		ln, err := listenOn(tn, ctx)
		if err == nil {
			d.mu.Lock()
			d.addrs = append(d.addrs, ln.Addr())
			d.mu.Unlock()
			slog.Info("listening", "addr", ln.Addr())
			return ln
		}
		if err.Error() != failing {
			failing = err.Error()
			slog.Warn("waiting to listen", "addr", tn.Address, "err", err)
		}
	}
}

func (d *Daemon) release() error {
	var errs []error
	for i := len(d.undo) - 1; i >= 0; i-- {
		errs = append(errs, d.undo[i]())
	}
	return errors.Join(errs...)
}

// Endpoint is the engine in this process, for a caller that embeds it.
func (d *Daemon) Endpoint() api.Endpoint { return endpoint{d.host, d, false} }

// SetLimits applies new child limits at once, as engine.json's Children
// does at the next start.
func (d *Daemon) SetLimits(l engine.Limits) { d.store.SetLimits(l) }

// Node is the engine's own tailnet node, which reaches other machines'
// engines when Tailscale isn't installed; nil when it runs none.
func (d *Daemon) Node() transport.Node {
	if d.node == nil {
		return nil
	}
	return d.node
}

// Addrs are where the listeners from Config.Listen took their addresses,
// once bound.
func (d *Daemon) Addrs() []net.Addr {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.addrs)
}

// Wait returns once the engine has stopped serving and released its state,
// with why it stopped: nil when its ctx ended or Close was called.
func (d *Daemon) Wait() error {
	<-d.done
	return d.err
}

func (d *Daemon) Close() error {
	d.stop()
	return d.Wait()
}
