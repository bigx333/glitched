package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
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

func TestALinksTokenReachesOnlyItsScopeOnARealEngine(t *testing.T) {
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
	var rej *api.RejectedError
	if _, err := ep.Submit(ctx, api.Command{ID: api.NewID(), Target: api.NewID(), Body: api.ThreadCreate{Title: "x", Model: api.ModelRef{Provider: "fake"}}}); !errors.As(err, &rej) {
		t.Fatalf("a create through the link that isn't a link's create: %v, want it refused", err)
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

// A removal refuses the link's next handshake and closes its socket within a
// second; a link minted again under the label is another link, and the old
// token reaches nothing.
func TestARemovedMachinesLinkIsGoneAndAReMintIsAnotherLink(t *testing.T) {
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
	if err := RemoveToken(dir, "a"); err != nil {
		t.Fatal(err)
	}
	removed := time.Now()
	for {
		if _, err := live.Info(ctx); err != nil {
			break
		}
		if time.Since(removed) > 3*time.Second {
			t.Fatal("the removed link's socket still answers 3s after the removal")
		}
		time.Sleep(50 * time.Millisecond)
	}
	again := transport.NewEndpointClient(transport.Tailnet{Address: addr, Token: old}, api.ID{})
	defer again.Close()
	if _, err := again.Info(ctx); !errors.Is(err, transport.ErrRefused) {
		t.Fatalf("a handshake with the removed link's token: %v, want ErrRefused", err)
	}

	fresh, second, err := PairLink(dir, "a", api.Link{Access: api.AccessAsk})
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID {
		t.Fatal("a link minted again under its label kept the removed one's id")
	}
	if _, err := again.Info(ctx); !errors.Is(err, transport.ErrRefused) {
		t.Fatalf("the removed token after a re-mint under its label: %v, want ErrRefused", err)
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

// remoteEngine starts an engine in dir whose fake agent's turn "split"
// starts kids, and returns its endpoint, the adapter and a top thread.
func remoteEngine(t *testing.T, dir string) (api.Endpoint, *fake.Adapter, api.ID) {
	t.Helper()
	ad := fake.New(fake.Turn(false))
	d, err := Start(context.Background(), Config{Dir: dir, Hub: true, Epoch: 1, Providers: provider.Mux{"fake": ad}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	ep := d.Endpoint()
	// A remote child starts from its parent's branch of its repository.
	repo := t.TempDir()
	gitIn(t, repo, "init", "-q", "-b", "main")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "first")
	parent := api.NewID()
	if _, err := ep.Submit(context.Background(), api.Command{ID: api.NewID(), Target: parent, Body: api.ThreadCreate{Workspace: repo, Title: "top", Model: api.ModelRef{Provider: "fake", Model: "fake"}}}); err != nil {
		t.Fatal(err)
	}
	return ep, ad, parent
}

// spawn has a turn of parent's start kids after anything active and
// returns each create's thread and how it went.
func spawn(t *testing.T, ep api.Endpoint, ad *fake.Adapter, parent api.ID, kids ...fake.Child) ([]api.ID, []error) {
	t.Helper()
	return spawnNowOrAfter(t, ep, ad, parent, api.AfterActive, kids...)
}

// spawnNow is spawn with the turn taken now, beside a waiting child.
func spawnNow(t *testing.T, ep api.Endpoint, ad *fake.Adapter, parent api.ID, kids ...fake.Child) ([]api.ID, []error) {
	t.Helper()
	return spawnNowOrAfter(t, ep, ad, parent, api.StartNow, kids...)
}

func spawnNowOrAfter(t *testing.T, ep api.Endpoint, ad *fake.Adapter, parent api.ID, mode api.DispatchMode, kids ...fake.Child) ([]api.ID, []error) {
	t.Helper()
	text := api.NewID().String()
	ad.Spawns(ep, text, kids...)
	run := api.NewID()
	if _, err := ep.Submit(context.Background(), api.Command{ID: run, Target: parent, Body: api.MessageDispatch{Text: text, Mode: mode}}); err != nil {
		t.Fatal(err)
	}
	ids, errs := make([]api.ID, len(kids)), make([]error, len(kids))
	for i := range kids {
		ids[i] = fake.ChildOf(run, i)
		for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(2 * time.Millisecond) {
			answered, err := ad.Spawned(ids[i])
			if answered {
				errs[i] = err
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("spawn %d was never answered", i)
			}
		}
	}
	return ids, errs
}

func writeMachines(t *testing.T, dir string, content []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, machinesFile), content, 0o600); err != nil {
		t.Fatal(err)
	}
}

func refused(t *testing.T, what string, err error, want string) {
	t.Helper()
	var rej *api.RejectedError
	if !errors.As(err, &rej) || !strings.Contains(rej.Reason, want) {
		t.Fatalf("%s: %v; want refused with %q", what, err, want)
	}
}

// The engine places a remote child only on a machine machines.json lists
// as the file is at the create, so a machine removed while the engine
// runs takes no more, by that machine's engine and link; its token
// reaches neither the thread nor its log.
func TestARemoteChildIsPlacedOnlyOnAMachineInMachinesJSON(t *testing.T) {
	dir := stateDir(t)
	laptop := Machine{Label: "laptop", Server: "laptop:7777", Engine: api.NewID(), Link: api.NewID(), Token: "link-token-5d1e0c3b9a"}
	b, err := json.Marshal([]Machine{laptop})
	if err != nil {
		t.Fatal(err)
	}
	writeMachines(t, dir, b)
	ep, ad, parent := remoteEngine(t, dir)
	ids, errs := spawn(t, ep, ad, parent, fake.Child{Title: "there", Task: "go", Remote: "laptop"}, fake.Child{Title: "nowhere", Task: "go", Remote: "desktop"})
	if errs[0] != nil {
		t.Fatalf("a child on laptop: %v", errs[0])
	}
	refused(t, "a child on desktop, which machines.json doesn't list", errs[1], `no machine called "desktop"`)
	snap, err := ep.Read(context.Background(), api.Query{Thread: ids[0]})
	if err != nil {
		t.Fatal(err)
	}
	created, err := snap.Events[0].Decode()
	if err != nil {
		t.Fatal(err)
	}
	if r := created.(api.ThreadCreated).Remote; r == nil || *r != (api.Remote{Engine: laptop.Engine, Link: laptop.Link}) {
		t.Fatalf("the placeholder's machine: %+v; want laptop's engine and link", r)
	}
	if strings.Contains(string(snap.Events[0].Payload), laptop.Token) {
		t.Fatalf("the create holds the link's token: %s", snap.Events[0].Payload)
	}

	if err := RemoveMachine(dir, "laptop"); err != nil {
		t.Fatal(err)
	}
	_, errs = spawnNow(t, ep, ad, parent, fake.Child{Title: "there again", Task: "go", Remote: "laptop"})
	refused(t, "a child on laptop once it was removed", errs[0], `no machine called "laptop"`)
	for _, name := range []string{"engine.db", "engine.db-wal"} {
		if db, err := os.ReadFile(filepath.Join(dir, name)); err == nil && strings.Contains(string(db), laptop.Token) {
			t.Fatalf("%s holds the link's token", name)
		}
	}
}

// A machines.json that can't be read stops no engine and no local child:
// only a remote child's create is refused, and says why.
func TestAnUnreadableMachinesJSONRefusesOnlyRemoteChildren(t *testing.T) {
	dir := stateDir(t)
	writeMachines(t, dir, []byte("{not json"))
	ep, ad, parent := remoteEngine(t, dir)
	_, errs := spawn(t, ep, ad, parent, fake.Child{Title: "here", Task: "go"}, fake.Child{Title: "there", Task: "go", Remote: "laptop"})
	if errs[0] != nil {
		t.Fatalf("a local child beside an unreadable machines.json: %v; want it started", errs[0])
	}
	refused(t, "a remote child", errs[1], "machines.json can't be read")
}

// A machines.json whose machine lacks its link or its token is as
// unreadable as a corrupt one: no placeholder is made on a machine that
// can't be reached, and local children still start.
func TestAMachineWithoutItsLinkRefusesOnlyRemoteChildren(t *testing.T) {
	for _, tc := range []struct {
		name  string
		entry map[string]any
	}{
		{"no link", map[string]any{"label": "laptop", "server": "laptop:7777", "engine": api.NewID(), "token": "link-token-1"}},
		{"no token", map[string]any{"label": "laptop", "server": "laptop:7777", "engine": api.NewID(), "link": api.NewID()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := stateDir(t)
			b, err := json.Marshal([]map[string]any{tc.entry})
			if err != nil {
				t.Fatal(err)
			}
			writeMachines(t, dir, b)
			ep, ad, parent := remoteEngine(t, dir)
			_, errs := spawn(t, ep, ad, parent, fake.Child{Title: "here", Task: "go"}, fake.Child{Title: "there", Task: "go", Remote: "laptop"})
			if errs[0] != nil {
				t.Fatalf("a local child beside a machine with %s: %v; want it started", tc.name, errs[0])
			}
			refused(t, "a child on a machine with "+tc.name, errs[1], "machines.json can't be read")
		})
	}
}

// Turning children from other machines off on B removes every machine it
// minted a link for at once: a link's next handshake is refused as soon as
// the setting is written, its live socket closes, and its token is gone,
// while a whole-engine token stays. A's row is lost for the removal, and
// the child already running on B is left alone. Turned on again, it starts
// with no links, so the old token stays refused.
func TestTurningChildrenOffRemovesEveryMachine(t *testing.T) {
	p := newRemotes(t, fake.New(fake.Asking()), "fake", engine.Limits{})
	kid := p.place("there")
	there := p.bound(kid).Thread
	until(t, "the child waiting on b", func() bool { return p.placeholder(kid).Remote.Slot == api.RunWaiting })
	if _, err := Pair(p.dirB, "phone"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	live := transport.NewEndpointClient(transport.Tailnet{Address: p.addrB, Token: p.token}, api.ID{})
	defer live.Close()
	if _, err := live.Info(ctx); err != nil {
		t.Fatal(err)
	}
	refused := func(when string) {
		t.Helper()
		ep := transport.NewEndpointClient(transport.Tailnet{Address: p.addrB, Token: p.token}, api.ID{})
		defer ep.Close()
		if _, err := ep.Info(ctx); !errors.Is(err, transport.ErrRefused) {
			t.Fatalf("a handshake with the link's token %s: %v, want ErrRefused", when, err)
		}
	}
	links := func() []Token {
		t.Helper()
		ts, err := Tokens(p.dirB)
		if err != nil {
			t.Fatal(err)
		}
		return slices.DeleteFunc(ts, func(t Token) bool { return t.Link == nil })
	}

	if _, err := Edit(p.dirB, func(cfg *Config) error { cfg.ChildrenFromOtherMachines = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	refused("once children from other machines are off")
	if ls := links(); len(ls) != 0 {
		t.Fatalf("tokens.json keeps %+v once children from other machines are off", ls)
	}
	if ts, _ := Tokens(p.dirB); !slices.ContainsFunc(ts, func(t Token) bool { return t.Label == "phone" && t.Link == nil }) {
		t.Fatalf("tokens.json holds %+v, want phone's whole-engine token kept", ts)
	}
	off := time.Now()
	for {
		if _, err := live.Info(ctx); err != nil {
			break
		}
		if time.Since(off) > 3*time.Second {
			t.Fatal("the link's live socket still answers 3s after children from other machines went off")
		}
		time.Sleep(50 * time.Millisecond)
	}
	p.lostOnce(kid, "b removed this machine, or turned off children from other machines")
	if th := p.onB(there); th.Live() == nil {
		t.Fatal("turning children from other machines off stopped the child already running on b")
	}

	allowChildren(t, p.dirB, api.AccessAutoEdits)
	if ls := links(); len(ls) != 0 {
		t.Fatalf("tokens.json holds %+v once children from other machines are on again, want no link", ls)
	}
	refused("once children from other machines are on again")
}
