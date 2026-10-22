package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"glitched/internal/transport"
)

// quiet drops the engine's logs for the rest of the test.
func quiet(t *testing.T) {
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
}

type recorder struct {
	mu   *sync.Mutex
	msgs *[]string
}

func (r recorder) Enabled(context.Context, slog.Level) bool { return true }
func (r recorder) WithAttrs([]slog.Attr) slog.Handler       { return r }
func (r recorder) WithGroup(string) slog.Handler            { return r }
func (r recorder) Handle(_ context.Context, rec slog.Record) error {
	r.mu.Lock()
	*r.msgs = append(*r.msgs, rec.Message)
	r.mu.Unlock()
	return nil
}

// logged records the engine's log messages for the rest of the test, and
// counts those equal to msg.
func logged(t *testing.T) (count func(msg string) int) {
	var (
		mu   sync.Mutex
		msgs []string
	)
	old := slog.Default()
	slog.SetDefault(slog.New(recorder{&mu, &msgs}))
	t.Cleanup(func() { slog.SetDefault(old) })
	return func(msg string) int {
		mu.Lock()
		defer mu.Unlock()
		n := 0
		for _, m := range msgs {
			if m == msg {
				n++
			}
		}
		return n
	}
}

func TestARefusedPeerIsLoggedOnceAMinute(t *testing.T) {
	count := logged(t)
	g := &grants{dir: stateDir(t), base: context.Background()}
	for _, from := range []string{"100.64.0.5:1001", "100.64.0.5:1002", "100.64.0.5:1003", "100.64.0.6:1001", "100.64.0.5:1004"} {
		r := httptest.NewRequest("GET", "/ws", nil)
		r.RemoteAddr = from
		r.Header.Set("Authorization", "Bearer wrong")
		if _, err := g.authorize(r); err == nil {
			t.Fatalf("admitted a wrong token from %s", from)
		}
	}
	if n := count("refused a socket"); n != 2 {
		t.Fatalf("logged %d refusals from 2 peers within a minute, want 2", n)
	}
}

func TestAnUnreadableTokensFileIsLoggedOncePerError(t *testing.T) {
	count := logged(t)
	dir := stateDir(t)
	token, err := Pair(dir, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	d := start(t, Config{Dir: dir, Listen: []string{"127.0.0.1:0"}})
	path := filepath.Join(dir, tokensFile)
	good, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := func(b string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(b), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	upgrades := func(n int) error {
		var err error
		for range n {
			_, err = remote(t, d, token).Info(context.Background())
		}
		return err
	}
	const failing = "admitting no token"

	corrupt("{")
	time.Sleep(2500 * time.Millisecond) // the engine rereads the file every second
	if n := count(failing); n != 1 {
		t.Fatalf("the engine's rereads logged %d errors, want 1", n)
	}
	if err := upgrades(3); !errors.Is(err, transport.ErrRefused) {
		t.Fatalf("a paired token while tokens.json is unreadable: %v, want ErrRefused", err)
	}
	if n := count(failing); n != 1 {
		t.Fatalf("3 upgrades and the rereads logged %d errors, want 1", n)
	}
	corrupt("[1]")
	if err := upgrades(2); !errors.Is(err, transport.ErrRefused) {
		t.Fatalf("a paired token while tokens.json is unreadable: %v, want ErrRefused", err)
	}
	if n := count(failing); n != 2 {
		t.Fatalf("a second error logged %d errors in all, want 2", n)
	}
	corrupt(string(good))
	if err := upgrades(1); err != nil {
		t.Fatalf("a paired token once tokens.json is readable again: %v", err)
	}
}

func TestNoUpgradeAfterARevokeReturnsGetsIn(t *testing.T) {
	quiet(t)
	for attempt := range 50 {
		dir := stateDir(t)
		var others []Token
		for i := range 32 {
			s := sha256.Sum256([]byte(fmt.Sprint(i)))
			others = append(others, Token{Label: fmt.Sprint("other", i), Hash: hex.EncodeToString(s[:]), Created: time.Now()})
		}
		b, _ := json.Marshal(others)
		if err := os.WriteFile(filepath.Join(dir, tokensFile), b, 0o600); err != nil {
			t.Fatal(err)
		}
		token, err := Pair(dir, "laptop")
		if err != nil {
			t.Fatal(err)
		}
		g := &grants{dir: dir, base: context.Background()}
		var running atomic.Bool
		running.Store(true)
		var bg sync.WaitGroup
		for range 64 {
			bg.Go(func() {
				for running.Load() {
					g.authorize(httptest.NewRequest("GET", "/ws", nil))
				}
			})
		}
		time.Sleep(2 * time.Millisecond)
		if err := Revoke(dir, "laptop"); err != nil {
			t.Fatal(err)
		}
		var admitted atomic.Int32
		var after sync.WaitGroup
		for range 64 {
			after.Go(func() {
				r := httptest.NewRequest("GET", "/ws", nil)
				r.Header.Set("Authorization", "Bearer "+token)
				if grant, err := g.authorize(r); err == nil && grant.Err() == nil {
					admitted.Add(1)
				}
			})
		}
		after.Wait()
		running.Store(false)
		bg.Wait()
		if n := admitted.Load(); n > 0 {
			t.Fatalf("attempt %d: %d upgrades that began after Revoke returned got in with the revoked token", attempt, n)
		}
	}
}
