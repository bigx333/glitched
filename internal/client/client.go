// Package client is the GUI's view of its backends: one environment per
// engine it reaches, each with its threads' views, the transcripts the GUI
// opened and the commands still in flight. It imports no UI.
//
// Every change to that state runs in a function handed to Config.Post, so
// the GUI reads and changes it on its own thread without locks. The
// goroutines that talk to engines never touch it; they post what they read.
package client

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"glitched/internal/api"
	"glitched/internal/daemon"
	"glitched/internal/transport"
)

type Config struct {
	// Dir is the local engine's state directory, and holds gui.json.
	Dir string
	// Post runs fn on the thread that owns the manager's state, in the order
	// posted, and must not block; the GUI passes its window's Update.
	Post func(fn func())
}

const (
	probeTimeout   = 750 * time.Millisecond
	connectTimeout = 10 * time.Second
	submitTimeout  = 10 * time.Second
	minBackoff     = time.Second
	maxBackoff     = 30 * time.Second
)

// Manager owns the environments. Call its methods and read what they
// return only on the Post thread.
type Manager struct {
	cfg    Config
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	local  *local

	envs   []*Env
	saved  saved
	closed bool
}

func New(cfg Config) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{cfg: cfg, ctx: ctx, cancel: cancel}
}

// Start reaches the local engine and the servers gui.json in Dir lists.
// The local engine is the one serving the socket under Dir, or one
// embedded here when none answers, which then serves that socket for the
// CLI until Close.
func (m *Manager) Start() error {
	s, err := load(m.cfg.Dir)
	if err != nil {
		return err
	}
	m.saved = s
	m.local = &local{dir: m.cfg.Dir}
	m.envs = append(m.envs, m.launch(&Env{Kind: Local, Name: "local"}, m.local))
	for _, srv := range s.Servers {
		m.envs = append(m.envs, m.remote(srv))
	}
	return nil
}

// Close stops every environment and an embedded engine, and returns once
// their goroutines have.
func (m *Manager) Close() error {
	if m.closed {
		return nil
	}
	m.closed = true
	m.cancel()
	m.wg.Wait()
	if m.local != nil {
		return m.local.err
	}
	return nil
}

// Envs is the local environment, then the remotes. The slice is the
// manager's: read it, don't change it.
func (m *Manager) Envs() []*Env { return m.envs }

type Kind uint8

const (
	Local Kind = iota
	Remote
)

// Status is sealed: Connecting, Online, Offline or Refused.
type Status interface{ status() }

type Connecting struct{}

type Online struct{}

// Offline: the last attempt failed with Err, and the next starts at RetryAt.
type Offline struct {
	Err     error
	RetryAt time.Time
}

// Refused: the server refused the token. Nothing retries until the server
// entry changes.
type Refused struct{}

func (Connecting) status() {}
func (Online) status()     {}
func (Offline) status()    {}
func (Refused) status()    {}

type Env struct {
	ID      api.ID // the host; zero until it first answers
	Name    string // the host's name for the local engine, the server's label for a remote
	Kind    Kind
	Status  Status
	Threads map[api.ID]*Thread

	ctx     context.Context
	stop    context.CancelFunc
	ep      api.Endpoint // where commands go; nil until the local engine answers
	session *session     // the live shell stream; nil while there is none
	gone    bool
}

// session is one shell stream. Thread subscriptions run under its ctx, so
// they end with it.
type session struct {
	ctx context.Context
	ep  api.Endpoint
}

type Thread struct {
	View       api.Thread
	Transcript *Transcript // nil until opened
	Pending    []Pending

	known bool // the engine sent a view; false for a thread created here and not yet confirmed
	open  bool
	sub   *sub
	last  chan struct{} // closed once the thread's latest submit returns
}

type sub struct{ cancel context.CancelFunc }

func (m *Manager) launch(e *Env, r route) *Env {
	e.Status, e.Threads = Connecting{}, map[api.ID]*Thread{}
	e.ctx, e.stop = context.WithCancel(m.ctx)
	m.wg.Go(func() { m.link(e, r) })
	return e
}

func (m *Manager) remote(s Server) *Env {
	c := transport.NewEndpointClient(transport.Tailnet{Address: s.Addr, Token: s.Token}, api.ID{})
	return m.launch(&Env{Kind: Remote, Name: s.Label, ep: c}, remote{c})
}

// retire ends e's goroutines; what they still post is dropped.
func (e *Env) retire() {
	e.stop()
	e.gone = true
}

// post runs fn on the Post thread unless e was removed or the manager
// closed by then.
func (m *Manager) post(e *Env, fn func()) {
	m.cfg.Post(func() {
		if !m.closed && !e.gone {
			fn()
		}
	})
}

// A route is how an env reaches its engine. Only the env's link goroutine
// calls it.
type route interface {
	endpoint(ctx context.Context) (api.Endpoint, error)
	// failed is called after a session on the last endpoint failed.
	failed()
	close()
}

// link keeps e's shell stream open, reconnecting with backoff, until e's
// ctx ends or the server refuses the token.
func (m *Manager) link(e *Env, r route) {
	defer r.close()
	ctx := e.ctx
	wait := minBackoff
	for {
		ep, err := r.endpoint(ctx)
		if err == nil {
			err = m.serve(ctx, e, ep, func() { wait = minBackoff })
		}
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, transport.ErrRefused) {
			m.post(e, func() { e.down(Refused{}) })
			return
		}
		r.failed()
		retry := time.Now().Add(wait)
		m.post(e, func() { e.down(Offline{Err: err, RetryAt: retry}) })
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = min(2*wait, maxBackoff)
		m.post(e, func() { e.Status = Connecting{} })
	}
}

// serve reads e's shell until it fails, posting what it reads. online runs
// once the snapshot arrives.
func (m *Manager) serve(ctx context.Context, e *Env, ep api.Endpoint, online func()) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	dial, undial := context.WithTimeout(ctx, connectTimeout)
	info, err := ep.Info(dial)
	var st api.Stream
	if err == nil {
		st, err = ep.Open(dial, api.Subscription{})
	}
	undial()
	if err != nil {
		return err
	}
	defer st.Close()
	s := &session{ctx: ctx, ep: ep}
	for {
		it, err := st.Next(ctx)
		if err != nil {
			return err
		}
		if it.Kind == api.StreamSnapshot {
			online()
		}
		m.post(e, func() { m.shell(e, s, info, it) })
	}
}

func (m *Manager) shell(e *Env, s *session, info api.Environment, it api.StreamItem) {
	switch it.Kind {
	case api.StreamSnapshot:
		e.ID, e.Status, e.session, e.ep = info.ID, Online{}, s, s.ep
		if e.Kind == Local {
			e.Name = info.Name
		}
		live := map[api.ID]bool{}
		for _, v := range it.Snapshot.Threads {
			live[v.ID] = true
			m.view(e, v)
		}
		for id, th := range e.Threads {
			if th.known && !live[id] {
				th.unfollow()
				delete(e.Threads, id)
			}
		}
	case api.StreamThread:
		m.view(e, *it.Thread)
	}
}

func (m *Manager) view(e *Env, v api.Thread) {
	th := e.Threads[v.ID]
	if th == nil {
		th = &Thread{}
		e.Threads[v.ID] = th
	}
	th.View, th.known = v, true
	m.follow(e, th)
}

// down records why e has no session. The session's ctx already ended its
// thread subscriptions.
func (e *Env) down(s Status) {
	e.Status, e.session = s, nil
	for _, th := range e.Threads {
		th.sub = nil
	}
}

// OpenThread follows a thread's events into its Transcript until
// CloseThread, starting from the transcript cached by an earlier open.
func (m *Manager) OpenThread(e *Env, thread api.ID) {
	th := e.Threads[thread]
	if th == nil || th.open {
		return
	}
	if th.Transcript == nil {
		th.Transcript = &Transcript{}
	}
	th.open = true
	m.follow(e, th)
}

// CloseThread stops following a thread. Its transcript stays cached for
// the next OpenThread.
func (m *Manager) CloseThread(e *Env, thread api.ID) {
	th := e.Threads[thread]
	if th == nil || !th.open {
		return
	}
	th.open = false
	th.unfollow()
	th.Pending = slices.DeleteFunc(th.Pending, func(p Pending) bool { return p.sent })
}

func (th *Thread) unfollow() {
	if th.sub != nil {
		th.sub.cancel()
		th.sub = nil
	}
}

func (m *Manager) follow(e *Env, th *Thread) {
	if !th.open || !th.known || th.sub != nil || e.session == nil {
		return
	}
	ctx, cancel := context.WithCancel(e.session.ctx)
	s := &sub{cancel: cancel}
	th.sub = s
	ep, thread, after := e.session.ep, th.View.ID, th.Transcript.Cursor
	m.wg.Go(func() { m.watch(ctx, e, s, ep, thread, after) })
}

// watch reads one thread's subscription, reopening it from the last cursor
// it read when it fails, until ctx ends.
func (m *Manager) watch(ctx context.Context, e *Env, s *sub, ep api.Endpoint, thread api.ID, after api.Cursor) {
	for wait := minBackoff; ; wait = min(2*wait, maxBackoff) {
		st, err := ep.Open(ctx, api.Subscription{Thread: thread, After: after})
		for err == nil {
			var it api.StreamItem
			if it, err = st.Next(ctx); err != nil {
				st.Close()
				break
			}
			switch it.Kind {
			case api.StreamSnapshot:
				after = it.Snapshot.Cursor
			case api.StreamEvent:
				after = it.Event.Cursor
			}
			wait = minBackoff
			m.post(e, func() {
				if th := e.Threads[thread]; th != nil && th.sub == s {
					th.take(it)
				}
			})
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

func (th *Thread) take(it api.StreamItem) {
	switch it.Kind {
	case api.StreamSnapshot:
		*th.Transcript = fold(*it.Snapshot)
	case api.StreamEvent:
		th.Transcript.apply(*it.Event)
	default:
		return
	}
	// A message shows as pending until the transcript has it.
	th.Pending = slices.DeleteFunc(th.Pending, func(p Pending) bool {
		_, message := p.Body.(api.MessageDispatch)
		return message && th.Transcript.message(p.Command) != nil
	})
}

// local attaches to the engine serving the socket under dir, or embeds one
// when none answers. The embedded engine lasts until the manager closes,
// so a run it drives outlives a dropped shell stream.
type local struct {
	dir      string
	attached *transport.EndpointClient
	embedded *daemon.Daemon
	err      error // from closing the embedded engine
}

func (l *local) endpoint(ctx context.Context) (api.Endpoint, error) {
	switch {
	case l.embedded != nil:
		return l.embedded.Endpoint(), nil
	case l.attached != nil:
		return l.attached, nil
	}
	// An engine that took the lock after the probe answers the probe after.
	for range 2 {
		c := transport.NewEndpointClient(daemon.Socket(l.dir), api.ID{})
		probe, cancel := context.WithTimeout(ctx, probeTimeout)
		_, err := c.Info(probe)
		cancel()
		if err == nil {
			l.attached = c
			return c, nil
		}
		c.Close()
		d, err := daemon.Start(ctx, daemon.Defaults(l.dir))
		if errors.Is(err, daemon.ErrLocked) {
			continue
		}
		if err != nil {
			return nil, err
		}
		l.embedded = d
		return d.Endpoint(), nil
	}
	return nil, errors.New("client: another engine holds the lock under " + l.dir + " but does not answer its socket")
}

// failed drops an attached engine, so the next attempt probes again and
// embeds one if it went away.
func (l *local) failed() {
	if l.attached != nil {
		l.attached.Close()
		l.attached = nil
	}
}

func (l *local) close() {
	l.failed()
	if l.embedded != nil {
		l.err = l.embedded.Close()
	}
}

// remote reaches a server over the tailnet. Its client redials on use, so
// a session that failed needs no new one.
type remote struct{ c *transport.EndpointClient }

func (r remote) endpoint(context.Context) (api.Endpoint, error) { return r.c, nil }
func (remote) failed()                                          {}
func (r remote) close()                                         { r.c.Close() }
