package transport

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"glitched/internal/api"
)

func TestCallsAndStreamsShareOneSocket(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	fake := &fakeEndpoint{env: api.Environment{ID: api.NewID(), Log: api.NewID()}}
	d := &dialer{Loopback: start(t, "unix", &Server{Endpoint: fake})}
	c := NewEndpointClient(d, fake.env.ID)

	a, b := api.NewID(), api.NewID()
	sa, err := c.Open(ctx, api.Subscription{Thread: a, After: api.Cursor{Log: fake.env.Log, Seq: 10}})
	if err != nil {
		t.Fatal(err)
	}
	defer sa.Close()
	sb, err := c.Open(ctx, api.Subscription{Thread: b, After: api.Cursor{Log: fake.env.Log, Seq: 20}})
	if err != nil {
		t.Fatal(err)
	}
	defer sb.Close()

	var wg sync.WaitGroup
	errs := make(chan error, 102)
	for range 100 {
		wg.Go(func() {
			cmd := api.Command{ID: api.NewID(), Target: api.NewID(), Body: api.TurnInterrupt{}}
			r, err := c.Submit(ctx, cmd)
			if err == nil && r.Command != cmd.ID {
				err = fmt.Errorf("submit %v answered for %v", cmd.ID, r.Command)
			}
			errs <- err
		})
	}
	wg.Go(func() { errs <- readStream(ctx, sa, a, api.Cursor{Seq: 10}) })
	wg.Go(func() { errs <- readStream(ctx, sb, b, api.Cursor{Seq: 20}) })
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := len(d.dialed()); n != 1 {
		t.Fatalf("dialed %d sockets, want 1", n)
	}
}

func TestDroppedSocketEndsStreamsAndRedials(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	fake := &fakeEndpoint{env: api.Environment{ID: api.NewID(), Name: "mac", Log: api.NewID()}, reads: make(chan error, 2)}
	d := &dialer{Loopback: start(t, "tcp", &Server{Endpoint: fake})}
	c := NewEndpointClient(d, fake.env.ID)

	thread := api.NewID()
	st := mustOpen(t, ctx, c, thread)
	if it, err := st.Next(ctx); err != nil || it.Kind != api.StreamSnapshot {
		t.Fatalf("first item: %+v %v", it, err)
	}
	blocked := make(chan error)
	go func() {
		_, err := c.Read(ctx, api.Query{Thread: blocks})
		blocked <- err
	}()
	<-fake.reads
	d.dialed()[0].Close()

	for range 4 {
		if _, err := st.Next(ctx); err != nil {
			break
		}
	}
	if _, err := st.Next(ctx); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stream on a dropped socket: got %v, want a connection error", err)
	}
	if err := <-blocked; err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("call on a dropped socket: got %v, want a connection error", err)
	}
	if env, err := c.Info(ctx); err != nil || env != fake.env {
		t.Fatalf("info after the drop: %+v %v", env, err)
	}
	if n := len(d.dialed()); n != 2 {
		t.Fatalf("dialed %d sockets, want 2", n)
	}
}

// gated dials connections whose writes it can hold, as a stalled link would.
type gated struct {
	Loopback
	mu   sync.Mutex
	last *gatedConn
}

func (g *gated) Dial(ctx context.Context, m api.ID) (net.Conn, error) {
	c, err := g.Loopback.Dial(ctx, m)
	if err != nil {
		return nil, err
	}
	gc := &gatedConn{Conn: c, held: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{})}
	g.mu.Lock()
	g.last = gc
	g.mu.Unlock()
	return gc, nil
}

func (g *gated) conn() *gatedConn {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.last
}

type gatedConn struct {
	net.Conn
	hold      atomic.Bool
	held      chan struct{} // closed once a write is held
	release   chan struct{}
	closed    chan struct{}
	heldOnce  sync.Once
	closeOnce sync.Once
}

func (c *gatedConn) Write(p []byte) (int, error) {
	if c.hold.Load() {
		c.heldOnce.Do(func() { close(c.held) })
		select {
		case <-c.release:
		case <-c.closed:
			return 0, net.ErrClosed
		}
	}
	return c.Conn.Write(p)
}

func (c *gatedConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

func TestDroppedSocketIsNotTheCallersCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	g := &gated{Loopback: start(t, "tcp", &Server{Endpoint: &fakeEndpoint{}})}
	c := NewEndpointClient(g, api.ID{})
	if _, err := c.Info(ctx); err != nil {
		t.Fatal(err)
	}
	gc := g.conn()
	gc.hold.Store(true)
	done := make(chan error, 1)
	go func() {
		_, err := c.Submit(ctx, api.Command{ID: api.NewID(), Body: api.TurnInterrupt{}})
		done <- err
	}()
	<-gc.held
	gc.Conn.(*net.TCPConn).CloseRead()
	err := <-done
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, errLost) {
		t.Fatalf("submit on a socket that died under its write: got %v, want a lost connection and no context error", err)
	}
}

// hung's dials wait for their ctx, like a peer that never answers.
type hung struct {
	Loopback
	dialing chan struct{} // gets one value per dial begun
	gave    chan struct{} // gets one value per dial given up
}

func (h *hung) Dial(ctx context.Context, _ api.ID) (net.Conn, error) {
	h.dialing <- struct{}{}
	<-ctx.Done()
	h.gave <- struct{}{}
	return nil, ctx.Err()
}

func TestDialWaitHonorsEachCallersContext(t *testing.T) {
	h := &hung{dialing: make(chan struct{}, 2), gave: make(chan struct{}, 2)}
	c := NewEndpointClient(h, api.ID{})
	first, stopFirst := context.WithCancel(context.Background())
	defer stopFirst()
	firstDone := make(chan error, 1)
	go func() {
		_, err := c.Info(first)
		firstDone <- err
	}()
	<-h.dialing

	second, stopSecond := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer stopSecond()
	secondDone := make(chan error, 1)
	go func() {
		_, err := c.Info(second)
		secondDone <- err
	}()
	select {
	case err := <-secondDone:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("a 30ms call behind a hung dial: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		stopFirst()
		t.Fatal("a 30ms call waits on another caller's hung dial")
	}
	select {
	case <-h.gave:
		t.Fatal("the dial was given up while a caller still waited for it")
	default:
	}
	stopFirst()
	if err := <-firstDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("first caller: %v", err)
	}
	select {
	case <-h.gave:
	case <-time.After(time.Second):
		t.Fatal("the dial went on after every caller had left")
	}
	if len(h.dialing) != 0 {
		t.Fatal("the second caller dialed again instead of waiting for the first dial")
	}
}

func TestWriteWaitHonorsTheCallersContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	g := &gated{Loopback: start(t, "tcp", &Server{Endpoint: &fakeEndpoint{}})}
	c := NewEndpointClient(g, api.ID{})
	if _, err := c.Info(ctx); err != nil {
		t.Fatal(err)
	}
	gc := g.conn()
	gc.hold.Store(true)
	submit, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		_, err := c.Submit(submit, api.Command{ID: api.NewID(), Body: api.TurnInterrupt{}})
		done <- err
	}()
	<-gc.held
	stop()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("submit: %v, want its own cancel", err)
		}
	case <-time.After(time.Second):
		t.Fatal("a cancelled submit still waits on a held write")
	}
	gc.hold.Store(false)
	close(gc.release)
	if _, err := c.Info(ctx); err != nil {
		t.Fatalf("the socket after a cancelled write: %v", err)
	}
}

// submits is an Endpoint that reports each command it runs.
type submits struct {
	fakeEndpoint
	ran chan api.ID
}

func (s *submits) Submit(ctx context.Context, cmd api.Command) (api.Receipt, error) {
	s.ran <- cmd.ID
	return s.fakeEndpoint.Submit(ctx, cmd)
}

func TestCancelledCallSendsNothing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ep := &submits{ran: make(chan api.ID, 2)}
	c := NewEndpointClient(start(t, "tcp", &Server{Endpoint: ep}), api.ID{})
	if _, err := c.Info(ctx); err != nil {
		t.Fatal(err)
	}
	gone, stop := context.WithCancel(ctx)
	stop()
	if _, err := c.Submit(gone, api.Command{ID: api.NewID(), Body: api.TurnInterrupt{}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("submit with a cancelled ctx: %v", err)
	}
	live := api.Command{ID: api.NewID(), Body: api.TurnInterrupt{}}
	if _, err := c.Submit(ctx, live); err != nil {
		t.Fatal(err)
	}
	if got := <-ep.ran; got != live.ID {
		t.Fatalf("the server ran %v, which its caller had cancelled before sending", got)
	}
	select {
	case got := <-ep.ran:
		t.Fatalf("the server also ran %v, which its caller had cancelled before sending", got)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestCallWaitsBehindAnotherWriteOnlyUntilItsDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ep := &submits{ran: make(chan api.ID, 1)}
	g := &gated{Loopback: start(t, "unix", &Server{Endpoint: ep})}
	c := NewEndpointClient(g, api.ID{})
	if _, err := c.Info(ctx); err != nil {
		t.Fatal(err)
	}
	gc := g.conn()
	gc.hold.Store(true)
	first := api.Command{ID: api.NewID(), Body: api.TurnInterrupt{}}
	firstDone := make(chan error, 1)
	go func() {
		_, err := c.Submit(ctx, first)
		firstDone <- err
	}()
	<-gc.held

	short, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	defer stop()
	t0 := time.Now()
	if _, err := c.Info(short); !errors.Is(err, context.DeadlineExceeded) || time.Since(t0) > 500*time.Millisecond {
		t.Fatalf("a 100ms call behind a held write: %v after %v", err, time.Since(t0))
	}
	gc.hold.Store(false)
	close(gc.release)
	if err := <-firstDone; err != nil {
		t.Fatalf("the held submit: %v", err)
	}
	if got := <-ep.ran; got != first.ID {
		t.Fatalf("ran %v", got)
	}
}

func TestCloseEndsTheClientsCallsAndStreams(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	fake := &fakeEndpoint{env: api.Environment{ID: api.NewID()}, closed: make(chan api.ID, 1)}
	g := &gated{Loopback: start(t, "tcp", &Server{Endpoint: fake})}
	c := NewEndpointClient(g, api.ID{})
	thread := api.NewID()
	st, err := c.Open(ctx, api.Subscription{Thread: thread})
	if err != nil {
		t.Fatal(err)
	}
	blocked := make(chan error, 1)
	go func() {
		_, err := c.Read(ctx, api.Query{Thread: blocks})
		blocked <- err
	}()
	for range 5 {
		if _, err := st.Next(ctx); err != nil {
			t.Fatal(err)
		}
	}

	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-g.conn().closed:
	default:
		t.Fatal("Close returned with the socket still open")
	}
	if _, err := st.Next(ctx); !errors.Is(err, errClosed) {
		t.Fatalf("stream after Close: %v", err)
	}
	if err := <-blocked; !errors.Is(err, errClosed) {
		t.Fatalf("call in flight at Close: %v", err)
	}
	if _, err := c.Info(ctx); !errors.Is(err, errClosed) {
		t.Fatalf("call after Close: %v", err)
	}
	select {
	case got := <-fake.closed:
		if got != thread {
			t.Fatalf("server closed %v", got)
		}
	case <-ctx.Done():
		t.Fatal("the server never closed the stream of a closed client")
	}
}

func TestIdleSocketCloses(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	g := &gated{Loopback: start(t, "tcp", &Server{Endpoint: &fakeEndpoint{}})}
	c := NewEndpointClient(g, api.ID{})
	c.c.idleAfter = 50 * time.Millisecond

	st, err := c.Open(ctx, api.Subscription{Thread: api.NewID()})
	if err != nil {
		t.Fatal(err)
	}
	first := g.conn()
	select {
	case <-first.closed:
		t.Fatal("a socket with an open stream was closed as idle")
	case <-time.After(200 * time.Millisecond):
	}
	st.Close()
	select {
	case <-first.closed:
	case <-ctx.Done():
		t.Fatal("a socket with no calls or streams stayed open")
	}
	if _, err := c.Info(ctx); err != nil {
		t.Fatalf("a call after the idle close: %v", err)
	}
	if g.conn() == first {
		t.Fatal("the call after the idle close did not dial again")
	}
}

func TestCancelReachesTheServer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	fake := &fakeEndpoint{env: api.Environment{ID: api.NewID()}, reads: make(chan error, 2), closed: make(chan api.ID, 1)}
	d := &dialer{Loopback: start(t, "unix", &Server{Endpoint: fake})}
	c := NewEndpointClient(d, api.ID{})

	read, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		_, err := c.Read(read, api.Query{Thread: blocks})
		done <- err
	}()
	<-fake.reads
	stop()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read: %v", err)
	}
	select {
	case err := <-fake.reads:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("the server's read ended with %v, want its ctx cancelled", err)
		}
	case <-ctx.Done():
		t.Fatal("the server's read never saw the cancel")
	}

	thread := api.NewID()
	st, err := c.Open(ctx, api.Subscription{Thread: thread})
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	select {
	case got := <-fake.closed:
		if got != thread {
			t.Fatalf("server closed %v", got)
		}
	case <-ctx.Done():
		t.Fatal("the server never closed a stream the client closed")
	}
	if env, err := c.Info(ctx); err != nil || env != fake.env {
		t.Fatalf("info: %+v %v", env, err)
	}
	if n := len(d.dialed()); n != 1 {
		t.Fatalf("dialed %d sockets, want 1: the server saw the socket drop, not the cancels", n)
	}
}
