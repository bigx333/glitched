package daemon

import (
	"context"
	"net"
	"os"
	"slices"
	"strings"
	"time"

	"log/slog"

	"glitched/internal/api"
	"glitched/internal/plugins/guard"
	"glitched/internal/projects"
	"glitched/internal/runtime"
	"glitched/internal/tailnet"
	"glitched/internal/transport"
)

// Port is where an engine serves the tailnet; any port works with
// -listen.
const Port = "7465"

// nodeDir, under the engine's, keeps its own tailnet node's identity.
const nodeDir = "tailscale"

// detect is tailnet.Detect; a test swaps it to pretend Tailscale isn't
// installed.
var detect = tailnet.Detect

// endpoint is the engine, whose Info also says how its machine is on the
// tailnet. A remote one leaves out the login URL: whoever opens it can
// sign this machine in to their own account.
type endpoint struct {
	*runtime.Host
	d      *Daemon
	remote bool
}

func (e endpoint) Info(ctx context.Context) (api.Environment, error) {
	env, err := e.Host.Info(ctx)
	if err == nil {
		env.Tailnet = e.d.tailnet(ctx)
		if e.remote {
			env.Tailnet.AuthURL = ""
		}
	}
	return env, err
}

var _ transport.Projects = endpoint{}

// Folders lists a folder on this machine, never the engine's secrets.
func (e endpoint) Folders(ctx context.Context, q api.FolderQuery) (api.FolderList, error) {
	return projects.List(ctx, q.Path, []string{e.d.vault})
}

// Clone clones a repository onto this machine, never into the engine's
// secrets, with the programs the user's terminal finds.
func (e endpoint) Clone(ctx context.Context, req api.CloneRequest, progress func(string)) (api.Cloned, error) {
	return projects.CloneInto(ctx, req, []string{e.d.vault}, guard.UserPath(), progress)
}

// tailnet is how this machine is on the tailnet: through the engine's own
// node, else an installed Tailscale, else not at all.
func (d *Daemon) tailnet(ctx context.Context) api.Tailnet {
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	if d.node != nil {
		t, err := d.node.Status(ctx)
		if err == nil && t.Running() && d.nodeServes {
			host := t.Name
			if host == "" {
				host = t.IP.String()
			}
			t.Serving = net.JoinHostPort(host, Port)
		}
		return t
	}
	t, _ := detect(ctx)
	d.mu.Lock()
	t.Serving = d.tailnetAt
	d.mu.Unlock()
	return t
}

// servingTailnet records that the engine serves the tailnet at addr, which
// clients reach by the MagicDNS name when it has one.
func (d *Daemon) servingTailnet(addr, name string) {
	at := addr
	if name != "" {
		at = net.JoinHostPort(name, Port)
	}
	d.mu.Lock()
	d.tailnetAt = at
	d.mu.Unlock()
	slog.Info("serving the tailnet", "addr", addr, "name", name)
}

// serveInstalled serves on the installed Tailscale's address at Port once
// it has one, until ctx ends, unless an address in listen serves it.
func (d *Daemon) serveInstalled(ctx context.Context, listen []string, server func(transport.Transport) *transport.Server) error {
	failing := ""
	for delay := 250 * time.Millisecond; ; delay = min(2*delay, 30*time.Second) {
		probe, cancel := context.WithTimeout(ctx, time.Second)
		t, err := detect(probe)
		cancel()
		why := "Tailscale isn't running"
		if err == nil && t.Running() && t.IP.IsValid() {
			tn := transport.Tailnet{Address: net.JoinHostPort(t.IP.String(), Port)}
			if slices.Contains(listen, tn.Address) {
				// Listen serves it already.
				d.servingTailnet(tn.Address, t.Name)
				return nil
			}
			ln, err := listenOn(tn, ctx)
			if err == nil {
				d.mu.Lock()
				d.addrs = append(d.addrs, ln.Addr())
				d.mu.Unlock()
				d.servingTailnet(tn.Address, t.Name)
				return server(tn).Serve(ctx, ln)
			}
			why = err.Error()
		}
		if why != failing {
			failing = why
			slog.Warn("waiting to serve the tailnet", "why", why)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(delay):
		}
	}
}

// retire signs out the node an engine ran before Tailscale was installed,
// at control, so the tailnet doesn't list the machine twice, and forgets
// it. One that fails is kept for the next start to retry.
func retire(ctx context.Context, dir, control string) {
	if _, err := os.Stat(dir); err != nil {
		return
	}
	node, err := tailnet.Start(dir, hostname(), control)
	if err == nil {
		out, cancel := context.WithTimeout(ctx, 10*time.Second)
		err = node.Logout(out)
		cancel()
		node.Close()
	}
	if err != nil {
		slog.Warn("couldn't sign out the engine's own tailnet node; the next start tries again", "err", err)
		return
	}
	os.RemoveAll(dir)
}

// hostname is the name the engine's own node asks the tailnet for.
func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "glitched"
	}
	h, _, _ = strings.Cut(h, ".")
	return strings.ToLower(h)
}

// loopback reports whether addr, a host:port, is a loopback IP.
func loopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	return err == nil && net.ParseIP(host).IsLoopback()
}
