package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"glitched/internal/daemon"
	"glitched/internal/plugins"
	"glitched/internal/plugins/pack"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
)

// seenProvider is a provider that says what reached it, and, when it holds,
// stops only once its ask is ended and released.
type seenProvider struct {
	*fake.Adapter
	arrived chan arrival
	ended   chan struct{}
	release chan struct{}
	hold    bool
}

// arrival is an ask as the provider got it, and whether its context had
// a deadline.
type arrival struct {
	spec    provider.AskSpec
	bounded bool
}

func (f *seenProvider) Ask(ctx context.Context, q provider.AskSpec) (provider.AskResult, error) {
	_, bounded := ctx.Deadline()
	f.arrived <- arrival{q, bounded}
	if !f.hold {
		return provider.AskResult{Text: "ok", Model: "cheap"}, nil
	}
	<-ctx.Done()
	close(f.ended)
	<-f.release
	return provider.AskResult{}, ctx.Err()
}

// askEngine is an app whose asks reach an engine of its own, on its
// socket, with f its one provider, "asking".
func askEngine(t *testing.T, f *seenProvider) (*app, string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "ask")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	d, err := daemon.Start(context.Background(), daemon.Config{Dir: dir, Hub: true, Providers: provider.Mux{"asking": f}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	a := &app{}
	t.Cleanup(func() {
		if a.services != nil {
			a.services.Close()
		}
	})
	return a, dir
}

// askingPlugin runs a plugin of init's that asks through a's askModel, and
// shows the state init comes to.
func askingPlugin(t *testing.T, a *app, dir, init string) string {
	t.Helper()
	bundles := t.TempDir()
	folder := filepath.Join(bundles, "asker")
	if err := os.Mkdir(folder, 0700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"plugin.json": `{"id":"asker","name":"Asker","main":"main.js","contract":"glitched","contractVersion":2,"capabilities":["ui.place","models:ask"],"glitched":{"places":[{"id":"said","at":"thread.header"}]}}`,
		"main.js":     init + `export function place({state,items}) { return items.map(()=>({type:"badge",text:state})); }`,
	}
	for n, c := range files {
		if err := os.WriteFile(filepath.Join(folder, n), []byte(c), 0600); err != nil {
			t.Fatal(err)
		}
	}
	key, err := pack.LoadKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := pack.Sign(folder, key); err != nil {
		t.Fatal(err)
	}
	q := make(chan func(), 1024)
	h := plugins.Open(t.TempDir(), plugins.Options{Bundled: os.DirFS(bundles), ReleaseKeys: []string{key.Public()}, Post: func(fn func()) { q <- fn },
		Ask: func(ctx context.Context, id string, req plugins.AskRequest) (plugins.AskAnswer, error) {
			return a.askModel(ctx, dir, id, req)
		},
	})
	t.Cleanup(h.Close)
	s := h.App("asker")
	if s == nil {
		t.Fatalf("the plugin doesn't run: %+v", h.Installed())
	}
	s.SetItems("said", []plugins.PlaceItem{{Key: "t1", Version: "1"}})
	end := time.After(5 * time.Second)
	for s.PlaceView("said", "t1") == nil {
		select {
		case fn := <-q:
			fn()
		case <-end:
			t.Fatalf("the plugin showed nothing: %v", s.Err)
		}
	}
	return s.PlaceView("said", "t1").Text
}

// An ask whose time is up before it reaches the engine times out there,
// and never reaches the engine with no deadline.
func TestAnAskWithNoTimeLeftNeverReachesTheEngine(t *testing.T) {
	f := &seenProvider{Adapter: fake.New(nil), arrived: make(chan arrival, 1)}
	a, dir := askEngine(t, f)
	said := askingPlugin(t, a, dir, `export async function init(){
		try { await glitched.models.ask("p", {provider:"asking", timeout:1e-10}); }
		catch(e) { return {state:e.code}; }
		return {state:"answered"};
	}`)
	if said != "timeout" {
		t.Errorf("the plugin heard %q, want timeout", said)
	}
	select {
	case got := <-f.arrived:
		t.Errorf("the engine got %+v, bounded %v", got.spec, got.bounded)
	case <-time.After(500 * time.Millisecond):
	}
}

// A canceled ask isn't sent to the engine.
func TestACanceledAskNeverReachesTheEngine(t *testing.T) {
	f := &seenProvider{Adapter: fake.New(nil), arrived: make(chan arrival, 1)}
	a, dir := askEngine(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	cancel()
	if _, err := a.askModel(ctx, dir, "asker", plugins.AskRequest{Prompt: "p", Provider: "asking"}); !errors.Is(err, context.Canceled) {
		t.Errorf("a canceled ask: %v, want context.Canceled", err)
	}
	select {
	case got := <-f.arrived:
		t.Errorf("the engine got %+v", got.spec)
	case <-time.After(500 * time.Millisecond):
	}
}

// An ask that times out holds the plugin's slot while the engine ends it:
// its next ask is busy, and never reaches the engine meanwhile.
func TestAnAskThatTimesOutHoldsItsSlotTillTheEngineEndsIt(t *testing.T) {
	f := &seenProvider{Adapter: fake.New(nil), arrived: make(chan arrival, 2), hold: true, ended: make(chan struct{}), release: make(chan struct{})}
	a, dir := askEngine(t, f)
	defer close(f.release)
	said := askingPlugin(t, a, dir, `export async function init(){
		let first = "", second = "";
		try { await glitched.models.ask("first", {provider:"asking", timeout:0.3}); } catch(e) { first = e.code; }
		try { await glitched.models.ask("second", {provider:"asking", timeout:1}); } catch(e) { second = e.code; }
		return {state:first + "/" + second};
	}`)
	select {
	case <-f.ended:
	case <-time.After(3 * time.Second):
		t.Fatal("the engine didn't end the ask")
	}
	first := <-f.arrived
	if said != "timeout/busy" || !first.bounded || first.spec.Timeout <= 0 || len(f.arrived) != 0 {
		t.Errorf("the plugin heard %q; the engine got %+v, bounded %v, and %d more", said, first.spec, first.bounded, len(f.arrived))
	}
}
