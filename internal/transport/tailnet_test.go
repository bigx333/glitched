package transport

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"glitched/internal/api"
)

func TestTailnetListensOnlyOnLoopbackOrTailscale(t *testing.T) {
	ok := []string{"127.0.0.1:0", "[::1]:0", "100.64.0.1:7465", "100.127.255.254:7465", "[fd7a:115c:a1e0::1]:7465"}
	refused := []string{"0.0.0.0:7465", "[::]:7465", ":7465", "8.8.8.8:7465", "192.168.1.10:7465", "100.128.0.1:7465", "[fd7a:115c:a1e1::1]:7465", "localhost:7465"}
	for _, addr := range ok {
		// This machine may not hold the address, so only the rule's refusal counts.
		ln, err := Tailnet{Address: addr}.Listen(context.Background())
		if errors.Is(err, errNotTailnet) {
			t.Errorf("%s refused: %v", addr, err)
		}
		if ln != nil {
			ln.Close()
		}
	}
	for _, addr := range refused {
		ln, err := Tailnet{Address: addr}.Listen(context.Background())
		if !errors.Is(err, errNotTailnet) {
			t.Errorf("%s: %v, want the tailnet rule's refusal", addr, err)
		}
		if ln != nil {
			ln.Close()
		}
	}
}

func TestAuthorizeRefusesWith401AndClosesARevokedSocket(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	grant, revoke := context.WithCancel(ctx)
	var revoked atomic.Bool
	ep := &fakeEndpoint{reads: make(chan error, 2)}
	srv := &Server{Transport: Tailnet{}, Endpoint: ep, Authorize: func(r *http.Request) (context.Context, error) {
		if revoked.Load() || r.Header.Get("Authorization") != "Bearer good" {
			return nil, errors.New("unknown token")
		}
		return grant, nil
	}}
	ln, err := Tailnet{Address: "127.0.0.1:0"}.Listen(ctx)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error)
	go func() { done <- srv.Serve(ctx, ln) }()
	defer func() { cancel(); <-done }()
	addr := ln.Addr().String()

	bad := NewEndpointClient(Tailnet{Address: addr, Token: "bad"}, api.ID{})
	defer bad.Close()
	if _, err := bad.Info(ctx); !errors.Is(err, ErrRefused) {
		t.Fatalf("a wrong token: %v, want ErrRefused", err)
	}
	good := NewEndpointClient(Tailnet{Address: addr, Token: "good"}, api.ID{})
	defer good.Close()
	if _, err := good.Info(ctx); err != nil {
		t.Fatal(err)
	}
	blocked := make(chan error, 1)
	go func() {
		_, err := good.Read(ctx, api.Query{Thread: blocks})
		blocked <- err
	}()
	<-ep.reads
	revoked.Store(true)
	revoke()
	if err := <-blocked; !errors.Is(err, errLost) {
		t.Fatalf("a call on a socket whose grant ended: %v, want the connection lost", err)
	}
	if _, err := good.Info(ctx); !errors.Is(err, ErrRefused) {
		t.Fatalf("redialling with a revoked token: %v, want ErrRefused", err)
	}
}
