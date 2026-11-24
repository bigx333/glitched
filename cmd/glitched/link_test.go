package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"glitched/internal/daemon"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
)

// B's person turns children from other machines on and mints A a link;
// A adds B with the token pair printed, and lists it.
func TestALinkMintedOnOneMachineIsAddedOnAnother(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	b, a := stateDir(t), stateDir(t)
	if err := pair(ctx, []string{"-dir", b, "-name", "a", "-link"}); err == nil {
		t.Fatal("pair -link minted while children from other machines are off")
	}
	var err error
	if out := stdout(t, func() { err = pair(ctx, []string{"-dir", b, "-children", "full"}) }); err != nil || !strings.Contains(out, "credentials") {
		t.Fatalf("pair -children full: %v, printed no warning:\n%s", err, out)
	}
	if err := pair(ctx, []string{"-dir", b, "-children", "auto-edits"}); err != nil {
		t.Fatal(err)
	}
	out := stdout(t, func() { err = pair(ctx, []string{"-dir", b, "-name", "a", "-link"}) })
	if err != nil {
		t.Fatal(err)
	}
	_, rest, _ := strings.Cut(out, "shown only this once:\n\n  ")
	token, _, _ := strings.Cut(rest, "\n")
	if token == "" || !strings.Contains(out, "glitched link -add") || !strings.Contains(out, "\n  glitched pair -dir "+shellQuote(b)+" -revoke a\n") {
		t.Fatalf("pair -link printed no token and add line:\n%s", out)
	}
	d, err := daemon.Start(ctx, daemon.Config{Dir: b, Hub: true, Listen: []string{"127.0.0.1:0"}, Providers: provider.Mux{"fake": fake.New(fake.Turn(true))}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	addr := d.Addrs()[0].String()
	if err := link(ctx, []string{"-dir", a, "-add", "b", "-server", addr, "-token", token}); err != nil {
		t.Fatalf("link -add: %v", err)
	}
	if out := stdout(t, func() { err = link(ctx, []string{"-dir", a, "-list"}) }); err != nil || !strings.HasPrefix(out, "b  "+addr) {
		t.Fatalf("link -list: %v\n%s", err, out)
	}
	if out := stdout(t, func() { err = pair(ctx, []string{"-dir", b, "-list"}) }); err != nil || !strings.Contains(out, "a  link ") {
		t.Fatalf("pair -list on B: %v\n%s", err, out)
	}
	if err := link(ctx, []string{"-dir", a, "-remove", "b"}); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"-dir", b, "-name", "c", "-access", "full"},
		{"-dir", b, "-children", "most"},
		{"-dir", b, "-name", "review", "laptop", "-link"},
		{"-dir", b, "-list", "-revoke", "a"},
		{"-dir", b, "-link"},
	} {
		if err := pair(ctx, args); !errors.As(err, new(usageError)) {
			t.Errorf("pair %v: %v, want a usage error", args, err)
		}
	}
	for _, args := range [][]string{{"-dir", a, "-add", "b"}, {"-dir", a, "-list", "-remove", "b"}, {"-dir", a, "-list", "b"}, {"-dir", a, "-server", addr}} {
		if err := link(ctx, args); !errors.As(err, new(usageError)) {
			t.Errorf("link %v: %v, want a usage error", args, err)
		}
	}
	if err := link(ctx, []string{"-dir", a, "-add", "b", "-server", addr, "secret-value"}); err == nil || strings.Contains(err.Error(), "secret-value") {
		t.Errorf("link with a stray argument: %v, want a usage error that does not repeat it", err)
	}
	if ts, err := daemon.Tokens(b); err != nil || len(ts) != 1 || ts[0].Link == nil {
		t.Fatalf("tokens.json after the refused pairs: %+v %v, want only the link", ts, err)
	}
}
