package mcp_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"glitched/internal/api"
	"glitched/internal/mcp"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
)

// catalog is a provider whose program says what Describe and
// DefaultModel answer, as a test sets them, over a fake adapter's runs.
type catalog struct {
	*fake.Adapter
	mu       sync.Mutex
	info     provider.Info
	err      error
	def      map[string]string // by workspace
	describe chan struct{}     // when set, Describe waits on it or its ctx
}

func (c *catalog) set(info provider.Info, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.info, c.err = info, err
}

func (c *catalog) Describe(ctx context.Context) (provider.Info, error) {
	if c.describe != nil {
		select {
		case <-c.describe:
		case <-ctx.Done():
			return provider.Info{}, ctx.Err()
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.info, c.err
}

func (c *catalog) DefaultModel(_ context.Context, workspace string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.def[workspace], nil
}

// twoProviders is an engine's mux of fake, the caller's, and other, whose
// models are o-1, its default, at low or high, and o-2.
func twoProviders() (provider.Mux, *catalog) {
	other := &catalog{Adapter: fake.New(fake.Turn(true))}
	other.set(provider.Info{Name: "other", Models: []provider.Model{
		{ID: "o-1", Default: true, Efforts: []provider.Effort{{ID: "low"}, {ID: "high"}}},
		{ID: "o-2"},
		{ID: "o-3", Efforts: []provider.Effort{{ID: "max"}}},
	}}, nil)
	return provider.Mux{"fake": fake.New(fake.Turn(true)), "other": other}, other
}

// A child on another provider gets none of the caller's model, effort or
// Fast, so that provider's program runs its own default, and it runs at
// the caller's access, which that provider's adapter makes its own. The
// child's first turn runs on that provider.
func TestADelegateOnAnotherProviderRunsAtThatProvidersDefault(t *testing.T) {
	mux, _ := twoProviders()
	h := engineOf(t, mux)
	o := workingOn(t, h, api.ModelRef{Provider: "fake", Model: "fake", Effort: "max", Fast: true}, api.AccessAutoEdits)
	configure(t, h, o.Thread, api.ThreadConfigure{Access: new(api.AccessFull)})
	got := serveTools(t, &mcp.Tools{Engine: h, Origin: o, Providers: mux},
		delegate(1, `{"task":"Review the diff","provider":"other"}`, ""),
		delegate(2, `{"task":"Review it again","provider":"other","model":"o-2"}`, ""),
		delegate(3, `{"task":"Review it at high","provider":"other","effort":"high"}`, ""),
	)
	for id, r := range got {
		if r.Error != nil || r.Result.IsError {
			t.Fatalf("call %d: %+v", id, r)
		}
	}
	want := map[string]api.ModelRef{
		"Review the diff":   {Provider: "other"},
		"Review it again":   {Provider: "other", Model: "o-2"},
		"Review it at high": {Provider: "other", Effort: "high"},
	}
	kids := children(t, h, o.Thread)
	if len(kids) != len(want) {
		t.Fatalf("%d children, want %d: %+v", len(kids), len(want), kids)
	}
	for _, k := range kids {
		if w, ok := want[k.Title]; !ok || k.Model != w || k.Access != api.AccessAutoEdits || k.Turn == nil || k.Turn.Model != w {
			t.Errorf("child %q at %+v and %v with turn %+v, want %+v at the turn's AutoEdits, its turn too", k.Title, k.Model, k.Access, k.Turn, w)
		}
	}
}

// A child on the caller's own provider keeps what the caller's turn runs
// at, and one of a turn that follows its program's default sends no model
// either, so the program still picks.
func TestADelegateOnTheCallersProviderFollowsWhatTheTurnFollows(t *testing.T) {
	mux, _ := twoProviders()
	h := engineOf(t, mux)
	o := workingOn(t, h, api.ModelRef{Provider: "other", Effort: "low"}, api.AccessAsk)
	got := serveTools(t, &mcp.Tools{Engine: h, Origin: o, Providers: mux},
		delegate(1, `{"task":"Same provider, named","provider":"other"}`, ""),
		delegate(2, `{"task":"Same provider, unnamed"}`, ""),
	)
	for id, r := range got {
		if r.Error != nil || r.Result.IsError {
			t.Fatalf("call %d: %+v", id, r)
		}
	}
	for _, k := range children(t, h, o.Thread) {
		if k.Model != (api.ModelRef{Provider: "other", Effort: "low"}) {
			t.Errorf("child %q at %+v, want the turn's provider and effort with no model", k.Title, k.Model)
		}
	}
}

// A provider, model or effort the engine doesn't offer starts nothing, and
// the agent reads the ones there are.
func TestADelegateToWhatTheEngineDoesntOfferNamesWhatItDoes(t *testing.T) {
	mux, _ := twoProviders()
	h := engineOf(t, mux)
	o := working(t, h)
	got := serveTools(t, &mcp.Tools{Engine: h, Origin: o, Providers: mux},
		delegate(1, `{"task":"x","provider":"nope"}`, ""),
		delegate(2, `{"task":"x","provider":"other","model":"gpt"}`, ""),
		delegate(3, `{"task":"x","provider":"other","effort":"max"}`, ""),
		delegate(4, `{"task":"x","provider":"other","model":"o-1","effort":"max"}`, ""),
		delegate(5, `{"task":"x","provider":"other","model":"o-2","effort":"high"}`, ""),
		delegate(6, `{"task":"x","provider":"nope"}`, ""),
	)
	for id, want := range map[int]string{
		1: `nope can't run a child`,
		2: `other has no model "gpt"; its models are o-1, o-2, o-3`,
		3: `o-1 runs at no effort "max"; its efforts are low, high`,
		4: `o-1 runs at no effort "max"; its efforts are low, high`,
		5: `offers no efforts for o-2, so it would run at its own: leave effort out`,
		6: `no adapter for "nope" on this engine, which has fake, other`,
	} {
		if r := got[id]; !r.Result.IsError || len(r.Result.Content) == 0 || !strings.Contains(r.Result.Content[0].Text, want) {
			t.Errorf("call %d: %+v, want an error with %q", id, r, want)
		}
	}
	if n := len(children(t, h, o.Thread)); n != 0 {
		t.Errorf("%d children, want none", n)
	}
}

// A server that can't list the engine's providers starts no child on
// another, rather than one on a provider nobody checked.
func TestAServerWithNoProviderListKeepsItsChildrenOnTheCallersProvider(t *testing.T) {
	h := engine(t)
	o := working(t, h)
	got := serve(t, h, o, delegate(1, `{"task":"x","provider":"other"}`, ""), delegate(2, `{"task":"y","provider":"fake"}`, ""))
	if r := got[1]; !r.Result.IsError || !strings.Contains(r.Result.Content[0].Text, "another provider") {
		t.Errorf("call 1: %+v, want a refusal", r)
	}
	if r := got[2]; r.Error != nil || r.Result.IsError {
		t.Errorf("call 2, on the caller's provider: %+v", r)
	}
	if kids := children(t, h, o.Thread); len(kids) != 1 || kids[0].Model.Provider != "fake" {
		t.Errorf("children %+v, want one on fake", kids)
	}
}

// An effort named with no model is checked against the model the child's
// program runs by default where it runs, as its settings there say, and
// still goes out with no model.
func TestAnEffortWithNoModelIsCheckedAgainstTheWorkspacesDefault(t *testing.T) {
	mux, other := twoProviders()
	other.def = map[string]string{"/src/app": "o-3"}
	h := engineOf(t, mux)
	o := working(t, h)
	got := serveTools(t, &mcp.Tools{Engine: h, Origin: o, Providers: mux},
		delegate(1, `{"task":"At max","provider":"other","effort":"max"}`, ""),
		delegate(2, `{"task":"At low","provider":"other","effort":"low"}`, ""),
	)
	if r := got[1]; r.Error != nil || r.Result.IsError {
		t.Fatalf("max, which the workspace's default o-3 runs at: %+v", r)
	}
	if r := got[2]; !r.Result.IsError || !strings.Contains(r.Result.Content[0].Text, `o-3 runs at no effort "low"; its efforts are max`) {
		t.Errorf("low, which o-3 doesn't run at: %+v", r)
	}
	if kids := children(t, h, o.Thread); len(kids) != 1 || kids[0].Model != (api.ModelRef{Provider: "other", Effort: "max"}) {
		t.Errorf("children %+v, want one at max with no model", kids)
	}
}

// A retry finds the child its first try made though the provider can no
// longer say what it offers.
func TestARetryFindsItsChildThoughTheProviderNoLongerAnswers(t *testing.T) {
	mux, other := twoProviders()
	h := engineOf(t, mux)
	o := working(t, h)
	tools := &mcp.Tools{Engine: h, Origin: o, Providers: mux}
	calls := []string{
		delegate(1, `{"task":"Review","provider":"other","client_request_id":"r-1"}`, ""),
		delegate(2, `{"task":"Check","provider":"other","model":"o-2"}`, `{"claudecode/toolUseId":"toolu_B"}`),
	}
	first := serveTools(t, tools, calls...)
	other.set(provider.Info{}, errors.New("signed out"))
	again := serveTools(t, tools, calls...)
	for id := 1; id <= 2; id++ {
		if a, b := first[id], again[id]; a.Result.IsError || b.Result.IsError || a.Result.StructuredContent.TaskID != b.Result.StructuredContent.TaskID {
			t.Errorf("call %d: first %+v, retry %+v; want the same child twice", id, a, b)
		}
	}
	if n := len(children(t, h, o.Thread)); n != 2 {
		t.Errorf("%d children, want 2", n)
	}
}

// A provider that never answers holds up no delegate to another.
func TestAProviderThatDoesntAnswerHoldsUpNoOther(t *testing.T) {
	mux, _ := twoProviders()
	stuck := &catalog{Adapter: fake.New(fake.Turn(true)), describe: make(chan struct{})}
	mux["stuck"] = stuck
	h := engineOf(t, mux)
	o := working(t, h)
	if r := serveTools(t, &mcp.Tools{Engine: h, Origin: o, Providers: mux}, delegate(1, `{"task":"x","provider":"other"}`, ""))[1]; r.Error != nil || r.Result.IsError {
		t.Errorf("a delegate to other while stuck doesn't answer: %+v", r)
	}
}
