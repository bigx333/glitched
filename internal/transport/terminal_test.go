package transport

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/pty"
)

func readTermUntil(t *testing.T, ctx context.Context, st api.TerminalStream, want string) api.TerminalItem {
	t.Helper()
	var sb strings.Builder
	for {
		it, err := st.Next(ctx)
		if err != nil {
			t.Fatalf("TerminalStream Next error before %q (saw %q): %v", want, sb.String(), err)
		}
		sb.Write(it.Data)
		if strings.Contains(sb.String(), want) {
			return it
		}
	}
}

func TestRemoteTerminalStreamInputResizeAndLease(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	mgr := pty.New(pty.Options{Command: []string{"/bin/sh"}})
	t.Cleanup(func() { _ = mgr.Close() })

	grantACtx, revokeA := context.WithCancel(context.Background())
	defer revokeA()
	grantBCtx, revokeB := context.WithCancel(context.Background())
	defer revokeB()

	srv := &Server{
		Transport: Tailnet{},
		Endpoint:  &fakeEndpoint{},
		Terminals: mgr,
		Authorize: func(_ *http.Request, proved func(Key) bool) (Grant, error) {
			if k := KeyOf("tok-a"); proved(k) {
				return Grant{Key: k, Done: grantACtx}, nil
			}
			if k := KeyOf("tok-b"); proved(k) {
				return Grant{Key: k, Done: grantBCtx}, nil
			}
			return Grant{}, errors.New("unknown token")
		},
	}
	addr := serveTailnet(t, srv)

	c1 := NewEndpointClient(Tailnet{Address: addr, Token: "tok-a"}, api.ID{})
	defer c1.Close()
	c2 := NewEndpointClient(Tailnet{Address: addr, Token: "tok-b"}, api.ID{})
	defer c2.Close()

	ws := t.TempDir()
	st1, err := c1.OpenTerminal(ctx, api.TerminalOpen{Workspace: ws, Cols: 80, Rows: 24, Client: "client-a"})
	if err != nil {
		t.Fatalf("c1 OpenTerminal: %v", err)
	}
	defer st1.Close()

	initItem, err := st1.Next(ctx)
	if err != nil {
		t.Fatalf("c1 Next initial: %v", err)
	}
	if initItem.Session.IsZero() {
		t.Fatal("expected non-zero session ID")
	}

	if err := c1.TerminalInput(ctx, api.TerminalInput{
		Workspace: ws,
		Session:   initItem.Session,
		Client:    "client-a",
		Data:      []byte("echo transport-ok-111\n"),
	}); err != nil {
		t.Fatalf("c1 TerminalInput: %v", err)
	}
	readTermUntil(t, ctx, st1, "transport-ok-111")

	if err := c1.TerminalResize(ctx, api.TerminalResize{
		Workspace: ws,
		Session:   initItem.Session,
		Client:    "client-a",
		Cols:      100,
		Rows:      32,
	}); err != nil {
		t.Fatalf("c1 TerminalResize: %v", err)
	}

	// Second client cannot write while client-a holds active lease.
	err = c2.TerminalInput(ctx, api.TerminalInput{
		Workspace: ws,
		Session:   initItem.Session,
		Client:    "client-b",
		Data:      []byte("echo blocked\n"),
	})
	if !errors.Is(err, api.ErrLeaseHeld) {
		t.Fatalf("c2 input err = %v, want api.ErrLeaseHeld", err)
	}

	// Second client opens stream and force-claims lease.
	st2, err := c2.OpenTerminal(ctx, api.TerminalOpen{Workspace: ws, Session: initItem.Session, After: 0, Client: "client-b"})
	if err != nil {
		t.Fatalf("c2 OpenTerminal: %v", err)
	}
	defer st2.Close()
	readTermUntil(t, ctx, st2, "transport-ok-111")

	lease, err := c2.TerminalClaim(ctx, api.TerminalClaim{
		Workspace: ws,
		Session:   initItem.Session,
		Client:    "client-b",
		Force:     true,
	})
	if err != nil || lease.Client != "client-b" {
		t.Fatalf("c2 TerminalClaim = (%+v, %v), want client-b", lease, err)
	}

	// Revoking client-b's grant closes its socket and releases its lease so client-a can write again.
	revokeB()
	deadline := time.Now().Add(5 * time.Second)
	var writeErr error
	for time.Now().Before(deadline) {
		writeErr = c1.TerminalInput(ctx, api.TerminalInput{
			Workspace: ws,
			Session:   initItem.Session,
			Client:    "client-a",
			Data:      []byte("echo recovered-222\n"),
		})
		if writeErr == nil {
			break
		}
		if !errors.Is(writeErr, api.ErrLeaseHeld) {
			t.Fatalf("unexpected err waiting for lease release: %v", writeErr)
		}
		time.Sleep(25 * time.Millisecond)
	}
	if writeErr != nil {
		t.Fatalf("c1 failed to regain input after client-b grant revocation: %v", writeErr)
	}
	readTermUntil(t, ctx, st1, "recovered-222")
}

// releaseSpy reports each client whose terminal leases the server releases.
type releaseSpy struct {
	*pty.Manager
	released chan string
}

func (r releaseSpy) ReleaseTerminalClient(client string) {
	r.Manager.ReleaseTerminalClient(client)
	r.released <- client
}

func TestClosingAStaleSocketKeepsTheReconnectedClientsLease(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	mgr := pty.New(pty.Options{Command: []string{"/bin/sh"}})
	t.Cleanup(func() { _ = mgr.Close() })
	spy := releaseSpy{Manager: mgr, released: make(chan string, 16)}
	srv := &Server{
		Transport: Tailnet{},
		Endpoint:  &fakeEndpoint{},
		Terminals: spy,
		Authorize: func(_ *http.Request, proved func(Key) bool) (Grant, error) {
			for _, tok := range []string{"tok-a", "tok-b"} {
				if k := KeyOf(tok); proved(k) {
					return Grant{Key: k, Done: context.Background()}, nil
				}
			}
			return Grant{}, errors.New("unknown token")
		},
	}
	addr := serveTailnet(t, srv)
	ws := t.TempDir()

	stale := NewEndpointClient(Tailnet{Address: addr, Token: "tok-a"}, api.ID{})
	defer stale.Close()
	st1, err := stale.OpenTerminal(ctx, api.TerminalOpen{Workspace: ws, Client: "client-a"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := st1.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// A client only this socket used, whose release tells the socket's
	// teardown ran.
	_ = stale.TerminalInput(ctx, api.TerminalInput{Workspace: ws, Session: first.Session, Client: "probe"})

	fresh := NewEndpointClient(Tailnet{Address: addr, Token: "tok-a"}, api.ID{})
	defer fresh.Close()
	st2, err := fresh.OpenTerminal(ctx, api.TerminalOpen{Workspace: ws, Session: first.Session, Client: "client-a"})
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if _, err := st2.Next(ctx); err != nil {
		t.Fatal(err)
	}

	st1.Close()
	stale.Close()
	for got := ""; got != "probe"; {
		select {
		case got = <-spy.released:
		case <-ctx.Done():
			t.Fatal("the stale socket's teardown never released its clients")
		}
	}

	other := NewEndpointClient(Tailnet{Address: addr, Token: "tok-b"}, api.ID{})
	defer other.Close()
	err = other.TerminalInput(ctx, api.TerminalInput{Workspace: ws, Session: first.Session, Client: "client-b", Data: []byte("echo hijacked\n")})
	if !errors.Is(err, api.ErrLeaseHeld) {
		t.Fatalf("client-b's input: %v, want api.ErrLeaseHeld while client-a's live socket holds the lease", err)
	}
}
