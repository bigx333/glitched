package client

import (
	"slices"
	"strings"
	"testing"

	"glitched/internal/transport"
)

// The window asks an engine for its providers and an update the same way
// whether it embeds the engine or attached to one serving its socket.
func TestProvidersAndUpdatesReachTheEngineEitherWay(t *testing.T) {
	for _, attached := range []bool{false, true} {
		dir := stateDir(t)
		if attached {
			engine(t, dir)
		}
		m, g := manager(t, dir)
		env := m.Envs()[0]
		online(t, g, env)

		var served []transport.ProviderInfo
		var err error
		answered := false
		m.Providers(env, false, true, func(p []transport.ProviderInfo, e error) { served, err, answered = p, e, true })
		g.until(t, "the providers", func() bool { return answered })
		if err != nil || !slices.ContainsFunc(served, func(p transport.ProviderInfo) bool { return p.Name == "fake" }) {
			t.Errorf("attached %v: providers %+v, %v", attached, served, err)
		}

		var res transport.UpdateResult
		answered = false
		m.UpdateProvider(env, "fake", false, func(r transport.UpdateResult, e error) { res, err, answered = r, e, true })
		g.until(t, "the update", func() bool { return answered })
		if err != nil || !strings.Contains(res.Error, "can't tell how fake was installed") {
			t.Errorf("attached %v: update %+v, %v; want it refused, as the fake runs no program", attached, res, err)
		}
	}
}
