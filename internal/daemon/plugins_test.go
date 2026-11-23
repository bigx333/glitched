package daemon

import (
	"context"
	"encoding/json"
	"testing"

	"glitched/internal/api"
	"glitched/internal/plugins"
	"glitched/internal/transport"
)

// A service with threads:write alone never starts a thread above Ask for
// approval, whatever the thread the user worked in last runs at: only an
// access it names, which the host gates on threads:access, starts wider.
func TestAServiceCreateStartsAtAskUnlessItNamesAnAccess(t *testing.T) {
	dir := stateDir(t)
	d := start(t, Config{Dir: dir})
	ep := transport.NewEndpointClient(Socket(dir), api.ID{})
	defer ep.Close()
	ctx := context.Background()
	if _, err := ep.Submit(ctx, api.Command{ID: api.NewID(), Target: api.NewID(), Body: api.ThreadCreate{Workspace: "/src/app", Title: "last", Model: api.ModelRef{Provider: "fake", Model: "fake"}, Access: api.AccessFull}}); err != nil {
		t.Fatal(err)
	}
	accessOf := func(args string) api.Access {
		t.Helper()
		out, err := d.pluginEffect("starter", plugins.Effect{Type: "threads.create", Args: json.RawMessage(args)})
		if err != nil {
			t.Fatal(err)
		}
		id, err := api.ParseID(out.(map[string]string)["thread"])
		if err != nil {
			t.Fatal(err)
		}
		snap, err := ep.Read(ctx, api.Query{})
		if err != nil {
			t.Fatal(err)
		}
		for _, th := range snap.Threads {
			if th.ID == id {
				return th.Access
			}
		}
		t.Fatalf("no thread %s", id)
		return 0
	}
	if got := accessOf(`{"prompt": "plain", "workspace": "/src/app"}`); got != api.AccessAsk {
		t.Errorf("a create naming no access started at %v, want Ask for approval (%v)", got, api.AccessAsk)
	}
	if got := accessOf(`{"prompt": "wide", "workspace": "/src/app", "access": "Full access"}`); got != api.AccessFull {
		t.Errorf("a create naming Full access started at %v, want %v", got, api.AccessFull)
	}
	if _, err := d.pluginEffect("starter", plugins.Effect{Type: "threads.create", Args: json.RawMessage(`{"prompt": "odd", "workspace": "/src/app", "access": "root"}`)}); err == nil {
		t.Error("a create naming no level the picker offers was taken")
	}
}
