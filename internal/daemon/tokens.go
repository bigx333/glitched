package daemon

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
	"glitched/internal/jsonfile"
	"glitched/internal/transport"
)

// A Token is a pairing token as tokens.json keeps it: by its SHA-256, which
// is the key a client and the engine prove to each other (transport.KeyOf).
// The hash is as secret as the token: with it a client connects and a server
// passes for this engine. So the file is the user's alone, mode 0600.
type Token struct {
	Label   string    `json:"label"`
	Hash    string    `json:"sha256,omitempty"` // hex SHA-256 of the token; a link's is in Link
	Created time.Time `json:"created"`
	// Link is set for a token another machine's engine holds, which reaches
	// only what its scope allows. Its hash is kept in it, never in Hash, so
	// a build from before links, which admits every Hash to the whole
	// engine, skips the token instead.
	Link *LinkToken `json:"link,omitempty"`
}

// A LinkToken is a link's scope as tokens.json keeps it (api.Link).
type LinkToken struct {
	ID      api.ID     `json:"id"`
	Hash    string     `json:"sha256"`
	Access  api.Access `json:"access"`
	Working int        `json:"working,omitempty"`
	All     int        `json:"all,omitempty"`
}

// hash is the hex SHA-256 of t's token.
func (t Token) hash() string {
	if t.Link != nil {
		return t.Link.Hash
	}
	return t.Hash
}

// link is t's scope, nil for a token that reaches the whole engine.
func (t Token) link() *api.Link {
	if t.Link == nil {
		return nil
	}
	return &api.Link{ID: t.Link.ID, Label: t.Label, Access: t.Link.Access, Working: t.Link.Working, All: t.Link.All}
}

const tokensFile = "tokens.json"

// Pair mints a token for label. It is returned once and never stored.
func Pair(dir, label string) (string, error) {
	return mint(dir, label, func(hash string) (Token, error) {
		return Token{Label: label, Hash: hash}, nil
	})
}

// PairLink mints a token for the engine of the machine label names, scoped
// to l: Label and ID are its own. The scope may be no wider than this
// engine's setting for children from other machines, which must be on.
func PairLink(dir, label string, l api.Link) (string, api.Link, error) {
	l.ID, l.Label = api.NewID(), label
	token, err := mint(dir, label, func(hash string) (Token, error) {
		cfg, err := Defaults(dir)
		if err != nil {
			return Token{}, err
		}
		switch ceiling := cfg.ChildrenFromOtherMachines; {
		case ceiling == nil:
			return Token{}, errors.New("pair: children from other machines are off on this machine; turn them on first")
		case l.Access > *ceiling:
			return Token{}, fmt.Errorf("pair: a link may run children at %s at most here, as this machine's setting for children from other machines says", AccessName(*ceiling))
		case l.Working < 0 || l.All < 0:
			return Token{}, errors.New("pair: a link's counts can't be negative")
		}
		return Token{Label: label, Link: &LinkToken{ID: l.ID, Hash: hash, Access: l.Access, Working: l.Working, All: l.All}}, nil
	})
	if err != nil {
		return "", api.Link{}, err
	}
	return token, l, nil
}

// mint adds the token made for a new secret's hash to tokens.json, under
// label, and returns the secret.
func mint(dir, label string, made func(hash string) (Token, error)) (string, error) {
	if strings.TrimSpace(label) == "" {
		return "", errors.New("pair: a token needs a label")
	}
	secret := make([]byte, 32)
	rand.Read(secret)
	token := base64.RawURLEncoding.EncodeToString(secret)
	key := transport.KeyOf(token)
	err := edit(dir, func(ts *[]Token) error {
		if slices.ContainsFunc(*ts, func(t Token) bool { return t.Label == label }) {
			return fmt.Errorf("pair: %s already has a token; remove it first", label)
		}
		t, err := made(hex.EncodeToString(key[:]))
		if err != nil {
			return err
		}
		t.Created = time.Now().UTC()
		*ts = append(*ts, t)
		return nil
	})
	if err != nil {
		return "", err
	}
	return token, nil
}

// AccessName is a's name as pair's flags and messages give it.
func AccessName(a api.Access) string {
	for name, v := range accessNames {
		if v == a {
			return name
		}
	}
	return fmt.Sprint(int(a))
}

// ParseAccess is the access level name names (AccessName).
func ParseAccess(name string) (api.Access, error) {
	a, ok := accessNames[name]
	if !ok {
		return 0, errors.New("unknown access: give ask, auto-edits or full")
	}
	return a, nil
}

var accessNames = map[string]api.Access{"ask": api.AccessAsk, "auto-edits": api.AccessAutoEdits, "full": api.AccessFull}

// RemoveToken removes label's token. An engine refuses it on the next
// socket and closes the sockets it admitted within a second. For a link,
// that removes the machine it was minted for: its threads here stay, and
// what they run goes on, for this machine's person to stop.
func RemoveToken(dir, label string) error {
	return edit(dir, func(ts *[]Token) error {
		i := slices.IndexFunc(*ts, func(t Token) bool { return t.Label == label })
		if i < 0 {
			return fmt.Errorf("pair: no token is labelled %s", label)
		}
		*ts = slices.Delete(*ts, i, i+1)
		return nil
	})
}

// endLinks removes every link's token, as turning children from other
// machines off or on does, so no link minted before comes back.
func endLinks(dir string) error {
	return edit(dir, func(ts *[]Token) error {
		*ts = slices.DeleteFunc(*ts, func(t Token) bool { return t.Link != nil })
		return nil
	})
}

func Tokens(dir string) ([]Token, error) {
	var ts []Token
	if err := jsonfile.Load(filepath.Join(dir, tokensFile), &ts); err != nil {
		return nil, err
	}
	return ts, nil
}

// edit rewrites tokens.json under its lock, so a pair racing a removal
// can't bring the removed token back.
func edit(dir string, change func(*[]Token) error) error {
	_, err := jsonfile.Edit(filepath.Join(dir, tokensFile), change)
	return err
}

// links finds a link in dir's tokens.json as the file is at each call,
// its access narrowed to engine.json's setting for children from other
// machines as that is then: a link whose machine was removed, or a setting
// turned off since the link's socket was admitted, starts and runs nothing
// more.
func links(dir string) engine.Links {
	return func(id api.ID) (api.Link, error) {
		cfg, err := Defaults(dir)
		if err != nil {
			return api.Link{}, err
		}
		if cfg.ChildrenFromOtherMachines == nil {
			return api.Link{}, errors.New("children from other machines are off on this machine")
		}
		ts, err := Tokens(dir)
		if err != nil {
			return api.Link{}, fmt.Errorf("tokens.json can't be read: %w", err)
		}
		i := slices.IndexFunc(ts, func(t Token) bool { return t.Link != nil && t.Link.ID == id })
		if i < 0 {
			return api.Link{}, errors.New("this machine removed the link's machine")
		}
		l := *ts[i].link()
		l.Access = min(l.Access, *cfg.ChildrenFromOtherMachines)
		return l, nil
	}
}

// grants admits a socket whose token tokens.json holds, and a link's only
// while children from other machines are on. It reads both files on every
// handshake, so a removal from another process applies to the next one,
// and every second, to close the sockets a removed token holds.
type grants struct {
	dir  string
	base context.Context

	// mu spans a read of the file through the admission it decides, so a
	// read from before a removal never replaces the grants of a later one.
	mu       sync.Mutex
	live     map[transport.Key]grant
	failing  string               // the last error reading the file, logged once
	refusals map[string]time.Time // when each peer's last refusal was logged
}

type grant struct {
	label  string
	link   *api.Link
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
	cfg, err := Defaults(g.dir)
	linksOn := err == nil && cfg.ChildrenFromOtherMachines != nil
	live := map[transport.Key]grant{}
	for _, t := range ts {
		if t.Link != nil && !linksOn {
			continue
		}
		var sum transport.Key
		if b, _ := hex.DecodeString(t.hash()); len(b) == len(sum) {
			copy(sum[:], b)
		} else {
			continue
		}
		gr, ok := g.live[sum]
		if ok {
			delete(g.live, sum)
		} else {
			gr.label, gr.link = t.Label, t.link()
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

func (g *grants) authorize(r *http.Request, proved func(transport.Key) bool) (transport.Grant, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.reload()
	var key transport.Key
	var match *grant
	// Every key is tried, so the time taken doesn't say which one matched.
	for k, gr := range g.live {
		if proved(k) {
			key, match = k, &gr
		}
	}
	if match == nil {
		g.refused(r.RemoteAddr)
		return transport.Grant{}, errUnknownToken
	}
	slog.Info("admitted a socket", "token", match.label, "link", match.link != nil, "from", r.RemoteAddr)
	return transport.Grant{Key: key, Done: match.ctx, Link: match.link}, nil
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
