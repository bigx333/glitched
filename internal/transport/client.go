package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"

	"glitched/internal/api"
	"glitched/internal/hub"
)

type conn struct{ hc *http.Client }

func dial(t Transport, machine api.ID) conn {
	return conn{hc: &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return t.Dial(ctx, machine) },
	}}}
}

// The host part is never resolved: Transport.Dial picks the connection.
const origin = "http://glitched"

func (c conn) post(ctx context.Context, path string, in any) (*http.Response, error) {
	body, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, origin+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, readError(resp)
	}
	return resp, nil
}

func (c conn) call(ctx context.Context, path string, in, out any) error {
	resp, err := c.post(ctx, path, in)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(out)
}

// stream reads NDJSON frames on its own goroutine, so a Next whose ctx ends
// leaves the stream usable.
type stream[T any] struct {
	frames chan frame[T]
	err    error // why frames closed; read only after it has
	cancel context.CancelFunc
}

// open keeps the stream alive past ctx, which bounds only the wait for the
// response header. Close ends it.
func open[T any](ctx context.Context, c conn, path string, in any) (*stream[T], error) {
	streamCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stop := context.AfterFunc(ctx, cancel)
	resp, err := c.post(streamCtx, path, in)
	stop()
	if err != nil {
		cancel()
		return nil, err
	}
	s := &stream[T]{frames: make(chan frame[T]), cancel: cancel}
	go func() {
		defer close(s.frames)
		defer resp.Body.Close()
		dec := json.NewDecoder(resp.Body)
		for {
			var f frame[T]
			if s.err = dec.Decode(&f); s.err != nil {
				return
			}
			select {
			case s.frames <- f:
			case <-streamCtx.Done():
				s.err = streamCtx.Err()
				return
			}
		}
	}()
	return s, nil
}

func (s *stream[T]) Next(ctx context.Context) (T, error) {
	var zero T
	select {
	case f, ok := <-s.frames:
		switch {
		case !ok:
			return zero, s.err
		case f.Error != nil:
			return zero, f.Error.err()
		case f.Item == nil:
			return zero, errors.New("transport: stream frame without an item")
		}
		return *f.Item, nil
	case <-ctx.Done():
		return zero, ctx.Err()
	}
}

func (s *stream[T]) Close() error {
	s.cancel()
	return nil
}

// EndpointClient is an api.Endpoint on another node.
type EndpointClient struct{ c conn }

var _ api.Endpoint = (*EndpointClient)(nil)

func NewEndpointClient(t Transport, machine api.ID) *EndpointClient {
	return &EndpointClient{dial(t, machine)}
}

func (e *EndpointClient) Info(ctx context.Context) (api.Environment, error) {
	var env api.Environment
	return env, e.c.call(ctx, "/v1/info", struct{}{}, &env)
}

func (e *EndpointClient) Read(ctx context.Context, q api.Query) (api.Snapshot, error) {
	var snap api.Snapshot
	return snap, e.c.call(ctx, "/v1/read", q, &snap)
}

func (e *EndpointClient) Submit(ctx context.Context, cmd api.Command) (api.Receipt, error) {
	var r api.Receipt
	return r, e.c.call(ctx, "/v1/submit", cmd, &r)
}

func (e *EndpointClient) Open(ctx context.Context, s api.Subscription) (api.Stream, error) {
	return open[api.StreamItem](ctx, e.c, "/v1/stream", s)
}

// HubClient reaches the hub replica on another node. It is the uploader's
// hub.Client.
type HubClient struct{ c conn }

var _ hub.Client = (*HubClient)(nil)

func NewHubClient(t Transport, machine api.ID) *HubClient {
	return &HubClient{dial(t, machine)}
}

func (h *HubClient) Append(ctx context.Context, u api.Upload) (api.UploadAck, error) {
	var ack api.UploadAck
	return ack, h.c.call(ctx, "/hub/v1/append", u, &ack)
}

func (h *HubClient) Acked(ctx context.Context, log api.ID) (uint64, error) {
	var ack api.UploadAck
	err := h.c.call(ctx, "/hub/v1/acked", ackedQuery{Log: log}, &ack)
	return ack.Acked, err
}

// Events reads one page of the replica; see hub.Replica.Events.
func (h *HubClient) Events(ctx context.Context, thread api.ID, after api.Cursor, limit int) ([]api.Event, error) {
	var page eventsPage
	err := h.c.call(ctx, "/hub/v1/events", eventsQuery{Thread: thread, After: after, Limit: limit}, &page)
	return page.Events, err
}

// EventStream is a live read of the replica.
type EventStream interface {
	Next(ctx context.Context) (api.Event, error)
	Close() error
}

// Follow is hub.Replica.Follow across the wire.
func (h *HubClient) Follow(ctx context.Context, thread api.ID, after api.Cursor) (EventStream, error) {
	return open[api.Event](ctx, h.c, "/hub/v1/stream", eventsQuery{Thread: thread, After: after})
}
