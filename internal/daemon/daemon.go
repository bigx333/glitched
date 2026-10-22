// Package daemon starts an engine: its lock, store and runtime, the hub role
// or an uploader to another engine's hub, and the sockets that serve them.
// cmd/glitched runs it as a service, and the GUI can embed it.
package daemon

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
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
	Dir string // the lock, the databases and the socket
	// Hub runs the hub role in this engine. Otherwise HubAt, when set, is
	// the engine whose hub this one uploads its log to.
	Hub     bool
	HubAt   transport.Transport
	Epoch   uint64 // this host's epoch at the hub
	Adapter provider.Adapter
}

// A Daemon is a started engine. It serves until Start's ctx ends, Close is
// called, or its socket fails.
type Daemon struct {
	host *runtime.Host
	stop context.CancelFunc
	done chan struct{}
	err  error
	undo []func() error // run in reverse once serving stops
}

// Socket is the unix socket an engine with state in dir serves.
func Socket(dir string) transport.Loopback {
	return transport.Loopback{Network: "unix", Address: filepath.Join(dir, "engine.sock")}
}

// Start returns once the socket listens, so a client may dial at once.
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

	ln, err := sock.Listen(ctx)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(sock.Address, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	srv := &transport.Server{Transport: sock, Endpoint: h, Hub: rep}

	var background sync.WaitGroup
	if up != nil {
		st.OnCommit(func(store.Commit) { up.Notify() })
		background.Go(func() {
			if err := up.Run(ctx); err != nil && ctx.Err() == nil {
				slog.Error("uploader stopped", "err", err)
			}
		})
	}
	go func() {
		d.err = srv.Serve(ctx, ln)
		stop()
		background.Wait()
		if err := d.release(); d.err == nil {
			d.err = err
		}
		close(d.done)
	}()
	slog.Info("serving", "socket", sock.Address, "host", st.HostID(), "log", st.LogID(), "hub", cfg.Hub)
	return d, nil
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
