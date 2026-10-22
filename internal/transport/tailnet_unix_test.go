//go:build unix

package transport

import (
	"context"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// counted counts the connections it accepted that are still open. Their
// send buffers are small, so a peer that doesn't read blocks the server's
// writes at once.
type counted struct {
	net.Listener
	open atomic.Int64
}

func (l *counted) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	c.(*net.TCPConn).SetWriteBuffer(1024)
	l.open.Add(1)
	return &countedConn{Conn: c, ln: l}, nil
}

type countedConn struct {
	net.Conn
	ln     *counted
	closed sync.Once
}

func (c *countedConn) Close() error {
	c.closed.Do(func() { c.ln.open.Add(-1) })
	return c.Conn.Close()
}

func TestServerClosesAConnectionThatNeverReadsItsResponses(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv := &Server{Transport: Tailnet{}, Endpoint: &fakeEndpoint{}, idle: 100 * time.Millisecond, Authorize: admit(nil)}
	inner, err := Tailnet{Address: "127.0.0.1:0"}.Listen(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ln := &counted{Listener: inner}
	done := make(chan error)
	go func() { done <- srv.Serve(ctx, ln) }()
	defer func() { cancel(); <-done }()
	d := net.Dialer{Control: func(_, _ string, c syscall.RawConn) error {
		return c.Control(func(fd uintptr) { syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, 1024) })
	}}
	nc, err := d.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	go nc.Write([]byte(strings.Repeat("GET /ws HTTP/1.1\r\nHost: glitched\r\n\r\n", 200000)))
	deadline := time.Now().Add(3 * time.Second)
	for ln.open.Load() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	for ln.open.Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("a peer that never reads the responses to its refused requests held its connection for 3s, 30 times the timeout")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
