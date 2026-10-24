// Package daemon starts an engine: its lock, store and runtime, the hub role
// or an uploader to another engine's hub, and the sockets that serve them.
// cmd/glitched runs it as a service, and the GUI can embed it.
package daemon

import (
	"context"
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
	"glitched/internal/hub"
	"glitched/internal/provider"
	"glitched/internal/runtime"
	"glitched/internal/store"
	"glitched/internal/transport"
)

// ErrLocked is Start's error when another engine owns the directory. It
// gets its own exit code so a service manager doesn't restart a second
// engine in a loop.
var ErrLocked = errors.New("another engine holds the lock")

type Config struct {
	Dir string // the lock, the databases, the socket and tokens.json
	// Hub runs the hub role in this engine. Otherwise HubAt, when set, is
	// the engine whose hub this one uploads its log to.
	Hub   bool
	HubAt transport.Transport
	// Listen adds TCP listeners on loopback or Tailscale addresses, where a
	// client must prove it holds a token from tokens.json. The unix socket in Dir
	// is for this machine's user and needs none.
	Listen  []string
	Epoch   uint64 // this host's epoch at the hub
	Adapter provider.Adapter
}

// A Daemon is a started engine. It serves until Start's ctx ends, Close is
// called, a listener fails, or the hub proves the database came back from a
// backup.
type Daemon struct {
	host *runtime.Host
	stop context.CancelFunc
	done chan struct{}
	err  error
	undo []func() error // run in reverse once serving stops

	mu    sync.Mutex
	addrs []net.Addr
}

// listenOn is Tailnet.Listen; a test swaps it to assign an address late.
var listenOn = transport.Tailnet.Listen

// Socket is the unix socket an engine with state in dir serves.
func Socket(dir string) transport.Loopback {
	return transport.Loopback{Network: "unix", Address: filepath.Join(dir, "engine.sock")}
}

// Start returns once every socket listens, so a client may dial at once.
func Start(ctx context.Context, cfg Config) (_ *Daemon, err error) {
	if cfg.Hub && cfg.HubAt != nil {
		return nil, errors.New("daemon: the hub role runs here, so there is no other hub to upload to")
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
	lk, err := lock(filepath.Join(cfg.Dir, "engine.lock"))
	if err != nil {
		return nil, err
	}
	if lk != nil {
		d.undo = append(d.undo, lk.Close)
	}
	sock := Socket(cfg.Dir)
	// The lock is ours, so a socket file left here is from a dead engine.
	os.Remove(sock.Address)

	st, err := store.Open(filepath.Join(cfg.Dir, "engine.db"))
	if err != nil {
		return nil, err
	}
	d.undo = append(d.undo, st.Close)

	var rep *hub.Replica
	var hubc hub.Client
	switch {
	case cfg.Hub:
		if rep, err = hub.Open(filepath.Join(cfg.Dir, "hub.db")); err != nil {
			return nil, err
		}
		d.undo = append(d.undo, rep.Close)
		hubc = rep
	case cfg.HubAt != nil:
		c := transport.NewHubClient(cfg.HubAt, api.ID{})
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
		case err != nil:
			slog.Warn("hub unreachable, so a restore the fence came back with goes unnoticed", "err", err)
		}
	}

	h, err := runtime.New(ctx, st, cfg.Adapter)
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
	servers := []*transport.Server{{Transport: sock, Endpoint: h, Hub: rep}}
	g := &grants{dir: cfg.Dir, base: ctx}
	tailnetServer := func(tn transport.Tailnet) *transport.Server {
		return &transport.Server{Transport: tn, Endpoint: h, Hub: rep, Authorize: g.authorize}
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
	if len(cfg.Listen) > 0 {
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
func (d *Daemon) Endpoint() api.Endpoint { return d.host }

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
