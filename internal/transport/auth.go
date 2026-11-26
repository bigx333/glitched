package transport

import (
	"cmp"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// A Key is what a client and the engine that paired it share: the SHA-256
// of the pairing token. Neither side ever sends it, or the token.
type Key [sha256.Size]byte

func KeyOf(token string) Key { return sha256.Sum256([]byte(token)) }

// ErrRefused is a call's error when the server closed the handshake with
// 1008, policy violation, instead of proving it holds the client's key. A
// server that holds no key for the token sends it, and so can a server that
// isn't the paired engine, so it doesn't mean the token is wrong.
var ErrRefused = errors.New("transport: the server refused the handshake: it holds no key for this token, or it isn't the paired engine")

// ErrNotTheServer is a call's error when the server failed to prove it holds
// the client's key, so whatever answered may not be the paired engine. The
// client sent it no call.
var ErrNotTheServer = errors.New("transport: the server didn't prove it holds the token, so the address may not be the paired engine")

// hello is one handshake message (wire.go).
type hello struct {
	Nonce []byte `json:"nonce,omitempty"`
	MAC   []byte `json:"mac,omitempty"`
}

const (
	nonceSize       = 32
	helloLimit      = 1 << 10
	handshakeWithin = 5 * time.Second
	clientProof     = "glitched-client\x00"
	serverProof     = "glitched-server\x00"
)

func mac(k Key, label string, first, second []byte) []byte {
	h := hmac.New(sha256.New, k[:])
	h.Write([]byte(label))
	h.Write(first)
	h.Write(second)
	return h.Sum(nil)
}

func nonce() []byte {
	b := make([]byte, nonceSize)
	rand.Read(b)
	return b
}

func writeHello(ctx context.Context, ws *websocket.Conn, h hello) error {
	b, _ := json.Marshal(h)
	return ws.Write(ctx, websocket.MessageText, b)
}

var errBadHello = errors.New("a handshake message that isn't one")

func readHello(ctx context.Context, ws *websocket.Conn) (hello, error) {
	_, b, err := ws.Read(ctx)
	if err != nil {
		return hello{}, err
	}
	var h hello
	if json.Unmarshal(b, &h) != nil {
		return hello{}, errBadHello
	}
	return h, nil
}

// challenge has the client prove it holds a key Authorize admits, then
// proves the server holds it too. It returns the key's grant.
func (s *Server) challenge(ctx context.Context, ws *websocket.Conn, r *http.Request) (Grant, error) {
	ctx, cancel := context.WithTimeout(ctx, cmp.Or(s.handshake, handshakeWithin))
	defer cancel()
	nonceS := nonce()
	if err := writeHello(ctx, ws, hello{Nonce: nonceS}); err != nil {
		return Grant{}, err
	}
	h, err := readHello(ctx, ws)
	if err != nil {
		return Grant{}, err
	}
	if len(h.Nonce) != nonceSize || len(h.MAC) != sha256.Size {
		return Grant{}, errBadHello
	}
	grant, err := s.Authorize(r, func(k Key) bool {
		return hmac.Equal(h.MAC, mac(k, clientProof, nonceS, h.Nonce))
	})
	if err != nil {
		return Grant{}, err
	}
	if err := writeHello(ctx, ws, hello{MAC: mac(grant.Key, serverProof, h.Nonce, nonceS)}); err != nil {
		return Grant{}, err
	}
	return grant, nil
}

// prove answers the server's challenge with k and checks the server's own
// proof, before the client sends any call.
func prove(ctx context.Context, ws *websocket.Conn, k Key) error {
	ctx, cancel := context.WithTimeout(ctx, handshakeWithin)
	defer cancel()
	ws.SetReadLimit(helloLimit)
	h, err := readHello(ctx, ws)
	if err != nil {
		return refusal(err)
	}
	if len(h.Nonce) != nonceSize {
		return fmt.Errorf("%w: its first message held no nonce", ErrNotTheServer)
	}
	nonceS, nonceC := h.Nonce, nonce()
	if err := writeHello(ctx, ws, hello{Nonce: nonceC, MAC: mac(k, clientProof, nonceS, nonceC)}); err != nil {
		return refusal(err)
	}
	if h, err = readHello(ctx, ws); err != nil {
		return refusal(err)
	}
	if !hmac.Equal(h.MAC, mac(k, serverProof, nonceC, nonceS)) {
		return ErrNotTheServer
	}
	return nil
}

// refusal is why a handshake that failed on err failed.
func refusal(err error) error {
	switch {
	case websocket.CloseStatus(err) == websocket.StatusPolicyViolation:
		return ErrRefused
	case errors.Is(err, errBadHello):
		return fmt.Errorf("%w: %w", ErrNotTheServer, err)
	}
	return fmt.Errorf("transport: handshake: %w", err)
}

const (
	defaultMaxHandshakes  = 64
	defaultAuthBurst      = 8
	defaultAuthBackoff    = 500 * time.Millisecond
	defaultMaxAuthBackoff = 30 * time.Second
	maxTrackedAuthPeers   = 1024
)

type peerAuth struct {
	failures     int
	lastFailure  time.Time
	blockedUntil time.Time
}

type handshakeLimiter struct {
	sem         chan struct{}
	burst       int
	baseBackoff time.Duration
	maxBackoff  time.Duration

	mu    sync.Mutex
	peers map[string]*peerAuth
}

func (s *Server) newHandshakeLimiter() *handshakeLimiter {
	maxConcurrent := s.maxHandshakes
	if maxConcurrent <= 0 {
		maxConcurrent = defaultMaxHandshakes
	}
	burst := s.authBurst
	if burst <= 0 {
		burst = defaultAuthBurst
	}
	backoff := s.authBackoff
	if backoff <= 0 {
		backoff = defaultAuthBackoff
	}
	maxBackoff := s.maxAuthBackoff
	if maxBackoff <= 0 {
		maxBackoff = defaultMaxAuthBackoff
	}
	if maxBackoff < backoff {
		maxBackoff = backoff
	}
	return &handshakeLimiter{
		sem:         make(chan struct{}, maxConcurrent),
		burst:       burst,
		baseBackoff: backoff,
		maxBackoff:  maxBackoff,
		peers:       map[string]*peerAuth{},
	}
}

func peerKey(remoteAddr string) string {
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil && host != "" {
		return host
	}
	return remoteAddr
}

func (l *handshakeLimiter) acquire() bool {
	select {
	case l.sem <- struct{}{}:
		return true
	default:
		return false
	}
}

func (l *handshakeLimiter) release() {
	<-l.sem
}

func (l *handshakeLimiter) checkPeer(remoteAddr string, now time.Time) (time.Duration, bool) {
	key := peerKey(remoteAddr)
	l.mu.Lock()
	defer l.mu.Unlock()
	p := l.peers[key]
	if p == nil || !now.Before(p.blockedUntil) {
		return 0, true
	}
	return p.blockedUntil.Sub(now), false
}

func (l *handshakeLimiter) recordSuccess(remoteAddr string) {
	key := peerKey(remoteAddr)
	l.mu.Lock()
	delete(l.peers, key)
	l.mu.Unlock()
}

func (l *handshakeLimiter) recordFailure(remoteAddr string, now time.Time) {
	key := peerKey(remoteAddr)
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.peers) > maxTrackedAuthPeers {
		for k, v := range l.peers {
			if now.Sub(v.lastFailure) > 2*l.maxBackoff && !now.Before(v.blockedUntil) {
				delete(l.peers, k)
			}
		}
	}
	p := l.peers[key]
	if p == nil {
		p = &peerAuth{}
		l.peers[key] = p
	} else if now.Sub(p.lastFailure) > 2*l.maxBackoff {
		p.failures = 0
	}
	p.failures++
	p.lastFailure = now
	if p.failures >= l.burst {
		shift := min(p.failures-l.burst, 10)
		delay := min(l.baseBackoff<<shift, l.maxBackoff)
		p.blockedUntil = now.Add(delay)
	}
}
