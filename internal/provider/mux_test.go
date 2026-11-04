package provider_test

import (
	"context"
	"errors"
	"testing"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
)

type named struct {
	*fake.Adapter
	name string
}

func (n named) Describe(context.Context) (provider.Info, error) {
	return provider.Info{Name: n.name}, nil
}

func TestMuxRoutesByProviderName(t *testing.T) {
	ctx := context.Background()
	m := provider.Mux{"a": named{fake.New(nil), "A"}, "b": named{fake.New(nil), "B"}}
	for name, want := range map[string]string{"a": "A", "b": "B"} {
		if info, err := m.Describe(ctx, name); err != nil || info.Name != want {
			t.Errorf("Describe(%q) = %+v, %v; want %s's adapter", name, info, err, want)
		}
	}

	if _, err := m.Describe(ctx, "nope"); !errors.Is(err, provider.ErrNoAdapter) {
		t.Errorf("Describe of an unknown provider: %v, want ErrNoAdapter", err)
	}
	spec := provider.SessionSpec{Thread: api.NewID(), Run: api.NewID(), Model: api.ModelRef{Provider: "nope"}}
	if _, err := m.Open(ctx, spec); !errors.Is(err, provider.ErrNoAdapter) {
		t.Errorf("Open on an unknown provider: %v, want ErrNoAdapter", err)
	}
	if _, err := m.Reconcile(ctx, provider.RuntimeIdentity{Thread: spec.Thread, Run: spec.Run, Provider: "nope"}); !errors.Is(err, provider.ErrNoAdapter) {
		t.Errorf("Reconcile on an unknown provider: %v, want ErrNoAdapter, since its agent may still run", err)
	}
}
