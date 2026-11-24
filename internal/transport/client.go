package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"

	"glitched/internal/api"
	"glitched/internal/hub"
	"glitched/internal/provider"
)

// conn dials one socket on first use and again after it drops. Calls in
// flight on a dropped socket fail; nothing is resent. A socket no call or
// stream has used for idleAfter is closed, so a client its owner dropped
// without Close does not hold one forever.
type conn struct {
	t         Transport
	machine   api.ID
	keepalive keepalive
	idleAfter time.Duration

	mu      sync.Mutex
	sock    *socket
	dialing *dialing // nil unless a dial is in flight
	closed  bool
}

// dialing is one dial that every caller needing a socket waits on, each
// with its own ctx. It is given up once none waits.
type dialing struct {
	done    chan struct{}
	stop    context.CancelFunc
	waiters int
	sock    *socket // or err, once done is closed
	err     error
}

func dial(t Transport, machine api.ID) *conn {
	return &conn{t: t, machine: machine, keepalive: defaultKeepalive, idleAfter: time.Minute}
}

var errClosed = errors.New("transport: client closed")

func (c *conn) close() error {
	c.mu.Lock()
	c.closed = true
	s, d := c.sock, c.dialing
	c.sock, c.dialing = nil, nil
	c.mu.Unlock()
	if d != nil {
		d.stop()
		<-d.done
	}
	if s != nil {
		s.close(errClosed)
	}
	return nil
}

func (c *conn) socket(ctx context.Context) (*socket, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, errClosed
	}
	if s := c.sock; s != nil && s.ctx.Err() == nil {
		c.mu.Unlock()
		return s, nil
	}
	d := c.dialing
	if d == nil {
		dctx, stop := context.WithCancel(context.Background())
		d = &dialing{done: make(chan struct{}), stop: stop}
		c.dialing = d
		go c.dial(dctx, d)
	}
	d.waiters++
	c.mu.Unlock()
	select {
	case <-d.done:
		return d.sock, d.err
	case <-ctx.Done():
		c.mu.Lock()
		if d.waiters--; d.waiters == 0 && c.dialing == d {
			c.dialing = nil
			d.stop()
		}
		c.mu.Unlock()
		return nil, ctx.Err()
	}
}

func (c *conn) dial(ctx context.Context, d *dialing) {
	defer close(d.done)
	defer d.stop()
	ws, err := handshake(ctx, c.t, c.machine)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dialing != d {
		// Every caller gave up, or the client closed.
		if err == nil {
			ws.CloseNow()
		}
		d.err = errClosed
		return
	}
	c.dialing = nil
	if err != nil {
		d.err = err
		return
	}
	c.sock = newSocket(ws, c.keepalive, c.idleAfter)
	d.sock = c.sock
}

// The host part is never resolved: Transport.Dial picks the connection.
const origin = "ws://glitched/ws"

// handshake dials machine, opens a WebSocket on the connection and, on a
// credentialed Transport, proves the client's key. It dials before net/http
// does, because net/http detaches its dial from ctx and a dial nobody waits
// for would go on.
func handshake(ctx context.Context, t Transport, machine api.ID) (*websocket.Conn, error) {
	nc, err := t.Dial(ctx, machine)
	if err != nil {
		return nil, fmt.Errorf("transport: %w", err)
	}
	hc := &http.Client{Transport: &http.Transport{
		DialContext: func(context.Context, string, string) (net.Conn, error) { return nc, nil },
	}}
	ws, resp, err := websocket.Dial(ctx, origin, &websocket.DialOptions{HTTPClient: hc, HTTPHeader: http.Header{protocolHeader: {strconv.Itoa(api.Protocol)}}})
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("transport: %w", err)
	}
	// A server of another protocol closes the socket with statusProtocol
	// before the proof. One of this protocol is trusted to announce its
	// own only once it has proved the key.
	if c, ok := t.(credentialed); ok {
		if err := prove(ctx, ws, c.key()); err != nil {
			ws.CloseNow()
			if websocket.CloseStatus(err) == statusProtocol {
				return nil, &api.ProtocolError{Theirs: protocolOf(resp.Header)}
			}
			return nil, err
		}
	}
	if theirs := protocolOf(resp.Header); theirs != api.Protocol {
		ws.CloseNow()
		return nil, &api.ProtocolError{Theirs: theirs}
	}
	return ws, nil
}

type socket struct {
	ws        *websocket.Conn
	ctx       context.Context // done once the socket is dead
	fail      context.CancelCauseFunc
	out       chan []byte // messages for the writer
	idle      *time.Timer // runs while sinks is empty
	idleAfter time.Duration
	running   sync.WaitGroup

	mu     sync.Mutex
	nextID uint64
	sinks  map[uint64]sink // nil once the socket is dead
}

func newSocket(ws *websocket.Conn, k keepalive, idleAfter time.Duration) *socket {
	// An Events page is up to maxPage events of any size; the server is
	// the one this client chose to trust, and proved it.
	ws.SetReadLimit(-1)
	ctx, fail := context.WithCancelCause(context.Background())
	s := &socket{ws: ws, ctx: ctx, fail: fail, out: make(chan []byte), idleAfter: idleAfter, sinks: map[uint64]sink{}}
	s.idle = time.AfterFunc(idleAfter, s.closeIdle)
	s.running.Go(s.read)
	s.running.Go(s.write)
	s.running.Go(func() { k.run(ctx, ws) })
	return s
}

// close ends the socket with cause and returns once its goroutines have.
func (s *socket) close(cause error) {
	s.fail(cause)
	s.ws.CloseNow()
	s.idle.Stop()
	s.running.Wait()
}

var errIdle = errors.New("transport: socket closed while idle")

func (s *socket) closeIdle() {
	s.mu.Lock()
	idle := s.sinks != nil && len(s.sinks) == 0
	if idle {
		s.sinks = nil
		s.fail(errIdle)
	}
	s.mu.Unlock()
	if idle {
		s.ws.CloseNow()
	}
}

// A sink takes the messages for one id on the socket's read goroutine, so it
// must not block.
type sink interface {
	// take reports whether the id is finished.
	take(response) bool
	drop(err error)
}

func (s *socket) add(k sink) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sinks == nil {
		return 0, context.Cause(s.ctx)
	}
	s.idle.Stop()
	s.nextID++
	s.sinks[s.nextID] = k
	return s.nextID, nil
}

// forget reports whether id was still live, so its caller owes the server a
// cancel.
func (s *socket) forget(id uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.sinks[id]
	delete(s.sinks, id)
	if ok && len(s.sinks) == 0 {
		s.idle.Reset(s.idleAfter)
	}
	return ok
}

// send hands b to the writer unless ctx or the socket ends first. A caller
// whose ctx has already ended sends nothing.
func (s *socket) send(ctx context.Context, b []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case s.out <- b:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-s.ctx.Done():
		return context.Cause(s.ctx)
	}
}

// post sends req whatever its caller's ctx, without waiting on the writer.
func (s *socket) post(req request) {
	b, _ := json.Marshal(req)
	go s.send(context.Background(), b)
}

// abandon forgets id and cancels it on the server if it was still live.
func (s *socket) abandon(id uint64) {
	if s.forget(id) {
		s.post(request{ID: id, Cancel: true})
	}
}

// write is the socket's only writer. It writes under the socket's ctx, never
// a caller's: one that ended mid-write would close the socket for every call
// on it.
func (s *socket) write() {
	for {
		select {
		case b := <-s.out:
			if err := writeMessage(s.ctx, s.ws, b); err != nil {
				s.fail(lost(err))
				return
			}
		case <-s.ctx.Done():
			return
		}
	}
}

func (s *socket) read() {
	var err error
	for {
		var data []byte
		if _, data, err = s.ws.Read(s.ctx); err != nil {
			break
		}
		var r response
		if err = json.Unmarshal(data, &r); err != nil {
			break
		}
		s.mu.Lock()
		k := s.sinks[r.ID]
		s.mu.Unlock()
		if k != nil && k.take(r) {
			s.forget(r.ID)
		}
	}
	s.fail(lost(err))
	s.ws.CloseNow()
	err = context.Cause(s.ctx)
	s.mu.Lock()
	sinks := s.sinks
	s.sinks = nil
	s.mu.Unlock()
	for _, k := range sinks {
		k.drop(err)
	}
}

var errLost = errors.New("transport: connection lost")

// lost is why the calls on a dead socket fail. It keeps err's text but not
// its chain, which can hold the socket's own context.Canceled or a library
// timeout that callers would take for their own ctx ending.
func lost(err error) error {
	return fmt.Errorf("%w: %v", errLost, err)
}

type reply struct {
	r   response
	err error
}

type callSink chan reply

func (c callSink) take(r response) bool {
	select {
	case c <- reply{r: r}:
	default:
	}
	return true
}

func (c callSink) drop(err error) {
	select {
	case c <- reply{err: err}:
	default:
	}
}

// start sends a request for method, with k taking the messages for its id.
func (c *conn) start(ctx context.Context, method string, in any, k sink) (*socket, uint64, error) {
	params, err := json.Marshal(in)
	if err != nil {
		return nil, 0, err
	}
	var s *socket
	var id uint64
	for {
		if s, err = c.socket(ctx); err != nil {
			return nil, 0, err
		}
		// A socket that went idle after socket returned it is no failure.
		if id, err = s.add(k); !errors.Is(err, errIdle) {
			break
		}
	}
	if err != nil {
		return nil, 0, err
	}
	b, err := json.Marshal(request{ID: id, Method: method, Params: params})
	if err == nil && len(b) > maxMessage {
		err = fmt.Errorf("%w: %s is %d bytes", errTooLarge, method, len(b))
	}
	if err == nil {
		err = s.send(ctx, b)
	}
	if err != nil {
		s.forget(id)
		return nil, 0, err
	}
	return s, id, nil
}

func (c *conn) call(ctx context.Context, method string, in, out any) error {
	ch := make(callSink, 1)
	s, id, err := c.start(ctx, method, in, ch)
	if err != nil {
		return err
	}
	select {
	case rep := <-ch:
		switch {
		case rep.err != nil:
			return rep.err
		case rep.r.Error != nil:
			return rep.r.Error.err()
		}
		return json.Unmarshal(rep.r.Result, out)
	case <-ctx.Done():
		s.abandon(id)
		return ctx.Err()
	}
}

// stream holds the items read for one subscription until Next takes them, so
// a Next whose ctx ends leaves the stream usable. The server's window bounds
// what it holds.
type stream[T any] struct {
	s    *socket
	id   uint64
	wake chan struct{}

	mu    sync.Mutex
	open  bool
	items []json.RawMessage
	taken int64 // bytes of items Next took and has not acked
	err   error // ends the stream once items are drained
}

func (st *stream[T]) take(r response) bool {
	st.mu.Lock()
	defer st.signal()
	defer st.mu.Unlock()
	switch {
	case r.Open:
		st.open = true
	case r.Error != nil:
		st.end(r.Error.err())
		return true
	default:
		st.items = append(st.items, r.Item)
	}
	return false
}

func (st *stream[T]) drop(err error) {
	st.mu.Lock()
	st.end(err)
	st.mu.Unlock()
	st.signal()
}

func (st *stream[T]) end(err error) {
	if st.err == nil {
		st.err = err
	}
}

func (st *stream[T]) signal() {
	select {
	case st.wake <- struct{}{}:
	default:
	}
}

// open returns once the server has opened the stream or refused it. ctx
// bounds only that wait; Close ends the stream.
func open[T any](ctx context.Context, c *conn, method string, in any) (*stream[T], error) {
	st := &stream[T]{wake: make(chan struct{}, 1)}
	var err error
	// The read goroutine may reach st before s and id are set, but take and
	// drop use neither.
	if st.s, st.id, err = c.start(ctx, method, in, st); err != nil {
		return nil, err
	}
	for {
		st.mu.Lock()
		opened, err := st.open, st.err
		st.mu.Unlock()
		switch {
		case opened:
			return st, nil
		case err != nil:
			return nil, err
		}
		select {
		case <-st.wake:
		case <-ctx.Done():
			st.Close()
			return nil, ctx.Err()
		}
	}
}

func (st *stream[T]) Next(ctx context.Context) (T, error) {
	var zero T
	for {
		st.mu.Lock()
		if len(st.items) > 0 {
			raw := st.items[0]
			st.items[0] = nil
			st.items = st.items[1:]
			// Acking a quarter window at a time keeps the server sending
			// without an ack per item.
			var ack int64
			if st.taken += int64(len(raw)); st.taken >= streamWindow/4 {
				ack, st.taken = st.taken, 0
			}
			st.mu.Unlock()
			if ack > 0 {
				st.s.post(request{ID: st.id, Ack: ack})
			}
			var v T
			if err := json.Unmarshal(raw, &v); err != nil {
				return zero, err
			}
			return v, nil
		}
		err := st.err
		st.mu.Unlock()
		if err != nil {
			return zero, err
		}
		select {
		case <-st.wake:
		case <-ctx.Done():
			return zero, ctx.Err()
		}
	}
}

var errStreamClosed = errors.New("transport: stream closed")

func (st *stream[T]) Close() error {
	st.s.abandon(st.id)
	st.drop(errStreamClosed)
	return nil
}

// EndpointClient is an api.Endpoint on another node.
type EndpointClient struct{ c *conn }

var (
	_ api.Endpoint = (*EndpointClient)(nil)
	_ Projects     = (*EndpointClient)(nil)
)

func NewEndpointClient(t Transport, machine api.ID) *EndpointClient {
	return &EndpointClient{dial(t, machine)}
}

// Close ends the client's calls and streams and closes its socket.
func (e *EndpointClient) Close() error { return e.c.close() }

func (e *EndpointClient) Info(ctx context.Context) (api.Environment, error) {
	var env api.Environment
	return env, e.c.call(ctx, "info", struct{}{}, &env)
}

func (e *EndpointClient) Read(ctx context.Context, q api.Query) (api.Snapshot, error) {
	var snap api.Snapshot
	return snap, e.c.call(ctx, "read", q, &snap)
}

// PluginTools are the tools of the plugins the engine runs.
func (e *EndpointClient) PluginTools(ctx context.Context) (json.RawMessage, error) {
	var out json.RawMessage
	return out, e.c.call(ctx, "plugins.tools", struct{}{}, &out)
}

// PluginTool calls a plugin's tool for an agent.
func (e *EndpointClient) PluginTool(ctx context.Context, call json.RawMessage) (json.RawMessage, error) {
	var out json.RawMessage
	return out, e.c.call(ctx, "plugins.tool", call, &out)
}

// PluginCall calls a method of a plugin's service.
func (e *EndpointClient) PluginCall(ctx context.Context, call json.RawMessage) (json.RawMessage, error) {
	var out json.RawMessage
	return out, e.c.call(ctx, "plugins.call", call, &out)
}

// ProviderDescribe is what the engine's provider name says of itself,
// the one provider asked.
func (e *EndpointClient) ProviderDescribe(ctx context.Context, name string) (provider.Info, error) {
	var out provider.Info
	return out, e.c.call(ctx, "provider.describe", commandsQuery{Provider: name}, &out)
}

// ProviderDefault is the model a session of the engine's provider name
// runs in workspace where it names none, "" where the provider can't say
// one of its own there.
func (e *EndpointClient) ProviderDefault(ctx context.Context, name, workspace string) (string, error) {
	var out string
	return out, e.c.call(ctx, "provider.default", commandsQuery{Provider: name, Workspace: workspace}, &out)
}

// ProviderCommands are the slash commands a session of the engine's
// provider name would have in workspace.
func (e *EndpointClient) ProviderCommands(ctx context.Context, name, workspace string) ([]provider.Command, error) {
	var out []provider.Command
	return out, e.c.call(ctx, "provider.commands", commandsQuery{Provider: name, Workspace: workspace}, &out)
}

// ProviderAsk asks the engine's provider name for one answer, as
// provider.Asker does.
func (e *EndpointClient) ProviderAsk(ctx context.Context, name string, spec provider.AskSpec) (provider.AskResult, error) {
	var out provider.AskResult
	return out, e.c.call(ctx, "provider.ask", askQuery{Provider: name, Spec: spec}, &out)
}

// Providers are every provider the engine serves, each with the models
// its sessions can run, or why it couldn't say. One it doesn't list it
// has no adapter for. Fresh has each ask its program again, rather than
// answer from what it last said; check has the engine look up whether
// each CLI has a newer version.
func (e *EndpointClient) Providers(ctx context.Context, fresh, check bool) ([]ProviderInfo, error) {
	var out []ProviderInfo
	return out, e.c.call(ctx, "providers", providersQuery{Fresh: fresh, Check: check}, &out)
}

// UpdateProvider runs the updater of the CLI of the engine's provider
// name, on the engine's machine.
func (e *EndpointClient) UpdateProvider(ctx context.Context, name string, whenIdle bool) (UpdateResult, error) {
	var out UpdateResult
	return out, e.c.call(ctx, "provider.update", updateQuery{Provider: name, WhenIdle: whenIdle}, &out)
}

// Launches are how each of the engine's providers' programs starts.
func (e *EndpointClient) Launches(ctx context.Context) ([]LaunchInfo, error) {
	var out []LaunchInfo
	return out, e.c.call(ctx, "provider.launches", struct{}{}, &out)
}

// SetLaunch has the engine start provider's program as l says, when
// nothing is wrong with l: the answer says what is, and whether it kept l.
func (e *EndpointClient) SetLaunch(ctx context.Context, provider string, l provider.Launch) (LaunchInfo, error) {
	var out LaunchInfo
	return out, e.c.call(ctx, "provider.launch", launchQuery{Provider: provider, Launch: l}, &out)
}

// AskCredentials opens a credentials request for an agent and waits for
// its answer.
func (e *EndpointClient) AskCredentials(ctx context.Context, a api.Ask) (api.Asked, error) {
	var out api.Asked
	return out, e.c.call(ctx, "credentials.ask", a, &out)
}

// ProvideCredentials gives the engine the values for a credentials
// request.
func (e *EndpointClient) ProvideCredentials(ctx context.Context, p api.Provide) error {
	return e.c.call(ctx, "credentials.provide", p, &struct{}{})
}

// ForgetCredentials deletes the values a thread's vault holds.
func (e *EndpointClient) ForgetCredentials(ctx context.Context, thread api.ID) error {
	return e.c.call(ctx, "credentials.forget", forgetQuery{Thread: thread}, &struct{}{})
}

// Folders lists a folder on the engine's machine.
func (e *EndpointClient) Folders(ctx context.Context, q api.FolderQuery) (api.FolderList, error) {
	var out api.FolderList
	return out, e.c.call(ctx, "projects.folders", q, &out)
}

// Skills says what skills Glitched gives a thread's agent on the
// engine's machine.
func (e *EndpointClient) Skills(ctx context.Context, q api.SkillQuery) (api.SkillList, error) {
	var out api.SkillList
	return out, e.c.call(ctx, "skills.list", q, &out)
}

// SyncSkills offers the engine the skills of the GUI's machine.
func (e *EndpointClient) SyncSkills(ctx context.Context, q api.SkillSync) (api.SkillsSynced, error) {
	var out api.SkillsSynced
	return out, e.c.call(ctx, "skills.sync", q, &out)
}

// PutSkill gives the engine the files of a skill it lacks.
func (e *EndpointClient) PutSkill(ctx context.Context, p api.SkillPut) error {
	return e.c.call(ctx, "skills.put", p, &struct{}{})
}

// DropSkills has the engine drop the skills a machine synced to it.
func (e *EndpointClient) DropSkills(ctx context.Context, d api.SkillDrop) error {
	return e.c.call(ctx, "skills.drop", d, &struct{}{})
}

// Clone clones a repository onto the engine's machine, saying each line
// the clone prints to progress. Its ctx ending stops the clone there.
func (e *EndpointClient) Clone(ctx context.Context, req api.CloneRequest, progress func(string)) (api.Cloned, error) {
	st, err := open[cloneItem](ctx, e.c, "projects.clone", req)
	if err != nil {
		return api.Cloned{}, err
	}
	defer st.Close()
	for {
		item, err := st.Next(ctx)
		switch {
		case err != nil:
			return api.Cloned{}, err
		case item.Path != "":
			return api.Cloned{Path: item.Path}, nil
		}
		progress(item.Line)
	}
}

func (e *EndpointClient) Submit(ctx context.Context, cmd api.Command) (api.Receipt, error) {
	var r api.Receipt
	return r, e.c.call(ctx, "submit", cmd, &r)
}

func (e *EndpointClient) Open(ctx context.Context, s api.Subscription) (api.Stream, error) {
	return open[api.StreamItem](ctx, e.c, "subscribe", s)
}

// HubClient reaches the hub replica on another node. It is the uploader's
// hub.Client.
type HubClient struct{ c *conn }

var _ hub.Client = (*HubClient)(nil)

func NewHubClient(t Transport, machine api.ID) *HubClient {
	return &HubClient{dial(t, machine)}
}

// Close ends the client's calls and streams and closes its socket.
func (h *HubClient) Close() error { return h.c.close() }

func (h *HubClient) Append(ctx context.Context, u api.Upload) (api.UploadAck, error) {
	var ack api.UploadAck
	err := h.c.call(ctx, "hub.append", u, &ack)
	if err == nil {
		err = sameProtocol(ack)
	}
	return ack, err
}

func (h *HubClient) Acked(ctx context.Context, log api.ID) (uint64, error) {
	var ack api.UploadAck
	err := h.c.call(ctx, "hub.acked", ackedQuery{Log: log}, &ack)
	if err == nil {
		err = sameProtocol(ack)
	}
	return ack.Acked, err
}

// sameProtocol refuses a hub of another protocol by its ack. One older
// than the field acks with none, and would keep a deleted thread readable.
func sameProtocol(ack api.UploadAck) error {
	if ack.Protocol != api.Protocol {
		return fmt.Errorf("%w: the hub speaks protocol %d, this engine %d", api.ErrProtocol, ack.Protocol, api.Protocol)
	}
	return nil
}

// Events reads one page of the replica; see hub.Replica.Events.
func (h *HubClient) Events(ctx context.Context, thread api.ID, after api.Cursor, limit int) ([]api.Event, error) {
	var page eventsPage
	err := h.c.call(ctx, "hub.events", eventsQuery{Thread: thread, After: after, Limit: limit}, &page)
	return page.Events, err
}

// EventStream is a live read of the replica.
type EventStream interface {
	Next(ctx context.Context) (api.Event, error)
	Close() error
}

// Follow is hub.Replica.Follow across the wire.
func (h *HubClient) Follow(ctx context.Context, thread api.ID, after api.Cursor) (EventStream, error) {
	return open[api.Event](ctx, h.c, "hub.follow", eventsQuery{Thread: thread, After: after})
}
