package daemon

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// A Token is a pairing token as tokens.json keeps it: by its hash, so the
// file alone lets no one connect.
type Token struct {
	Label   string    `json:"label"`
	Hash    string    `json:"sha256"` // hex SHA-256 of the token
	Created time.Time `json:"created"`
}

const tokensFile = "tokens.json"

// Pair mints a token for label. It is returned once and never stored.
func Pair(dir, label string) (string, error) {
	if strings.TrimSpace(label) == "" {
		return "", errors.New("pair: a token needs a label")
	}
	secret := make([]byte, 32)
	rand.Read(secret)
	token := base64.RawURLEncoding.EncodeToString(secret)
	sum := sha256.Sum256([]byte(token))
	err := edit(dir, func(ts []Token) ([]Token, error) {
		if slices.ContainsFunc(ts, func(t Token) bool { return t.Label == label }) {
			return nil, fmt.Errorf("pair: %s already has a token; revoke it first", label)
		}
		return append(ts, Token{Label: label, Hash: hex.EncodeToString(sum[:]), Created: time.Now().UTC()}), nil
	})
	if err != nil {
		return "", err
	}
	return token, nil
}

// Revoke removes label's token. An engine refuses it on the next upgrade and
// closes the sockets it admitted within a second.
func Revoke(dir, label string) error {
	return edit(dir, func(ts []Token) ([]Token, error) {
		i := slices.IndexFunc(ts, func(t Token) bool { return t.Label == label })
		if i < 0 {
			return nil, fmt.Errorf("pair: no token is labelled %s", label)
		}
		return slices.Delete(ts, i, i+1), nil
	})
}

func Tokens(dir string) ([]Token, error) {
	b, err := os.ReadFile(filepath.Join(dir, tokensFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ts []Token
	if err := json.Unmarshal(b, &ts); err != nil {
		return nil, fmt.Errorf("%s: %w", tokensFile, err)
	}
	return ts, nil
}

// edit rewrites tokens.json under a lock, so a pair racing a revoke can't
// bring the revoked token back.
func edit(dir string, change func([]Token) ([]Token, error)) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	lk, err := lock(filepath.Join(dir, "tokens.lock"))
	if errors.Is(err, ErrLocked) {
		return errors.New("pair: another pair command is changing the tokens")
	}
	if err != nil {
		return err
	}
	defer lk.Close()
	ts, err := Tokens(dir)
	if err != nil {
		return err
	}
	if ts, err = change(ts); err != nil {
		return err
	}
	b, err := json.MarshalIndent(ts, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, tokensFile+".*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(append(b, '\n'))
	if err == nil {
		err = f.Sync()
	}
	if err := errors.Join(err, f.Close()); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), filepath.Join(dir, tokensFile)); err != nil {
		return err
	}
	return syncDir(dir)
}

// grants admits a socket whose token tokens.json holds. It reads the file on
// every upgrade, so a revoke from another process applies to the next one,
// and every second, to close the sockets a revoked token holds.
type grants struct {
	dir  string
	base context.Context

	// mu spans a read of the file through the admission it decides, so a
	// read from before a revoke never replaces the grants of a later one.
	mu       sync.Mutex
	live     map[[sha256.Size]byte]grant
	failing  string               // the last error reading the file, logged once
	refusals map[string]time.Time // when each peer's last refusal was logged
}

type grant struct {
	label  string
	ctx    context.Context
	cancel context.CancelFunc
}

// reload admits no token when the file can't be read. g.mu must be held.
func (g *grants) reload() {
	ts, err := Tokens(g.dir)
	switch {
	case err != nil && err.Error() != g.failing:
		g.failing = err.Error()
		slog.Error("admitting no token", "err", err)
	case err == nil && g.failing != "":
		g.failing = ""
		slog.Info("admitting tokens again", "file", filepath.Join(g.dir, tokensFile))
	}
	live := map[[sha256.Size]byte]grant{}
	for _, t := range ts {
		var sum [sha256.Size]byte
		if b, _ := hex.DecodeString(t.Hash); len(b) == len(sum) {
			copy(sum[:], b)
		} else {
			continue
		}
		gr, ok := g.live[sum]
		if ok {
			delete(g.live, sum)
		} else {
			gr.label = t.Label
			gr.ctx, gr.cancel = context.WithCancel(g.base)
		}
		live[sum] = gr
	}
	for _, gr := range g.live {
		gr.cancel()
	}
	g.live = live
}

func (g *grants) watch(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			g.mu.Lock()
			g.reload()
			g.mu.Unlock()
		}
	}
}

var errUnknownToken = errors.New("unknown token")

func (g *grants) authorize(r *http.Request) (context.Context, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.reload()
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	sum := sha256.Sum256([]byte(token))
	var match *grant
	for s, gr := range g.live {
		if subtle.ConstantTimeCompare(s[:], sum[:]) == 1 {
			match = &gr
		}
	}
	if !ok || match == nil {
		g.refused(r.RemoteAddr)
		return nil, errUnknownToken
	}
	slog.Info("admitted a socket", "token", match.label, "from", r.RemoteAddr)
	return match.ctx, nil
}

// refused logs a peer's refusal at most once a minute, however often it
// retries. g.mu must be held.
func (g *grants) refused(from string) {
	peer, _, _ := net.SplitHostPort(from)
	now := time.Now()
	if now.Sub(g.refusals[peer]) < time.Minute {
		return
	}
	if g.refusals == nil {
		g.refusals = map[string]time.Time{}
	}
	maps.DeleteFunc(g.refusals, func(_ string, at time.Time) bool { return now.Sub(at) >= time.Minute })
	g.refusals[peer] = now
	slog.Warn("refused a socket", "from", from)
}
