package transport

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"glitched/internal/api"
)

// A client of another protocol, or one that announces none, as a protocol
// 1 client doesn't, is refused at the upgrade: the socket closes with 4001
// and a reason an older client shows, and no call is answered.
func TestServerRefusesAClientOfAnotherProtocol(t *testing.T) {
	lb := start(t, "unix", &Server{Endpoint: &fakeEndpoint{}, Hub: openReplica(t)})
	next := strconv.Itoa(api.Protocol + 1)
	for announce, theirs := range map[string]string{"": "1", "1": "1", next: next} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		nc, err := lb.Dial(ctx, api.ID{})
		if err != nil {
			t.Fatal(err)
		}
		hc := &http.Client{Transport: &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) { return nc, nil }}}
		h := http.Header{}
		if announce != "" {
			h.Set(protocolHeader, announce)
		}
		ws, _, err := websocket.Dial(ctx, origin, &websocket.DialOptions{HTTPClient: hc, HTTPHeader: h})
		if err != nil {
			t.Fatal(err)
		}
		ws.Write(ctx, websocket.MessageText, []byte(`{"id":1,"method":"info"}`))
		_, b, err := ws.Read(ctx)
		if websocket.CloseStatus(err) != statusProtocol || !strings.Contains(err.Error(), fmt.Sprintf("this engine speaks protocol %d and the client %s", api.Protocol, theirs)) {
			t.Errorf("a client announcing %q: read %s, %v; want the socket closed as of protocol %s", announce, b, err, theirs)
		}
		ws.CloseNow()
	}
}

// otherProtocol serves WebSocket upgrades announcing a protocol, none when
// empty, and counts the messages its clients send. refuse closes each
// socket at once with 4001, as a Server of that protocol does.
func otherProtocol(t *testing.T, announce string, refuse bool) (Loopback, *atomic.Int64) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var got atomic.Int64
	hs := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if announce != "" {
			w.Header().Set(protocolHeader, announce)
		}
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		if refuse {
			ws.Close(statusProtocol, "this engine speaks protocol "+announce)
			return
		}
		for {
			if _, _, err := ws.Read(context.Background()); err != nil {
				return
			}
			got.Add(1)
		}
	})}
	go hs.Serve(ln)
	t.Cleanup(func() { hs.Close() })
	return Loopback{Network: "tcp", Address: ln.Addr().String()}, &got
}

// The client refuses an engine or a hub of another protocol before it
// sends a call: an engine's Info, a hub's Events and Follow, as glitched
// replica reads them, and the uploader's Append. On a Tailnet the server
// refuses first, before the key's proof.
func TestClientRefusesAServerOfAnotherProtocol(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	next := strconv.Itoa(api.Protocol + 1)
	for _, tc := range []struct {
		announce string
		tailnet  bool
		theirs   int
	}{{"", false, 1}, {next, false, api.Protocol + 1}, {next, true, api.Protocol + 1}} {
		announce, theirs := tc.announce, tc.theirs
		lb, got := otherProtocol(t, announce, tc.tailnet)
		var tr Transport = lb
		if tc.tailnet {
			tr = Tailnet{Address: lb.Address, Token: "token"}
		}
		ep, hc := NewEndpointClient(tr, api.ID{}), NewHubClient(tr, api.ID{})
		defer ep.Close()
		defer hc.Close()
		calls := map[string]error{}
		_, calls["info"] = ep.Info(ctx)
		_, calls["hub.events"] = hc.Events(ctx, api.NewID(), api.Cursor{}, 10)
		_, calls["hub.follow"] = hc.Follow(ctx, api.NewID(), api.Cursor{})
		_, calls["hub.append"] = hc.Append(ctx, api.Upload{Protocol: api.Protocol})
		for call, err := range calls {
			var pe *api.ProtocolError
			if !errors.As(err, &pe) || pe.Theirs != theirs || !errors.Is(err, api.ErrProtocol) {
				t.Errorf("%s from a server announcing %q, tailnet %v: %v, want refused as of protocol %d", call, announce, tc.tailnet, err, theirs)
			}
		}
		if n := got.Load(); n != 0 {
			t.Errorf("the client sent %d messages to a server announcing %q", n, announce)
		}
	}
}
