package transport

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"glitched/internal/api"
)

// slowLink carries a client's writes at rate bytes a second.
type slowLink struct {
	Loopback
	rate int
}

func (l slowLink) Dial(ctx context.Context, m api.ID) (net.Conn, error) {
	c, err := l.Loopback.Dial(ctx, m)
	if err != nil {
		return nil, err
	}
	return slowConn{c, l.rate}, nil
}

type slowConn struct {
	net.Conn
	rate int
}

func (s slowConn) Write(p []byte) (int, error) {
	time.Sleep(time.Duration(len(p)) * time.Second / time.Duration(s.rate))
	return s.Conn.Write(p)
}

func TestLongWriteLetsPongsThrough(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	r := openReplica(t)
	// A message that takes a second to write, to a server that drops the
	// socket when a pong is 300ms late.
	srv := &Server{Hub: r, keepalive: keepalive{every: 20 * time.Millisecond, within: 300 * time.Millisecond}}
	c := NewHubClient(slowLink{start(t, "unix", srv), 512 << 10}, api.ID{})
	log := api.NewID()
	evs := events(log, api.NewID(), 1, 1)
	evs[0].Payload = json.RawMessage(`"` + strings.Repeat("x", 512<<10) + `"`)
	if ack, err := c.Append(ctx, api.Upload{Protocol: api.Protocol, Host: api.NewID(), Epoch: 1, Log: log, Events: evs}); err != nil || ack.Acked != 1 {
		t.Fatalf("append over a slow link: %+v %v", ack, err)
	}
}

func TestOversizedRequestLeavesTheSocketAlone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	r := openReplica(t)
	d := &dialer{Loopback: start(t, "unix", &Server{Hub: r})}
	c := NewHubClient(d, api.ID{})
	thread := api.NewID()
	feed, err := c.Follow(ctx, thread, api.Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	defer feed.Close()

	log := api.NewID()
	evs := events(log, thread, 1, 1)
	evs[0].Payload = json.RawMessage(`"` + strings.Repeat("x", maxMessage) + `"`)
	if _, err := c.Append(ctx, api.Upload{Protocol: api.Protocol, Host: api.NewID(), Epoch: 1, Log: log, Events: evs}); !errors.Is(err, errTooLarge) {
		t.Fatalf("an append over the server's limit: %v", err)
	}
	if ack, err := c.Append(ctx, api.Upload{Protocol: api.Protocol, Host: api.NewID(), Epoch: 1, Log: log, Events: events(log, thread, 1, 1)}); err != nil || ack.Acked != 1 {
		t.Fatalf("an append after the refused one: %+v %v", ack, err)
	}
	if e, err := feed.Next(ctx); err != nil || e.Cursor.Seq != 1 {
		t.Fatalf("the stream beside a refused append: %+v %v", e.Cursor, err)
	}
	if n := len(d.dialed()); n != 1 {
		t.Fatalf("dialed %d sockets, want 1", n)
	}
}

func TestKeepaliveHoldsAnIdleSocket(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	fast := keepalive{every: 10 * time.Millisecond, within: 200 * time.Millisecond}
	fake := &fakeEndpoint{env: api.Environment{ID: api.NewID()}}
	d := &dialer{Loopback: start(t, "tcp", &Server{Endpoint: fake, keepalive: fast})}
	c := NewEndpointClient(d, api.ID{})
	c.c.keepalive = fast
	thread := api.NewID()
	st := mustOpen(t, ctx, c, thread)
	if err := readStream(ctx, st, thread, api.Cursor{}); err != nil {
		t.Fatal(err)
	}

	idle, stop := context.WithTimeout(ctx, 400*time.Millisecond)
	defer stop()
	if _, err := st.Next(idle); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a stream idle for 40 pings: %v, want it still open", err)
	}
	if env, err := c.Info(ctx); err != nil || env != fake.env {
		t.Fatalf("info: %+v %v", env, err)
	}
	if n := len(d.dialed()); n != 1 {
		t.Fatalf("dialed %d sockets, want 1", n)
	}
}

func TestServerReadLimit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	lb := start(t, "unix", &Server{Endpoint: &fakeEndpoint{}})
	// Zeros are not JSON, so a message the server reads closes the socket
	// as malformed.
	for size, want := range map[int]websocket.StatusCode{
		maxMessage:     websocket.StatusInvalidFramePayloadData,
		maxMessage + 1: websocket.StatusMessageTooBig,
	} {
		ws, err := handshake(ctx, lb, api.ID{})
		if err != nil {
			t.Fatal(err)
		}
		go ws.Write(ctx, websocket.MessageText, make([]byte, size))
		_, _, err = ws.Read(ctx)
		ws.CloseNow()
		if got := websocket.CloseStatus(err); got != want {
			t.Errorf("a %d byte message: %v, want close status %v", size, err, want)
		}
	}
}
