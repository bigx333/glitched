package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"

	"github.com/coder/websocket"

	"glitched/internal/api"
	"glitched/internal/hub"
)

// conn dials one socket on first use and again after it drops. Calls in
// flight on a dropped socket fail; nothing is resent.
type conn struct {
	hc *http.Client

	mu   sync.Mutex
	sock *socket
}

func dial(t Transport, machine api.ID) *conn {
	return &conn{hc: &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return t.Dial(ctx, machine) },
	}}}
}

// The host part is never resolved: Transport.Dial picks the connection.
const origin = "ws://glitched/ws"

// streamBytes bounds the items a stream holds for a reader that has fallen
// behind, as the runtime bounds its subscribers.
const streamBytes = 8 << 20

func (c *conn) socket(ctx context.Context) (*socket, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sock != nil && c.sock.ctx.Err() == nil {
		return c.sock, nil
	}
	ws, _, err := websocket.Dial(ctx, origin, &websocket.DialOptions{HTTPClient: c.hc})
	if err != nil {
		return nil, fmt.Errorf("transport: %w", err)
	}
	// An Events page is up to maxPage events of any size; the server is
	// the one this client chose to trust.
	ws.SetReadLimit(-1)
	sctx, fail := context.WithCancelCause(context.Background())
	c.sock = &socket{ws: ws, ctx: sctx, fail: fail, sinks: map[uint64]sink{}}
	go c.sock.read()
	go keepAlive(sctx, ws)
	return c.sock, nil
}

type socket struct {
	ws   *websocket.Conn
	ctx  context.Context // done once the socket is dead
	fail context.CancelCauseFunc

	mu     sync.Mutex
	nextID uint64
	sinks  map[uint64]sink // nil once the socket is dead
}

// A sink takes the frames for one id on the socket's read goroutine, so it
// must not block.
type sink interface {
	// frame reports whether the id is finished.
	frame(response) bool
	drop(err error)
}

func (s *socket) add(k sink) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sinks == nil {
		return 0, context.Cause(s.ctx)
	}
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
	return ok
}

func (s *socket) send(req request) error {
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	// Never the caller's ctx: one that ends mid-write closes the socket for
	// every other call on it.
	if err := s.ws.Write(s.ctx, websocket.MessageText, b); err != nil {
		s.fail(lost(err))
		return context.Cause(s.ctx)
	}
	return nil
}

func (s *socket) cancel(id uint64) {
	go s.send(request{ID: id, Cancel: true})
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
		if k != nil && k.frame(r) {
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

func (c callSink) frame(r response) bool {
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

func (c *conn) call(ctx context.Context, method string, in, out any) error {
	params, err := json.Marshal(in)
	if err != nil {
		return err
	}
	s, err := c.socket(ctx)
	if err != nil {
		return err
	}
	ch := make(callSink, 1)
	id, err := s.add(ch)
	if err != nil {
		return err
	}
	if err := s.send(request{ID: id, Method: method, Params: params}); err != nil {
		s.forget(id)
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
		if s.forget(id) {
			s.cancel(id)
		}
		return ctx.Err()
	}
}

// stream holds the items read for one subscription until Next takes them, so
// a Next whose ctx ends leaves the stream usable.
type stream[T any] struct {
	s    *socket
	id   uint64
	wake chan struct{}

	mu    sync.Mutex
	open  bool
	items []json.RawMessage
	bytes int
	err   error // ends the stream once items are drained
}

func (st *stream[T]) frame(r response) bool {
	st.mu.Lock()
	defer st.signal()
	defer st.mu.Unlock()
	switch {
	case r.Open:
		st.open = true
	case r.Error != nil:
		st.end(r.Error.err())
		return true
	case len(st.items) > 0 && st.bytes+len(r.Item) > streamBytes:
		st.err, st.items, st.bytes = api.ErrOverflow, nil, 0
		st.s.cancel(st.id)
		return true
	default:
		st.items = append(st.items, r.Item)
		st.bytes += len(r.Item)
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
	params, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	s, err := c.socket(ctx)
	if err != nil {
		return nil, err
	}
	st := &stream[T]{s: s, wake: make(chan struct{}, 1)}
	if st.id, err = s.add(st); err != nil {
		return nil, err
	}
	if err := s.send(request{ID: st.id, Method: method, Params: params}); err != nil {
		s.forget(st.id)
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
			st.items = st.items[1:]
			st.bytes -= len(raw)
			st.mu.Unlock()
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
	if st.s.forget(st.id) {
		st.s.cancel(st.id)
	}
	st.drop(errStreamClosed)
	return nil
}

// EndpointClient is an api.Endpoint on another node.
type EndpointClient struct{ c *conn }

var _ api.Endpoint = (*EndpointClient)(nil)

func NewEndpointClient(t Transport, machine api.ID) *EndpointClient {
	return &EndpointClient{dial(t, machine)}
}

func (e *EndpointClient) Info(ctx context.Context) (api.Environment, error) {
	var env api.Environment
	return env, e.c.call(ctx, "info", struct{}{}, &env)
}

func (e *EndpointClient) Read(ctx context.Context, q api.Query) (api.Snapshot, error) {
	var snap api.Snapshot
	return snap, e.c.call(ctx, "read", q, &snap)
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

func (h *HubClient) Append(ctx context.Context, u api.Upload) (api.UploadAck, error) {
	var ack api.UploadAck
	return ack, h.c.call(ctx, "hub.append", u, &ack)
}

func (h *HubClient) Acked(ctx context.Context, log api.ID) (uint64, error) {
	var ack api.UploadAck
	err := h.c.call(ctx, "hub.acked", ackedQuery{Log: log}, &ack)
	return ack.Acked, err
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
