package daemon

import (
	"context"
	"net"
	"sync"
	"time"

	"glitched/internal/api"
	"glitched/internal/tailnet"
	"glitched/internal/transport"
)

// Status is an engine's state directory as seen from outside the engine:
// what engine.json asks of it, whether an engine answers its socket, how
// the machine is on the tailnet, whether each listener accepts, and the
// tokens clients hold.
type Status struct {
	Config    Config
	Engine    *api.Environment // nil when nothing answers the socket
	EngineErr error            // why nothing answers
	Listeners []Listener       // Config.Listen, in its order
	// Tailnet is as the engine says, or as an installed Tailscale does
	// when no engine answers.
	Tailnet api.Tailnet
	Tokens  []Token
}

// A Listener is one of Config.Listen, and whether a TCP connection to it
// was accepted. One that isn't may wait for Tailscale to assign the
// address, or the engine may not run.
type Listener struct {
	Addr      string
	Accepting bool
}

const probeTimeout = 750 * time.Millisecond

// Inspect reads dir's engine.json and tokens.json and probes the socket and
// each listener. It fails only when a file can't be read.
func Inspect(ctx context.Context, dir string) (Status, error) {
	cfg, err := Defaults(dir)
	if err != nil {
		return Status{}, err
	}
	tokens, err := Tokens(dir)
	if err != nil {
		return Status{}, err
	}
	st := Status{Config: cfg, Tokens: tokens, Listeners: make([]Listener, len(cfg.Listen))}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	var wg sync.WaitGroup
	for i, addr := range cfg.Listen {
		wg.Go(func() {
			c, err := new(net.Dialer).DialContext(ctx, "tcp", addr)
			if err == nil {
				c.Close()
			}
			st.Listeners[i] = Listener{Addr: addr, Accepting: err == nil}
		})
	}
	c := transport.NewEndpointClient(Socket(dir), api.ID{})
	info, err := c.Info(ctx)
	c.Close()
	if err == nil {
		st.Engine, st.Tailnet = &info, info.Tailnet
	} else {
		st.EngineErr = err
		st.Tailnet, _ = tailnet.Detect(ctx)
	}
	wg.Wait()
	return st, nil
}
