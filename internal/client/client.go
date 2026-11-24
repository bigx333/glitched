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
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"glitched/internal/api"
	"glitched/internal/daemon"
	"glitched/internal/jsonfile"
	"glitched/internal/transport"
)

type Config struct {
	// Dir is the local engine's state directory, and holds gui.json.
	Dir string
	// Post runs fn on the thread that owns the manager's state, in the order
	// posted, and must not block; the GUI passes its window's Update.
	Post func(fn func())
	// Asked, when set, runs on the Post thread once for each thread that
	// asks the user something the user has not heard of. A child is told
	// of on its Blocked report, or on a reconnect that finds the ask,
	// whichever the env sees first; nothing it asks at its first snapshot
	// of a log is told of but by a report after, or a reconnect that finds
	// its parent reported to while away. A thread of the user's, whose
	// parent is zero, is told of on the view that first asks it, or on a
	// reconnect that finds the ask; not for what it asks at the first
	// snapshot of a log.
	Asked func(e *Env, parent, child api.ID)
	// Engine, when set, changes how an engine embedded here serves, after
	// engine.json: the GUI has it run the real Claude Code. Left nil, as a
	// test leaves it, the engine plays every provider with the fake.
	Engine func(*daemon.Config)
}

const (
	probeTimeout   = 750 * time.Millisecond
	probeInterval  = 50 * time.Millisecond
	connectTimeout = 10 * time.Second
	addTimeout     = 5 * time.Second
	submitTimeout  = 10 * time.Second
	// submitsAtOnce is how many of an env's commands wait on its engine
	// at once. The engine commits one at a time, so a command sent beside
	// many others waits behind them there; with this many at most, its
	// submitTimeout covers its own commit and a few others', not a queue
	// as long as the commands sent.
	submitsAtOnce = 8
	minBackoff    = time.Second
	maxBackoff    = 30 * time.Second
)

// Manager owns the environments. Call its methods and read what they
// return only on the Post thread.
type Manager struct {
	cfg    Config
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	local  *local

	envs  []*Env
	saved saved
	// saving is whether a goroutine is saving the read marks; unsaved,
	// whether some are marked that no save has taken.
	saving, unsaved bool
	closed          bool

	machine    Machine
	inspecting bool
	// changes counts the changes made here to engine.json and tokens.json,
	// so a read begun before one is dropped for a new one.
	changes int
	// edits counts the changes the GUI made to threads here: the commands
	// it submitted or retried and the threads it read.
	edits uint64
	// node is the embedded engine's own tailnet node, which remotes dial
	// through while it runs; the link goroutines read it, so it's atomic.
	node atomic.Pointer[node]
	// restarting: Restart stopped the embedded engine and no new one has
	// reported in; again, whether Restart was called meanwhile.
	restarting, again bool
	// embedded is the engine embedded here, once it reported in.
	embedded *daemon.Daemon
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
	if err := jsonfile.Load(m.savedPath(), &m.saved); err != nil {
		return err
	}
	m.local = m.newLocal()
	m.envs = []*Env{m.launch(&Env{Kind: Local, Name: "local"}, m.local)}
	m.sync("")
	return nil
}

// Close stops every environment and an embedded engine, saves the read
// marks no save has, and returns once their goroutines have.
func (m *Manager) Close() error {
	if m.closed {
		return nil
	}
	m.closed = true
	m.cancel()
	m.wg.Wait()
	// A save that was under way posted whether it failed too late to tell.
	var err error
	if m.unsaved || m.saving {
		_, err = m.edit(func(f *saved) error { f.see(m.saved.Seen); return nil })
	}
	if m.local != nil {
		err = errors.Join(err, m.local.err)
	}
	return err
}

// Envs is the local environment, then the remotes, in gui.json's order.
// The slice is the manager's: read it, don't change it.
func (m *Manager) Envs() []*Env { return m.envs }

// holder is the first env listed that reaches engine id, which shows it.
func (m *Manager) holder(id api.ID) *Env {
	if id.IsZero() {
		return nil
	}
	if i := slices.IndexFunc(m.envs, func(e *Env) bool { return e.ID == id }); i >= 0 {
		return m.envs[i]
	}
	return nil
}

// ShownBy is the env that shows the engine e reaches: the first listed that
// reaches it, which may be e, or e while its engine is unknown. An env
// another shows is a duplicate, whatever its status says between retries.
func (m *Manager) ShownBy(e *Env) *Env {
	if h := m.holder(e.ID); h != nil {
		return h
	}
	return e
}

type Kind uint8

const (
	Local Kind = iota
	Remote
)

// Status is sealed: Connecting, Online, Offline, Refused, Incompatible or
// Duplicate.
type Status interface{ status() }

type Connecting struct{}

type Online struct{}

// Offline: the last attempt failed with Err, and the next starts at RetryAt.
type Offline struct {
	Err     error
	RetryAt time.Time
}

// Refused: the server refused the handshake. It holds no key for this
// token, or it isn't the paired engine. Nothing retries until the server
// entry changes.
type Refused struct{}

// Incompatible: the engine speaks protocol Theirs and this client Ours, so
// none of its threads show. The next attempt, at RetryAt, may find it
// upgraded.
type Incompatible struct {
	Theirs, Ours int
	RetryAt      time.Time
}

// Duplicate: the engine is the one an env listed before this one reaches,
// named Of, which shows it. None of this one's threads show. Later attempts
// may find it another engine.
type Duplicate struct{ Of string }

func (Connecting) status()   {}
func (Online) status()       {}
func (Offline) status()      {}
func (Refused) status()      {}
func (Incompatible) status() {}
func (Duplicate) status()    {}

type duplicateError struct{ of string }

func (e *duplicateError) Error() string { return "client: the engine is listed already, as " + e.of }

type Env struct {
	ID      api.ID // the engine it last reached; zero until it first answers
	Name    string // the host's name for the local engine, the server's label for a remote
	Kind    Kind
	Status  Status
	Threads map[api.ID]*Thread
	// Membership counts the threads that came to Threads or left it,
	// whether here, as a thread created or dismissed, or on the engine.
	Membership uint64

	ctx     context.Context // ends when the env is removed or the manager closes
	stop    context.CancelFunc
	unlink  context.CancelFunc // ends the link that reaches the engine
	session *session           // the live shell stream; nil while there is none
	log     api.ID             // the log of the last shell snapshot; zero before one
	waiting []chan<- *session  // commands waiting for the next session
	slots   chan struct{}      // a command's while it waits on the engine; submitsAtOnce of them
	server  Server             // a remote's entry in gui.json
}

// Log is the log of e's last shell snapshot, zero before one. Another is
// the engine's log rewound or replaced, as by a restore of its database.
func (e *Env) Log() api.ID { return e.log }

// session is one shell stream, and where commands go while it lasts.
// Thread subscriptions run under its ctx, so they end with it.
type session struct {
	ctx context.Context
	ep  api.Endpoint
}

type Thread struct {
	View       api.Thread
	Transcript *Transcript // nil until opened
	Pending    []Pending

	known bool             // the engine sent a view; false for a thread created here and not yet confirmed
	told  map[api.ID]heard // what the user heard of each request the view asks
	open  bool
	sub   *sub
	last  chan struct{} // closed once the thread's latest submit returns
}

// heard is what the user heard of a request a child asks.
type heard uint8

const (
	unheard heard = iota
	// before was pending at the env's first snapshot of the log. Its
	// report may have come before the env did, so only a report after
	// tells of it: one the env sees, or one to its parent while away.
	before
	notified
)

type sub struct{ cancel context.CancelFunc }

func (m *Manager) launch(e *Env, r route) *Env {
	e.Status, e.Threads, e.slots = Connecting{}, map[api.ID]*Thread{}, make(chan struct{}, submitsAtOnce)
	e.Membership++
	e.ctx, e.stop = context.WithCancel(m.ctx)
	m.connect(e, r)
	return e
}

func (m *Manager) connect(e *Env, r route) {
	var ctx context.Context
	ctx, e.unlink = context.WithCancel(e.ctx)
	m.wg.Go(func() { m.link(ctx, e, r) })
}

// redial reaches e's server at s from now on. e keeps its threads and the
// commands pending on them.
func (m *Manager) redial(e *Env, s Server) {
	e.unlink()
	m.down(e, Connecting{})
	e.server = s
	m.connect(e, m.newRemote(s))
}

func (m *Manager) remote(s Server) *Env {
	return m.launch(&Env{ID: s.Engine, Kind: Remote, Name: s.Label, server: s}, m.newRemote(s))
}

// post runs fn on the Post thread unless ctx ended by then. Only the Post
// thread cancels the contexts passed here, so fn never sees a later state
// than the one that ended them.
func (m *Manager) post(ctx context.Context, fn func()) {
	m.cfg.Post(func() {
		if ctx.Err() == nil {
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

// link keeps e's shell stream open, reconnecting with backoff, until ctx
// ends or the server refuses the handshake.
func (m *Manager) link(ctx context.Context, e *Env, r route) {
	defer r.close()
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
			m.post(ctx, func() { m.down(e, Refused{}) })
			return
		}
		r.failed()
		retry := time.Now().Add(wait)
		var s Status = Offline{Err: err, RetryAt: retry}
		if pe := (*api.ProtocolError)(nil); errors.As(err, &pe) {
			s = Incompatible{Theirs: pe.Theirs, Ours: api.Protocol, RetryAt: retry}
		}
		if de := (*duplicateError)(nil); errors.As(err, &de) {
			s = Duplicate{Of: de.of}
		}
		m.post(ctx, func() { m.down(e, s) })
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = min(2*wait, maxBackoff)
		m.post(ctx, func() { m.down(e, Connecting{}) })
	}
}

// serve reads e's shell until it fails, posting what it reads under ctx.
// online runs once the snapshot arrives.
func (m *Manager) serve(ctx context.Context, e *Env, ep api.Endpoint, online func()) error {
	s := &session{ep: ep}
	var cancel context.CancelFunc
	s.ctx, cancel = context.WithCancel(ctx)
	defer cancel()
	dial, undial := context.WithTimeout(s.ctx, connectTimeout)
	info, err := ep.Info(dial)
	if err == nil && info.Protocol != api.Protocol {
		err = &api.ProtocolError{Theirs: info.Protocol}
	}
	if err == nil {
		err = m.claim(dial, e, info.ID)
	}
	var st api.Stream
	if err == nil {
		st, err = ep.Open(dial, api.Subscription{})
	}
	undial()
	if err != nil {
		return err
	}
	defer st.Close()
	for {
		it, err := st.Next(s.ctx)
		if err != nil {
			return err
		}
		if it.Kind == api.StreamSnapshot {
			online()
		}
		m.post(ctx, func() { m.shell(e, s, info, it) })
	}
}

func (m *Manager) shell(e *Env, s *session, info api.Environment, it api.StreamItem) {
	switch it.Kind {
	case api.StreamSnapshot:
		e.ID, e.Status, e.session = info.ID, Online{}, s
		if e.Kind == Local {
			e.Name = info.Name
		}
		for _, w := range e.waiting {
			w <- s
		}
		e.waiting = nil
		// A snapshot of another log than the views held, as on the first
		// connect or from a restored database, tells of nothing: its
		// reports came before, or the views held say nothing of them.
		same := it.Snapshot.Cursor.Log == e.log
		e.log = it.Snapshot.Cursor.Log
		live := map[api.ID]bool{}
		moved := map[api.ID]bool{} // parents reported to while away
		for _, v := range it.Snapshot.Threads {
			live[v.ID] = true
			moved[v.ID] = m.view(e, v) && same
		}
		for id, th := range e.Threads {
			if th.known && !live[id] {
				th.unfollow()
				delete(e.Threads, id)
				e.Membership++
			}
		}
		for _, v := range it.Snapshot.Threads {
			th := e.Threads[v.ID]
			if !same {
				for _, r := range v.Requests {
					th.told[r.ID] = max(th.told[r.ID], before)
				}
				continue
			}
			// A report while away may be the child's own, hidden by a
			// sibling's after it, so it tells of all the child asks.
			enough := before
			if !v.Parent.IsZero() && moved[v.Parent] {
				enough = notified
			}
			m.tell(e, th, enough)
		}
	case api.StreamThread:
		if m.view(e, *it.Thread) {
			m.reported(e, *it.Thread.LastReport)
		}
		if th := e.Threads[it.Thread.ID]; th != nil && it.Thread.Parent.IsZero() {
			m.tell(e, th, before)
		}
	}
}

// reported tells of the child that made r if r says it waits on the user
// for something the user has not been told of.
func (m *Manager) reported(e *Env, r api.Report) {
	if th := e.Threads[r.Child]; th != nil && r.Kind == api.ReportBlocked {
		m.tell(e, th, notified)
	}
}

// tell tells of th if it asks a request the user heard less of than
// enough, and marks all it asks as told of.
func (m *Manager) tell(e *Env, th *Thread, enough heard) {
	if !slices.ContainsFunc(th.View.Requests, func(r api.Request) bool { return th.told[r.ID] < enough }) {
		return
	}
	for _, r := range th.View.Requests {
		th.told[r.ID] = notified
	}
	if m.cfg.Asked != nil {
		m.cfg.Asked(e, th.View.Parent, th.View.ID)
	}
}

// view takes v in place of the thread's view, and is whether v carries a
// report the view held did not. The shell stream is in commit order, so
// the view of the child that made the report came before it.
func (m *Manager) view(e *Env, v api.Thread) bool {
	th := e.Threads[v.ID]
	if v.Deleted {
		if th != nil {
			th.unfollow()
			delete(e.Threads, v.ID)
			e.Membership++
		}
		return false
	}
	if th == nil {
		th = &Thread{}
		e.Threads[v.ID] = th
		e.Membership++
	}
	was, seen := th.View.LastReport, th.known
	th.View, th.known = v, true
	th.configured(e.log)
	if th.told == nil {
		th.told = map[api.ID]heard{}
	}
	maps.DeleteFunc(th.told, func(id api.ID, _ heard) bool {
		return !slices.ContainsFunc(v.Requests, func(r api.Request) bool { return r.ID == id })
	})
	th.dropUnasked()
	m.follow(e, th)
	r := v.LastReport
	return seen && r != nil && (was == nil || *was != *r)
}

// claim records that e reaches engine id, and fails if an env listed
// before e reaches it too. An env listed after e that reaches it becomes
// the duplicate. A zero ID claims nothing.
func (m *Manager) claim(ctx context.Context, e *Env, id api.ID) error {
	got := make(chan error, 1)
	m.post(ctx, func() {
		e.ID = id
		if id.IsZero() {
			got <- nil
			return
		}
		if h := m.holder(id); h != e {
			got <- &duplicateError{h.Name}
			return
		}
		for _, o := range m.envs[slices.Index(m.envs, e)+1:] {
			if _, dup := o.Status.(Duplicate); o.ID == id && !dup {
				o.unlink()
				m.down(o, Duplicate{Of: e.Name})
				m.connect(o, m.newRemote(o.server))
			}
		}
		got <- nil
	})
	select {
	case err := <-got:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// down records why e has no session. The session's ctx already ended its
// thread subscriptions. A duplicate drops its threads, which the env
// listed first shows.
func (m *Manager) down(e *Env, s Status) {
	e.Status, e.session = s, nil
	for _, th := range e.Threads {
		th.sub = nil
	}
	if _, dup := s.(Duplicate); dup {
		clear(e.Threads)
		e.Membership++
		e.log = api.ID{}
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
// the next OpenThread, but no longer keeps the answers, failed or taken,
// to requests only it showed waiting.
func (m *Manager) CloseThread(e *Env, thread api.ID) {
	th := e.Threads[thread]
	if th == nil || !th.open {
		return
	}
	th.open = false
	th.unfollow()
	th.Pending = slices.DeleteFunc(th.Pending, func(p Pending) bool {
		_, message := p.Body.(api.MessageDispatch)
		return message && p.sent
	})
	th.dropUnasked()
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
// it read when it fails, until ctx ends or the thread is gone: past its
// tombstone the engine finds it no more. It folds a snapshot itself, as a
// long thread's takes a while, and posts the transcript.
func (m *Manager) watch(ctx context.Context, e *Env, s *sub, ep api.Endpoint, thread api.ID, after api.Cursor) {
	deleted := false
	for wait := minBackoff; ; wait = min(2*wait, maxBackoff) {
		st, err := ep.Open(ctx, api.Subscription{Thread: thread, After: after})
		for err == nil {
			var it api.StreamItem
			if it, err = st.Next(ctx); err != nil {
				st.Close()
				break
			}
			wait = minBackoff
			var take func(*Transcript)
			switch it.Kind {
			case api.StreamSnapshot:
				after = it.Snapshot.Cursor
				tr := fold(*it.Snapshot)
				deleted = tr.Deleted
				take = func(t *Transcript) { t.Replace(tr) }
			case api.StreamEvent:
				after = it.Event.Cursor
				deleted = deleted || it.Event.Type == api.EventType(api.ThreadDeleted{})
				take = func(t *Transcript) { t.apply(*it.Event) }
			default:
				continue
			}
			m.post(e.ctx, func() {
				if th := e.Threads[thread]; th != nil && th.sub == s {
					take(th.Transcript)
					th.shown()
					th.dropUnasked()
				}
			})
		}
		if deleted && errors.Is(err, api.ErrNotFound) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// shown drops the rows of the messages the transcript now has.
func (th *Thread) shown() {
	th.Pending = slices.DeleteFunc(th.Pending, func(p Pending) bool {
		_, message := p.Body.(api.MessageDispatch)
		return message && th.Transcript.message(p.Command) != nil
	})
}

// configured drops the changes the engine took once the view has them:
// a view of the log that took them, at the revision their receipt gave or
// a later one. A view of another log, as a restored database's snapshot
// sends, has none of that log's revisions, so they go for the thread to
// show what the view does.
func (th *Thread) configured(log api.ID) {
	th.Pending = slices.DeleteFunc(th.Pending, func(p Pending) bool {
		return inView(p.Body) && p.sent && (p.log != log || p.at <= th.View.Revision)
	})
}

// dropUnasked drops the answers, failed or taken, to requests the thread
// no longer asks, as neither its view nor its open transcript, whichever
// is behind, shows them waiting: a failed one can land no more, and the
// request shows how it ended.
func (th *Thread) dropUnasked() {
	th.Pending = slices.DeleteFunc(th.Pending, func(p Pending) bool {
		r, ok := p.Body.(api.RequestRespond)
		return ok && (p.Err != nil || p.sent) && !th.asks(r.Request)
	})
}

func (th *Thread) asks(request api.ID) bool {
	if slices.ContainsFunc(th.View.Requests, func(r api.Request) bool { return r.ID == request }) {
		return true
	}
	if th.open {
		r := th.Transcript.request(request)
		return r != nil && r.State == api.RequestPending
	}
	return false
}

// local attaches to the engine serving the socket under dir, or embeds one
// when none answers. The embedded engine lasts until the manager closes,
// so a run it drives outlives a dropped shell stream.
type local struct {
	dir      string
	hosted   func(embedded *daemon.Daemon) // reports the engine embedded, nil for one attached
	engine   func(*daemon.Config)          // Config.Engine
	attached *transport.EndpointClient
	embedded *daemon.Daemon
	err      error         // from closing the embedded engine
	done     chan struct{} // closed once close returns
}

func (l *local) endpoint(ctx context.Context) (api.Endpoint, error) {
	switch {
	case l.embedded != nil:
		return l.embedded.Endpoint(), nil
	case l.attached != nil:
		return l.attached, nil
	}
	// An engine holds the lock while it starts, and answers once it serves.
	deadline := time.Now().Add(connectTimeout)
	for {
		c := transport.NewEndpointClient(daemon.Socket(l.dir), api.ID{})
		probe, cancel := context.WithTimeout(ctx, probeTimeout)
		_, err := c.Info(probe)
		cancel()
		if err == nil {
			l.attached = c
			l.hosted(nil)
			return c, nil
		}
		c.Close()
		cfg, err := daemon.Defaults(l.dir)
		if err != nil {
			return nil, err
		}
		if l.engine != nil {
			l.engine(&cfg)
		}
		d, err := daemon.Start(ctx, cfg)
		switch {
		case err == nil:
			l.embedded = d
			l.hosted(d)
			return d.Endpoint(), nil
		case !errors.Is(err, daemon.ErrLocked):
			return nil, err
		case time.Now().After(deadline):
			return nil, errors.New("client: another engine holds the lock under " + l.dir + " but does not answer its socket")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(probeInterval):
		}
	}
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
	close(l.done)
}

// node wraps a tailnet node for an atomic.Pointer.
type node struct{ transport.Node }

// remote reaches a server over the tailnet: through the embedded engine's
// own node while it runs one, else through the installed Tailscale. Its
// client redials on use, so a session that failed needs no new one.
type remote struct {
	s   Server
	via *atomic.Pointer[node]
	c   *transport.EndpointClient
	on  *node // the node c dials through; nil for the installed Tailscale
}

func (m *Manager) newRemote(s Server) *remote { return &remote{s: s, via: &m.node} }

func (r *remote) endpoint(context.Context) (api.Endpoint, error) {
	if n := r.via.Load(); r.c == nil || n != r.on {
		r.close()
		r.on = n
		var tr transport.Transport = transport.Tailnet{Address: r.s.Addr, Token: r.s.Token}
		if n != nil {
			tr = transport.Embedded{Node: n.Node, Address: r.s.Addr, Token: r.s.Token}
		}
		r.c = transport.NewEndpointClient(tr, api.ID{})
	}
	return r.c, nil
}

func (*remote) failed() {}

func (r *remote) close() {
	if r.c != nil {
		r.c.Close()
	}
}
