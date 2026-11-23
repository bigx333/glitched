package mcp_test

import (
	"context"
	"strings"
	"testing"

	"glitched/internal/api"
	"glitched/internal/mcp"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
	"glitched/internal/transport"
)

// listed lists mux's providers as the engine lists them to glitched mcp.
type listed provider.Mux

func (m listed) Providers(ctx context.Context, _, _ bool) ([]transport.ProviderInfo, error) {
	var out []transport.ProviderInfo
	for _, name := range provider.Mux(m).Names() {
		info, err := provider.Mux(m).Describe(ctx, name)
		p := transport.ProviderInfo{Name: name, Info: info}
		if err != nil {
			p.Error = err.Error()
		}
		out = append(out, p)
	}
	return out, nil
}

// twoProviders is an engine's mux of fake, the caller's, and other, whose
// models are o-1, its default, at low or high, and o-2.
func twoProviders() provider.Mux {
	other := fake.New(fake.Turn(true))
	other.Models = []provider.Model{
		{ID: "o-1", Default: true, Efforts: []provider.Effort{{ID: "low"}, {ID: "high"}}},
		{ID: "o-2"},
	}
	return provider.Mux{"fake": fake.New(fake.Turn(true)), "other": other}
}

// A child on another provider gets none of the caller's model, effort or
// Fast, so that provider's program runs its own default, and it runs at
// the caller's access, which that provider's adapter makes its own. The
// child's first turn runs on that provider.
func TestADelegateOnAnotherProviderRunsAtThatProvidersDefault(t *testing.T) {
	mux := twoProviders()
	h := engineOf(t, mux)
	o := workingOn(t, h, api.ModelRef{Provider: "fake", Model: "fake", Effort: "max", Fast: true}, api.AccessAutoEdits)
	configure(t, h, o.Thread, api.ThreadConfigure{Access: new(api.AccessFull)})
	got := serveTools(t, &mcp.Tools{Engine: h, Origin: o, Providers: listed(mux)},
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
	mux := twoProviders()
	h := engineOf(t, mux)
	o := workingOn(t, h, api.ModelRef{Provider: "other", Effort: "low"}, api.AccessAsk)
	got := serveTools(t, &mcp.Tools{Engine: h, Origin: o, Providers: listed(mux)},
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
	mux := twoProviders()
	h := engineOf(t, mux)
	o := working(t, h)
	got := serveTools(t, &mcp.Tools{Engine: h, Origin: o, Providers: listed(mux)},
		delegate(1, `{"task":"x","provider":"nope"}`, ""),
		delegate(2, `{"task":"x","provider":"other","model":"gpt"}`, ""),
		delegate(3, `{"task":"x","provider":"other","effort":"max"}`, ""),
		delegate(4, `{"task":"x","provider":"other","model":"o-1","effort":"max"}`, ""),
	)
	for id, want := range map[int]string{
		1: `no provider "nope"; it has fake, other`,
		2: `other has no model "gpt"; its models are o-1, o-2`,
		3: `o-1 runs at no effort "max"; its efforts are low, high`,
		4: `o-1 runs at no effort "max"; its efforts are low, high`,
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
