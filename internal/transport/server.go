package transport

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"time"

	"glitched/internal/api"
	"glitched/internal/hub"
)

// Server serves an Endpoint under /v1 and a hub replica under /hub/v1. Either
// may be nil, and its routes are then not served.
type Server struct {
	Transport Transport
	Endpoint  api.Endpoint
	Hub       *hub.Replica
}

type connKey struct{}

// Serve answers requests on ln until ctx ends.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	hs := &http.Server{
		Handler:           s.handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			return context.WithValue(ctx, connKey{}, c)
		},
	}
	stop := context.AfterFunc(ctx, func() { hs.Close() })
	defer stop()
	err := hs.Serve(ln)
	if ctx.Err() != nil {
		return nil
	}
	return err
}

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	if e := s.Endpoint; e != nil {
		mux.HandleFunc("POST /v1/info", rpc(func(ctx context.Context, _ struct{}) (api.Environment, error) {
			return e.Info(ctx)
		}))
		mux.HandleFunc("POST /v1/read", rpc(e.Read))
		mux.HandleFunc("POST /v1/submit", rpc(e.Submit))
		mux.HandleFunc("POST /v1/stream", func(w http.ResponseWriter, r *http.Request) {
			var sub api.Subscription
			if !decode(w, r, &sub) {
				return
			}
			st, err := e.Open(r.Context(), sub)
			if err != nil {
				writeError(w, err)
				return
			}
			defer st.Close()
			pump(w, r, st.Next)
		})
	}
	if h := s.Hub; h != nil {
		mux.HandleFunc("POST /hub/v1/append", rpc(h.Append))
		mux.HandleFunc("POST /hub/v1/acked", rpc(func(ctx context.Context, q ackedQuery) (api.UploadAck, error) {
			n, err := h.Acked(ctx, q.Log)
			return api.UploadAck{Acked: n}, err
		}))
		mux.HandleFunc("POST /hub/v1/events", rpc(func(ctx context.Context, q eventsQuery) (eventsPage, error) {
			if q.Limit <= 0 || q.Limit > maxPage {
				q.Limit = maxPage
			}
			evs, err := h.Events(ctx, q.Thread, q.After, q.Limit)
			return eventsPage{Events: evs}, err
		}))
		mux.HandleFunc("POST /hub/v1/stream", func(w http.ResponseWriter, r *http.Request) {
			var q eventsQuery
			if !decode(w, r, &q) {
				return
			}
			feed, err := h.Follow(r.Context(), q.Thread, q.After)
			if err != nil {
				writeError(w, err)
				return
			}
			pump(w, r, feed.Next)
		})
	}
	return s.authorize(mux)
}

func (s *Server) authorize(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn := r.Context().Value(connKey{}).(net.Conn)
		if _, err := s.Transport.WhoIs(conn); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
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

func rpc[In, Out any](fn func(context.Context, In) (Out, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in In
		if !decode(w, r, &in) {
			return
		}
		out, err := fn(r.Context(), in)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<20)).Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, wireError{Code: "bad_request", Message: err.Error()})
		return false
	}
	return true
}

func pump[T any](w http.ResponseWriter, r *http.Request, next func(context.Context) (T, error)) {
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	// Flush the header so Open returns before the first item exists.
	if rc.Flush() != nil {
		return
	}
	enc := json.NewEncoder(w)
	for {
		item, err := next(r.Context())
		if r.Context().Err() != nil {
			return
		}
		f := frame[T]{Item: &item}
		if err != nil {
			we, _ := toWire(err)
			f = frame[T]{Error: &we}
		}
		if enc.Encode(f) != nil || rc.Flush() != nil || err != nil {
			return
		}
	}
}
