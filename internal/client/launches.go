package client

import (
	"context"
	"errors"
	"fmt"
	"time"

	"glitched/internal/api"
	"glitched/internal/daemon"
	"glitched/internal/provider"
	"glitched/internal/transport"
)

// launcher is an engine that says how its providers' programs start, and
// takes how they should.
type launcher interface {
	Launches(ctx context.Context) ([]transport.LaunchInfo, error)
	SetLaunch(ctx context.Context, provider string, l provider.Launch) (transport.LaunchInfo, error)
}

var errNoLaunches = errors.New("client: this engine doesn't say how its providers start")

// launchTimeout covers each program's answer to --version.
const launchTimeout = time.Minute

// Launches asks e's engine, when it is still engine, how each of its providers' programs starts on
// its machine. done gets the answer, on the UI thread.
func (m *Manager) Launches(e *Env, engine api.ID, done func([]transport.LaunchInfo, error)) {
	m.launches(e, engine, func(err error) { done(nil, err) }, func(ctx context.Context, l launcher) {
		out, err := l.Launches(ctx)
		m.post(e.ctx, func() { done(out, err) })
	})
}

// SetLaunch has e's engine, when it is still engine, start provider's program as l says from its
// next launch on, when nothing is wrong with l. done gets what the engine
// found, and whether it kept l, on the UI thread.
func (m *Manager) SetLaunch(e *Env, engine api.ID, name string, l provider.Launch, done func(transport.LaunchInfo, error)) {
	m.launches(e, engine, func(err error) { done(transport.LaunchInfo{}, err) }, func(ctx context.Context, c launcher) {
		out, err := c.SetLaunch(ctx, name, l)
		m.post(e.ctx, func() { done(out, err) })
	})
}

// launches calls e's engine: a remote's over its link, which proved its
// token, and this machine's on its unix socket, as an engine embedded
// here is reached in this process, where no launcher is served.
func (m *Manager) launches(e *Env, engine api.ID, fail func(error), call func(context.Context, launcher)) {
	s := e.session
	switch {
	case s == nil:
		fail(errNoEngine)
		return
	case e.ID != engine:
		fail(fmt.Errorf("client: %s reaches another engine now", e.Name))
		return
	}
	c, ok := s.ep.(launcher)
	var sock *transport.EndpointClient
	if e.Kind == Local {
		sock = transport.NewEndpointClient(daemon.Socket(m.cfg.Dir), api.ID{})
		c, ok = sock, true
	}
	if !ok {
		fail(errNoLaunches)
		return
	}
	ectx := e.ctx
	m.wg.Go(func() {
		ctx, cancel := context.WithTimeout(ectx, launchTimeout)
		defer cancel()
		call(ctx, c)
		if sock != nil {
			sock.Close()
		}
	})
}
