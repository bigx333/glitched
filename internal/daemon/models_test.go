package daemon

import (
	"context"
	"testing"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
	"glitched/internal/transport"
)

// The engine's providers say over its socket what they run. The ones the
// GUI offers before each has an adapter of its own, codex, gemini and
// grok, are placeholders: they run nothing.
func TestTheSocketSaysWhatModelsAProviderRuns(t *testing.T) {
	dir := stateDir(t)
	cfg, err := Defaults(dir)
	if err != nil {
		t.Fatal(err)
	}
	runs := fake.New(nil)
	runs.Models = []provider.Model{{ID: "m-1", Name: "One", Description: "The first"}}
	cfg.Providers["claude"] = runs
	d, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	local := transport.NewEndpointClient(Socket(dir), api.ID{})
	defer local.Close()
	ctx := context.Background()
	if info, err := local.ProviderInfo(ctx, "claude"); err != nil || len(info.Models) != 1 || info.Models[0] != runs.Models[0] {
		t.Errorf("claude: %+v, %v; want its one model", info, err)
	}
	for _, p := range []string{"codex", "gemini", "grok"} {
		if info, err := local.ProviderInfo(ctx, p); err != nil || len(info.Models) != 0 {
			t.Errorf("%s: %+v, %v; want a placeholder that runs no model", p, info, err)
		}
	}
	if _, err := local.ProviderInfo(ctx, "nobody"); err == nil {
		t.Error("a provider the engine has no adapter for answered no error")
	}
}
