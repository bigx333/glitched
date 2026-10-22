package transport

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"glitched/internal/api"
)

// impostor answers WebSocket upgrades on loopback as a server that doesn't
// hold the client's key would: it sends a nonce, reads the client's answer
// and sends a proof made with a key of its own.
type impostor struct {
	addr    string
	serving sync.WaitGroup

	mu      sync.Mutex
	raw     []*bytes.Buffer // what each client sent, as it crossed the wire
	answers []hello
}

func newImpostor(t *testing.T) *impostor {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	imp := &impostor{addr: ln.Addr().String()}
	hs := &http.Server{Handler: http.HandlerFunc(imp.serve)}
	go hs.Serve(&recording{Listener: ln, imp: imp})
	t.Cleanup(func() { hs.Close() })
	return imp
}

func (imp *impostor) serve(w http.ResponseWriter, r *http.Request) {
	imp.serving.Add(1)
	defer imp.serving.Done()
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer ws.CloseNow()
	ctx := context.Background()
	nonce := make([]byte, nonceSize)
	rand.Read(nonce)
	b, _ := json.Marshal(hello{Nonce: nonce})
	ws.Write(ctx, websocket.MessageText, b)
	_, b, err = ws.Read(ctx)
	if err != nil {
		return
	}
	var h hello
	json.Unmarshal(b, &h)
	imp.mu.Lock()
	imp.answers = append(imp.answers, h)
	imp.mu.Unlock()
	forged := make([]byte, sha256.Size)
	rand.Read(forged)
	b, _ = json.Marshal(hello{MAC: forged})
	ws.Write(ctx, websocket.MessageText, b)
	for {
		if _, _, err := ws.Read(ctx); err != nil {
			return
		}
	}
}

// sent is what the clients sent once they hung up: each one's HTTP request
// and the payload of each WebSocket frame, unmasked.
func (imp *impostor) sent() [][]byte {
	imp.serving.Wait()
	imp.mu.Lock()
	defer imp.mu.Unlock()
	var out [][]byte
	for _, b := range imp.raw {
		out = append(out, unmasked(b.Bytes()))
	}
	return out
}

type recording struct {
	net.Listener
	imp *impostor
}

func (l *recording) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	b := &bytes.Buffer{}
	l.imp.mu.Lock()
	l.imp.raw = append(l.imp.raw, b)
	l.imp.mu.Unlock()
	return &recorded{Conn: c, imp: l.imp, b: b}, nil
}

type recorded struct {
	net.Conn
	imp *impostor
	b   *bytes.Buffer
}

func (c *recorded) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.imp.mu.Lock()
	c.b.Write(p[:n])
	c.imp.mu.Unlock()
	return n, err
}

// unmasked is raw, a client's side of a WebSocket connection, with each
// frame's payload unmasked and its header dropped.
func unmasked(raw []byte) []byte {
	head, rest, _ := bytes.Cut(raw, []byte("\r\n\r\n"))
	out := append(head, "\r\n\r\n"...)
	for len(rest) >= 2 {
		n, masked := uint64(rest[1]&0x7f), rest[1]&0x80 != 0
		rest = rest[2:]
		switch {
		case n == 126 && len(rest) >= 2:
			n, rest = uint64(binary.BigEndian.Uint16(rest)), rest[2:]
		case n == 127 && len(rest) >= 8:
			n, rest = binary.BigEndian.Uint64(rest), rest[8:]
		}
		var key [4]byte
		if masked && len(rest) >= 4 {
			copy(key[:], rest)
			rest = rest[4:]
		}
		n = min(n, uint64(len(rest)))
		for i := range n {
			out = append(out, rest[i]^key[i%4])
		}
		rest = rest[n:]
	}
	return out
}

// secrets are the forms of token, and of its key, that would let whoever
// read one connect as the client.
func secrets(token string) map[string][]byte {
	k := KeyOf(token)
	return map[string][]byte{
		"the token":                    []byte(token),
		"its key":                      k[:],
		"its key in hex":               []byte(hex.EncodeToString(k[:])),
		"its key in base64":            []byte(base64.StdEncoding.EncodeToString(k[:])),
		"its key in unpadded base64":   []byte(base64.RawStdEncoding.EncodeToString(k[:])),
		"its key in URL-safe base64":   []byte(base64.RawURLEncoding.EncodeToString(k[:])),
		"the token in base64":          []byte(base64.StdEncoding.EncodeToString([]byte(token))),
		"the token in unpadded base64": []byte(base64.RawStdEncoding.EncodeToString([]byte(token))),
	}
}

func checkSent(t *testing.T, sent [][]byte, token string) {
	t.Helper()
	if len(sent) == 0 {
		t.Fatal("the client never connected")
	}
	for _, b := range sent {
		for name, s := range secrets(token) {
			if bytes.Contains(b, s) {
				t.Errorf("the client sent %s to a server that doesn't hold it:\n%q", name, b)
			}
		}
		if bytes.Contains(b, []byte(`"method"`)) {
			t.Errorf("the client sent a call to a server that didn't prove it holds the key:\n%q", b)
		}
	}
}

const longToken = "Zm9vYmFyYmF6cXV4cXV1eGNvcmdlZ3JhdWx0Z2FycGx5"

func TestClientSendsNeitherTheTokenNorItsKey(t *testing.T) {
	imp := newImpostor(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for name, tr := range map[string]Transport{"Tailnet": Tailnet{Address: imp.addr, Token: longToken}, "*Tailnet": &Tailnet{Address: imp.addr, Token: longToken}} {
		c := NewEndpointClient(tr, api.ID{})
		_, err := c.Info(ctx)
		c.Close()
		if !errors.Is(err, ErrNotTheServer) {
			t.Errorf("%s against a server that doesn't hold the key: %v, want ErrNotTheServer", name, err)
		}
	}
	checkSent(t, imp.sent(), longToken)
}

func TestTailnetSendsNoReusableSecretWhenTheRouteCheckIsWrong(t *testing.T) {
	// On Linux a socket bound to Tailscale's address can still leave by
	// another interface when the route says so: the route check passes, and
	// the connection reaches a plain listener outside WireGuard.
	interfaces(t, "127.0.0.1 ::1", "100.64.0.9 fd7a:115c:a1e0::9", "192.168.1.5")
	imp := newImpostor(t)
	at := func(s string) net.Addr { return net.TCPAddrFromAddrPort(netip.MustParseAddrPort(s)) }
	old := dialNet
	t.Cleanup(func() { dialNet = old })
	dialNet = func(ctx context.Context, network, _ string) (net.Conn, error) {
		var c net.Conn
		if network == "udp" {
			near, far := net.Pipe()
			far.Close()
			c = near
		} else {
			var err error
			if c, err = new(net.Dialer).DialContext(ctx, "tcp", imp.addr); err != nil {
				return nil, err
			}
		}
		return routedConn{c, at("100.64.0.9:40000"), at("100.101.2.3:7465")}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c := NewEndpointClient(Tailnet{Address: "100.101.2.3:7465", Token: longToken}, api.ID{})
	_, err := c.Info(ctx)
	c.Close()
	if !errors.Is(err, ErrNotTheServer) {
		t.Fatalf("a dial that reached a plain listener: %v, want ErrNotTheServer", err)
	}
	checkSent(t, imp.sent(), longToken)

	// What the listener did get answers its own nonce, and no other.
	dialNet = old
	srv := &Server{Transport: Tailnet{}, Endpoint: &fakeEndpoint{}, Authorize: admit(map[string]context.Context{longToken: context.Background()})}
	addr := serveTailnet(t, srv)
	imp.mu.Lock()
	answer := imp.answers[0]
	imp.mu.Unlock()
	ws := upgrade(t, ctx, addr)
	serverNonce(t, ctx, ws)
	writeJSON(t, ctx, ws, answer)
	if _, _, err := ws.Read(ctx); websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatalf("the engine took the client's answer to another server's nonce: %v, want it refused", err)
	}
}

// admit is an Authorize that admits each token in grants until its ctx ends.
func admit(grants map[string]context.Context) func(*http.Request, func(Key) bool) (Key, context.Context, error) {
	return func(_ *http.Request, proved func(Key) bool) (Key, context.Context, error) {
		for token, grant := range grants {
			if k := KeyOf(token); proved(k) {
				return k, grant, nil
			}
		}
		return Key{}, nil, errors.New("unknown token")
	}
}

// serveTailnet serves srv on a loopback port until the test ends.
func serveTailnet(t *testing.T, srv *Server) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ln, err := Tailnet{Address: "127.0.0.1:0"}.Listen(ctx)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error)
	go func() { done <- srv.Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return ln.Addr().String()
}

// upgrade opens a WebSocket to addr that has done none of the handshake.
func upgrade(t *testing.T, ctx context.Context, addr string) *websocket.Conn {
	t.Helper()
	ws, _, err := websocket.Dial(ctx, "ws://"+addr+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ws.CloseNow() })
	return ws
}

func serverNonce(t *testing.T, ctx context.Context, ws *websocket.Conn) hello {
	t.Helper()
	_, b, err := ws.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var h hello
	if err := json.Unmarshal(b, &h); err != nil || len(h.Nonce) != nonceSize {
		t.Fatalf("the server's first message %s: %v, want a nonce", b, err)
	}
	return h
}

func writeJSON(t *testing.T, ctx context.Context, ws *websocket.Conn, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if err := ws.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatal(err)
	}
}

func TestHandshakeClosesAPeerThatStalls(t *testing.T) {
	srv := &Server{Transport: Tailnet{}, Endpoint: &fakeEndpoint{}, handshake: 200 * time.Millisecond, Authorize: admit(nil)}
	addr := serveTailnet(t, srv)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws := upgrade(t, ctx, addr)
	serverNonce(t, ctx, ws)
	begin := time.Now()
	if _, _, err := ws.Read(ctx); err == nil || ctx.Err() != nil {
		t.Fatalf("a peer that never answered the nonce: %v, want the socket closed", err)
	}
	if took := time.Since(begin); took > 2*time.Second {
		t.Fatalf("a peer that never answered the nonce held the socket %v, want about 200ms", took)
	}
}

func TestHandshakeAnswersNoCallBeforeTheProof(t *testing.T) {
	srv := &Server{Transport: Tailnet{}, Endpoint: &fakeEndpoint{}, Authorize: admit(map[string]context.Context{"good": context.Background()})}
	addr := serveTailnet(t, srv)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for name, msg := range map[string]any{
		"a call":                request{ID: 1, Method: "info", Params: json.RawMessage(`{}`)},
		"a proof with no nonce": hello{MAC: make([]byte, sha256.Size)},
		"a message over 1 KiB":  hello{Nonce: make([]byte, nonceSize), MAC: []byte(strings.Repeat("x", 2048))},
		"a short nonce":         hello{Nonce: make([]byte, 8), MAC: make([]byte, sha256.Size)},
	} {
		ws := upgrade(t, ctx, addr)
		serverNonce(t, ctx, ws)
		writeJSON(t, ctx, ws, msg)
		if _, b, err := ws.Read(ctx); err == nil {
			t.Errorf("%s instead of a proof was answered: %s", name, b)
		}
	}
}

func TestHandshakeDropsABadProofWithoutWaitingForThePeer(t *testing.T) {
	srv := &Server{Transport: Tailnet{}, Endpoint: &fakeEndpoint{}, Authorize: admit(map[string]context.Context{"good": context.Background()})}
	addr := serveTailnet(t, srv)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var nc net.Conn
	ws, _, err := websocket.Dial(ctx, "ws://"+addr+"/ws", &websocket.DialOptions{HTTPClient: &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			c, err := new(net.Dialer).DialContext(ctx, network, addr)
			nc = c
			return c, err
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	serverNonce(t, ctx, ws)
	writeJSON(t, ctx, ws, hello{Nonce: make([]byte, nonceSize), MAC: make([]byte, sha256.Size)})
	// Read the raw socket, so nothing answers the server's close frame.
	nc.SetReadDeadline(time.Now().Add(2 * time.Second))
	got, err := io.ReadAll(nc)
	if err != nil {
		t.Fatalf("a peer that sent a bad proof and never answered the close: %v, want the server to drop it at once", err)
	}
	if want := []byte{0x88, 9, 0x03, 0xf0}; !bytes.HasPrefix(got, want) {
		t.Fatalf("the server's last bytes %x, want a close frame with 1008 first", got)
	}
}
