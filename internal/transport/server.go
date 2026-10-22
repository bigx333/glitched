package transport

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"sync"
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
}

type connKey struct{}

// The uploader caps a batch at 8 MiB; this leaves room for JSON overhead.
const maxMessage = 64 << 20

const (
	pingEvery  = 15 * time.Second
	pongWithin = 10 * time.Second
)

// Serve answers sockets on ln until ctx ends, then closes them and returns
// once their calls have.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
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
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer ws.CloseNow()
	ws.SetReadLimit(maxMessage)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go keepAlive(ctx, ws)

	// A failed write closes ws, which ends the read loop below.
	send := func(f response) {
		b, _ := json.Marshal(f)
		ws.Write(ctx, websocket.MessageText, b)
	}
	var (
		mu    sync.Mutex
		live  = map[uint64]context.CancelFunc{}
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
			ws.Close(websocket.StatusInvalidFramePayloadData, "malformed frame")
			return
		}
		mu.Lock()
		if req.Cancel {
			if stop := live[req.ID]; stop != nil {
				stop()
			}
			mu.Unlock()
			continue
		}
		h, known := methods[req.Method]
		_, dup := live[req.ID]
		if !known || dup {
			mu.Unlock()
			send(response{ID: req.ID, Error: &wireError{Code: "bad_request", Message: "unknown method or id in use: " + req.Method}})
			continue
		}
		callCtx, stop := context.WithCancel(ctx)
		live[req.ID] = stop
		mu.Unlock()
		calls.Go(func() {
			defer func() {
				mu.Lock()
				delete(live, req.ID)
				mu.Unlock()
				stop()
			}()
			h(callCtx, req.Params, func(f response) {
				f.ID = req.ID
				send(f)
			})
		})
	}
}

// keepAlive drops ws when the peer stops answering pings.
func keepAlive(ctx context.Context, ws *websocket.Conn) {
	t := time.NewTicker(pingEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		pctx, cancel := context.WithTimeout(ctx, pongWithin)
		err := ws.Ping(pctx)
		cancel()
		if err != nil {
			ws.CloseNow()
			return
		}
	}
}

// A handler answers one request with every frame for its id.
type handler func(ctx context.Context, params json.RawMessage, send func(response))

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
	return func(ctx context.Context, params json.RawMessage, send func(response)) {
		var in In
		if err := json.Unmarshal(params, &in); err != nil {
			send(response{Error: &wireError{Code: "bad_request", Message: err.Error()}})
			return
		}
		out, err := fn(ctx, in)
		if err != nil {
			send(response{Error: toWire(err)})
			return
		}
		b, err := json.Marshal(out)
		if err != nil {
			send(response{Error: toWire(err)})
			return
		}
		send(response{Result: b})
	}
}

type source[T any] interface {
	Next(ctx context.Context) (T, error)
}

func streamHandler[In, T any](open func(context.Context, In) (source[T], error)) handler {
	return func(ctx context.Context, params json.RawMessage, send func(response)) {
		var in In
		if err := json.Unmarshal(params, &in); err != nil {
			send(response{Error: &wireError{Code: "bad_request", Message: err.Error()}})
			return
		}
		src, err := open(ctx, in)
		if err != nil {
			send(response{Error: toWire(err)})
			return
		}
		if c, ok := src.(io.Closer); ok {
			defer c.Close()
		}
		send(response{Open: true})
		for {
			item, err := src.Next(ctx)
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				send(response{Error: toWire(err)})
				return
			}
			b, err := json.Marshal(item)
			if err != nil {
				send(response{Error: toWire(err)})
				return
			}
			send(response{Item: b})
		}
	}
}
