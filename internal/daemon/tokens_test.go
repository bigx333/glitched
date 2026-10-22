package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
)

// quiet drops the engine's logs for the rest of the test.
func quiet(t *testing.T) {
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
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
