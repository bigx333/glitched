package client

import (
	"testing"

	"glitched/internal/daemon"
	"glitched/internal/provider/fake"
)

// glitched serve keeps its provider log as it started with, and says so:
// once the switch is saved off, the status the app reads has the saved
// switch off and the engine's live one on.
func TestAnAttachedEngineReportsItsLiveProviderLog(t *testing.T) {
	dir := stateDir(t)
	cfg, err := daemon.Defaults(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ProviderLog = true
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	cfg.Providers = daemon.FakeProviders(fake.New(fake.Turn(false)))
	d, err := daemon.Start(timeout(t), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	m, g := manager(t, dir)
	online(t, g, m.Envs()[0])
	if m.embedded != nil {
		t.Fatal("the app runs an engine of its own")
	}
	if err := m.Configure(func(c *daemon.Config) { c.ProviderLog = false }); err != nil {
		t.Fatal(err)
	}
	m.Inspect()
	g.until(t, "the status", func() bool {
		st := m.Machine()
		return !st.Read.IsZero() && st.Err == nil && st.Engine != nil && !st.Config.ProviderLog
	})
	if st := m.Machine(); !st.Engine.ProviderLog {
		t.Errorf("the engine's status says its provider log is off, as saved; it keeps it on until it restarts")
	}
}
