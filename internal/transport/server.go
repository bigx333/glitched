package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"glitched/internal/api"
	"glitched/internal/hub"
)

// Server serves an Endpoint and a hub replica on /ws. Either may be nil, and
// its methods are then not served.
type Server struct {
	Transport Transport
	Endpoint  api.Endpoint
	Hub       *hub.Replica
	// Authorize admits an upgrade or refuses it with 401. A Tailnet server
	// must have one. A socket it admits closes once the returned ctx ends, as
	// when its grant is revoked.
	Authorize func(*http.Request) (context.Context, error)

	keepalive keepalive // zero means defaultKeepalive
}

type connKey struct{}

// Serve answers sockets on ln until ctx ends, then closes them and returns
// once their calls have.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	if _, ok := s.Transport.(credentialed); ok && s.Authorize == nil {
		ln.Close()
		return fmt.Errorf("transport: a %T server needs Authorize to check its clients' tokens", s.Transport)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		mu      sync.Mutex
		closed  bool
		sockets sync.WaitGroup
	)
	methods := s.methods()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if closed {
			mu.Unlock()
			http.Error(w, "shutting down", http.StatusServiceUnavailable)
			return
		}
		sockets.Add(1)
		mu.Unlock()
		defer sockets.Done()
		s.serveSocket(ctx, w, r, methods)
	})
	hs := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			return context.WithValue(ctx, connKey{}, c)
		},
	}
	stop := context.AfterFunc(ctx, func() { hs.Close() })
	defer stop()
	err := hs.Serve(ln)
	cancel()
	mu.Lock()
	closed = true
	mu.Unlock()
	sockets.Wait()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func (s *Server) serveSocket(ctx context.Context, w http.ResponseWriter, r *http.Request, methods map[string]handler) {
	if _, err := s.Transport.WhoIs(r.Context().Value(connKey{}).(net.Conn)); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	grant := context.Background()
	if s.Authorize != nil {
		var err error
		if grant, err = s.Authorize(r); err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
	}
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer ws.CloseNow()
	ws.SetReadLimit(maxMessage)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer context.AfterFunc(grant, cancel)()
	k := s.keepalive
	if k == (keepalive{}) {
		k = defaultKeepalive
	}
	go k.run(ctx, ws)

	// A failed write closes ws, which ends the read loop below.
	send := func(f response) {
		b, _ := json.Marshal(f)
		writeMessage(ctx, ws, b)
	}
	var (
		mu    sync.Mutex
		live  = map[uint64]*op{}
		calls sync.WaitGroup
	)
	defer calls.Wait()
	defer cancel()
	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		var req request
		if json.Unmarshal(data, &req) != nil {
			ws.Close(websocket.StatusInvalidFramePayloadData, "malformed message")
			return
		}
		mu.Lock()
		o := live[req.ID]
		switch {
		case req.Cancel:
			if o != nil {
				o.stop()
			}
			mu.Unlock()
			continue
		case req.Ack > 0:
			if o != nil {
				o.ack(req.Ack)
			}
			mu.Unlock()
			continue
		}
		h, known := methods[req.Method]
		if !known || o != nil {
			mu.Unlock()
			send(response{ID: req.ID, Error: &wireError{Code: "bad_request", Message: "unknown method or id in use: " + req.Method}})
			continue
		}
		callCtx, stop := context.WithCancel(ctx)
		o = &op{stop: stop, acked: make(chan struct{}, 1), send: func(f response) {
			f.ID = req.ID
			send(f)
		}}
		live[req.ID] = o
		mu.Unlock()
		calls.Go(func() {
			defer func() {
				mu.Lock()
				delete(live, req.ID)
				mu.Unlock()
				stop()
			}()
			defer func() {
				if p := recover(); p != nil {
					slog.Error("transport: handler panicked", "method", req.Method, "panic", p, "stack", string(debug.Stack()))
					o.send(response{Error: &wireError{Code: "internal", Message: fmt.Sprintf("%s panicked: %v", req.Method, p)}})
				}
			}()
			h(callCtx, req.Params, o)
		})
	}
}

// An op is one call or stream in flight on a socket.
type op struct {
	stop    context.CancelFunc
	send    func(response)
	unacked atomic.Int64 // bytes of stream items sent and not acked
	acked   chan struct{}
}

func (o *op) ack(n int64) {
	o.unacked.Add(-n)
	select {
	case o.acked <- struct{}{}:
	default:
	}
}

// room waits until fewer than streamWindow bytes are unacked.
func (o *op) room(ctx context.Context) error {
	for o.unacked.Load() >= streamWindow {
		select {
		case <-o.acked:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// writeMessage writes b in frames of at most 32 KiB. The library writes a
// pong under the same lock as a frame and drops the socket when that takes
// 5s, so one frame for a large message on a slow link would kill it.
func writeMessage(ctx context.Context, ws *websocket.Conn, b []byte) error {
	w, err := ws.Writer(ctx, websocket.MessageText)
	if err != nil {
		return err
	}
	for len(b) > 0 {
		n := min(len(b), 32<<10)
		if _, err := w.Write(b[:n]); err != nil {
			return err
		}
		b = b[n:]
	}
	return w.Close()
}

// keepalive is how often a socket pings its peer and how late a pong may be
// before the socket is dropped.
type keepalive struct{ every, within time.Duration }

var defaultKeepalive = keepalive{every: 15 * time.Second, within: 10 * time.Second}

func (k keepalive) run(ctx context.Context, ws *websocket.Conn) {
	t := time.NewTicker(k.every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		pctx, cancel := context.WithTimeout(ctx, k.within)
		err := ws.Ping(pctx)
		cancel()
		if err != nil {
			ws.CloseNow()
			return
		}
	}
}

// A handler answers one request with every message for its id.
type handler func(ctx context.Context, params json.RawMessage, o *op)

func (s *Server) methods() map[string]handler {
	m := map[string]handler{}
	if e := s.Endpoint; e != nil {
		m["info"] = callHandler(func(ctx context.Context, _ struct{}) (api.Environment, error) { return e.Info(ctx) })
		m["read"] = callHandler(e.Read)
		m["submit"] = callHandler(e.Submit)
		m["subscribe"] = streamHandler(func(ctx context.Context, sub api.Subscription) (source[api.StreamItem], error) {
			return e.Open(ctx, sub)
		})
	}
	if h := s.Hub; h != nil {
		m["hub.append"] = callHandler(h.Append)
		m["hub.acked"] = callHandler(func(ctx context.Context, q ackedQuery) (api.UploadAck, error) {
			n, err := h.Acked(ctx, q.Log)
			return api.UploadAck{Acked: n}, err
		})
		m["hub.events"] = callHandler(func(ctx context.Context, q eventsQuery) (eventsPage, error) {
			if q.Limit <= 0 || q.Limit > maxPage {
				q.Limit = maxPage
			}
			evs, err := h.Events(ctx, q.Thread, q.After, q.Limit)
			return eventsPage{Events: evs}, err
		})
		m["hub.follow"] = streamHandler(func(ctx context.Context, q eventsQuery) (source[api.Event], error) {
			return h.Follow(ctx, q.Thread, q.After)
		})
	}
	return m
}

const maxPage = 1000

type ackedQuery struct {
	Log api.ID `json:"log"`
}

// eventsQuery reads one thread when Thread is set, otherwise every thread.
type eventsQuery struct {
	Thread api.ID     `json:"thread,omitzero"`
	After  api.Cursor `json:"after,omitzero"`
	Limit  int        `json:"limit,omitempty"`
}

type eventsPage struct {
	Events []api.Event `json:"events"`
}

func callHandler[In, Out any](fn func(context.Context, In) (Out, error)) handler {
	return func(ctx context.Context, params json.RawMessage, o *op) {
		var in In
		if err := json.Unmarshal(params, &in); err != nil {
			o.send(response{Error: &wireError{Code: "bad_request", Message: err.Error()}})
			return
		}
		out, err := fn(ctx, in)
		if err != nil {
			o.send(response{Error: toWire(err)})
			return
		}
		b, err := json.Marshal(out)
		if err != nil {
			o.send(response{Error: toWire(err)})
			return
		}
		o.send(response{Result: b})
	}
}

type source[T any] interface {
	Next(ctx context.Context) (T, error)
}

func streamHandler[In, T any](open func(context.Context, In) (source[T], error)) handler {
	return func(ctx context.Context, params json.RawMessage, o *op) {
		var in In
		if err := json.Unmarshal(params, &in); err != nil {
			o.send(response{Error: &wireError{Code: "bad_request", Message: err.Error()}})
			return
		}
		src, err := open(ctx, in)
		if err != nil {
			o.send(response{Error: toWire(err)})
			return
		}
		if c, ok := src.(io.Closer); ok {
			defer c.Close()
		}
		o.send(response{Open: true})
		for {
			if o.room(ctx) != nil {
				return
			}
			item, err := src.Next(ctx)
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				o.send(response{Error: toWire(err)})
				return
			}
			b, err := json.Marshal(item)
			if err != nil {
				o.send(response{Error: toWire(err)})
				return
			}
			o.unacked.Add(int64(len(b)))
			o.send(response{Item: b})
		}
	}
}
