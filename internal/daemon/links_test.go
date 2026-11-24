package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/transport"
)

// allowChildren turns on children from other machines in dir, at ceiling.
func allowChildren(t *testing.T, dir string, ceiling api.Access) {
	t.Helper()
	if _, err := Edit(dir, func(cfg *Config) error {
		cfg.ChildrenFromOtherMachines = &ceiling
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestALinkIsMintedOnlyWithinThisMachinesSetting(t *testing.T) {
	dir := stateDir(t)
	if _, _, err := PairLink(dir, "a", api.Link{Access: api.AccessAsk}); err == nil || !strings.Contains(err.Error(), "off") {
		t.Fatalf("a link minted while children from other machines are off: %v", err)
	}
	allowChildren(t, dir, api.AccessAutoEdits)
	if _, _, err := PairLink(dir, "a", api.Link{Access: api.AccessFull}); err == nil {
		t.Fatal("a full link minted under an auto-edits setting")
	}
	_, l, err := PairLink(dir, "a", api.Link{Access: api.AccessAutoEdits, Working: 2, All: 4})
	if err != nil {
		t.Fatal(err)
	}
	if l.ID == (api.ID{}) || l.Label != "a" || l.Access != api.AccessAutoEdits || l.Working != 2 || l.All != 4 {
		t.Fatalf("minted %+v", l)
	}
	if _, _, err := PairLink(dir, "a", api.Link{}); err == nil {
		t.Fatal("a second link under a label that holds one")
	}
	if _, err := Pair(dir, "a"); err == nil {
		t.Fatal("a token under a label a link holds")
	}
	ts, err := Tokens(dir)
	if err != nil || len(ts) != 1 || ts[0].Link == nil || ts[0].Link.ID != l.ID {
		t.Fatalf("tokens.json after the mint: %+v %v", ts, err)
	}
}

// A build from before links admits every top-level sha256 in tokens.json
// to the whole engine. A link's hash is never there, so that build skips it.
func TestABuildFromBeforeLinksAdmitsNoLink(t *testing.T) {
	dir := stateDir(t)
	allowChildren(t, dir, api.AccessAutoEdits)
	if _, _, err := PairLink(dir, "a", api.Link{Access: api.AccessAsk}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, tokensFile))
	if err != nil {
		t.Fatal(err)
	}
	var before []struct {
		Label string `json:"label"`
		Hash  string `json:"sha256"`
	}
	if err := json.Unmarshal(b, &before); err != nil || len(before) != 1 || before[0].Hash != "" {
		t.Fatalf("tokens.json as a build from before links reads it: %+v %v, want no hash", before, err)
	}
}

func TestALinksTokenReachesOnlyInfoOnARealEngine(t *testing.T) {
	quiet(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dir := stateDir(t)
	allowChildren(t, dir, api.AccessAutoEdits)
	full, err := Pair(dir, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	token, l, err := PairLink(dir, "a", api.Link{Access: api.AccessAutoEdits})
	if err != nil {
		t.Fatal(err)
	}
	d := start(t, Config{Dir: dir, Listen: []string{"127.0.0.1:0"}})
	addr := d.Addrs()[0].String()

	ep := transport.NewEndpointClient(transport.Tailnet{Address: addr, Token: token}, api.ID{})
	defer ep.Close()
	env, err := ep.Info(ctx)
	if err != nil || env.Link != l.ID || env.ID == (api.ID{}) || env.Tailnet != (api.Tailnet{}) {
		t.Fatalf("info through the link: %+v %v, want the engine, link %s and no tailnet", env, err, l.ID)
	}
	if _, err := ep.Read(ctx, api.Query{}); !errors.Is(err, api.ErrNotAllowed) {
		t.Fatalf("read through the link: %v, want api.ErrNotAllowed", err)
	}
	if _, err := ep.Submit(ctx, api.Command{ID: api.NewID(), Target: api.NewID(), Body: api.ThreadCreate{Title: "x", Model: api.ModelRef{Provider: "fake"}}}); !errors.Is(err, api.ErrNotAllowed) {
		t.Fatalf("a create through the link: %v, want api.ErrNotAllowed", err)
	}

	user := transport.NewEndpointClient(transport.Tailnet{Address: addr, Token: full}, api.ID{})
	defer user.Close()
	if env, err := user.Info(ctx); err != nil || env.Link != (api.ID{}) {
		t.Fatalf("info with a whole engine's token: %+v %v, want no link", env, err)
	}
	if _, err := user.Read(ctx, api.Query{}); err != nil {
		t.Fatalf("read with a whole engine's token: %v", err)
	}
}

// A revoke refuses the link's next handshake and closes its socket within a
// second; a link minted again under the label is another link, and the old
// token reaches nothing.
func TestARevokedLinkIsGoneAndAReMintIsAnotherLink(t *testing.T) {
	quiet(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dir := stateDir(t)
	allowChildren(t, dir, api.AccessAutoEdits)
	old, first, err := PairLink(dir, "a", api.Link{Access: api.AccessAsk})
	if err != nil {
		t.Fatal(err)
	}
	d := start(t, Config{Dir: dir, Listen: []string{"127.0.0.1:0"}})
	addr := d.Addrs()[0].String()
	live := transport.NewEndpointClient(transport.Tailnet{Address: addr, Token: old}, api.ID{})
	defer live.Close()
	sub, err := live.Open(ctx, api.Subscription{Thread: api.NewID()})
	if err == nil {
		sub.Close()
		t.Fatal("a subscribe through the link opened")
	}
	if _, err := live.Info(ctx); err != nil {
		t.Fatal(err)
	}
	if err := Revoke(dir, "a"); err != nil {
		t.Fatal(err)
	}
	revoked := time.Now()
	for {
		if _, err := live.Info(ctx); err != nil {
			break
		}
		if time.Since(revoked) > 3*time.Second {
			t.Fatal("the revoked link's socket still answers 3s after the revoke")
		}
		time.Sleep(50 * time.Millisecond)
	}
	again := transport.NewEndpointClient(transport.Tailnet{Address: addr, Token: old}, api.ID{})
	defer again.Close()
	if _, err := again.Info(ctx); !errors.Is(err, transport.ErrRefused) {
		t.Fatalf("a handshake with the revoked link's token: %v, want ErrRefused", err)
	}

	fresh, second, err := PairLink(dir, "a", api.Link{Access: api.AccessAsk})
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID {
		t.Fatal("a link minted again under its label kept the revoked one's id")
	}
	if _, err := again.Info(ctx); !errors.Is(err, transport.ErrRefused) {
		t.Fatalf("the revoked token after a re-mint under its label: %v, want ErrRefused", err)
	}
	ep := transport.NewEndpointClient(transport.Tailnet{Address: addr, Token: fresh}, api.ID{})
	defer ep.Close()
	if env, err := ep.Info(ctx); err != nil || env.Link != second.ID {
		t.Fatalf("info through the new link: %+v %v, want link %s", env, err, second.ID)
	}
}

func TestAMachineIsAddedOnlyWithALinksToken(t *testing.T) {
	quiet(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	b := stateDir(t)
	allowChildren(t, b, api.AccessAutoEdits)
	full, err := Pair(b, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	token, l, err := PairLink(b, "a", api.Link{Access: api.AccessAutoEdits})
	if err != nil {
		t.Fatal(err)
	}
	d := start(t, Config{Dir: b, Listen: []string{"127.0.0.1:0"}})
	addr := d.Addrs()[0].String()
	engine, err := d.Endpoint().Info(ctx)
	if err != nil {
		t.Fatal(err)
	}

	a := stateDir(t)
	if _, err := AddMachine(ctx, a, "b", addr, full); err == nil || !strings.Contains(err.Error(), "not a link") {
		t.Fatalf("a machine added with a whole engine's token: %v", err)
	}
	m, err := AddMachine(ctx, a, "b", addr, token)
	if err != nil {
		t.Fatal(err)
	}
	if m != (Machine{Label: "b", Server: addr, Engine: engine.ID, Link: l.ID, Token: token}) {
		t.Fatalf("added %+v, want engine %s and link %s", m, engine.ID, l.ID)
	}
	if _, err := AddMachine(ctx, a, "b", addr, token); err == nil {
		t.Fatal("a second machine under a label that holds one")
	}
	if _, err := AddMachine(ctx, a, "alias", addr, token); err == nil {
		t.Fatal("the same link added again under another label")
	}
	ms, err := Machines(a)
	if err != nil || len(ms) != 1 || ms[0] != m || ms[0].Token != token {
		t.Fatalf("machines.json: %+v %v, want one machine with its token", ms, err)
	}
	info, err := os.Stat(filepath.Join(a, machinesFile))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("machines.json: %v %v, want mode 0600", info, err)
	}
	if raw, err := os.ReadFile(filepath.Join(a, configFile)); err == nil && strings.Contains(string(raw), token) {
		t.Fatal("engine.json holds the link's token")
	}
	if err := RemoveMachine(a, "b"); err != nil {
		t.Fatal(err)
	}
	if ms, err := Machines(a); err != nil || len(ms) != 0 {
		t.Fatalf("machines.json after the remove: %+v %v", ms, err)
	}
	if err := RemoveMachine(a, "b"); err == nil {
		t.Fatal("a second remove of a machine that is gone")
	}
}
